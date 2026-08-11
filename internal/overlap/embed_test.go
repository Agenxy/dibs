package overlap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A stand-in for any OpenAI-compatible embeddings service. Deterministic: the
// vector is derived from the text, so "the same text embeds the same way" holds
// without a model.
func fakeEmbeddings(t *testing.T, mutate func(w http.ResponseWriter, in []string) bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req struct {
			Model string          `json:"model"`
			Input json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var texts []string
		if err := json.Unmarshal(req.Input, &texts); err != nil {
			var one string
			if json.Unmarshal(req.Input, &one) == nil {
				texts = []string{one}
			}
		}
		if mutate != nil && mutate(w, texts) {
			return
		}
		type item struct {
			Object    string    `json:"object"`
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		out := struct {
			Object string `json:"object"`
			Data   []item `json:"data"`
		}{Object: "list"}
		for i, txt := range texts {
			out.Data = append(out.Data, item{Object: "embedding", Index: i, Embedding: vecFor(txt)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// vecFor is a bag-of-words vector: texts sharing words point in similar
// directions, which is all the retrieval assertions below need.
func vecFor(s string) []float32 {
	v := make([]float32, 32)
	for _, tok := range strings.Fields(strings.ToLower(s)) {
		h := 0
		for _, r := range tok {
			h = h*31 + int(r)
		}
		v[((h%32)+32)%32] += 1
	}
	return v
}

func TestEmbedAcceptsBaseURLWithOrWithoutV1(t *testing.T) {
	// The single most likely configuration mistake, and it costs nothing to
	// accept both spellings.
	srv := fakeEmbeddings(t, nil)
	for _, base := range []string{srv.URL, srv.URL + "/v1", srv.URL + "/v1/", srv.URL + "/"} {
		e := NewEmbed(base, "m", "", 5*time.Second)
		if err := e.Probe(context.Background()); err != nil {
			t.Fatalf("base %q: %v", base, err)
		}
	}
}

func TestEmbedSendsBearerTokenWhenGiven(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()
	e := NewEmbed(srv.URL, "m", "sk-secret", 5*time.Second)
	if err := e.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer sk-secret" {
		t.Fatalf("authorization header = %q", seen)
	}
}

// The failure that would corrupt an entire index silently: a service returning
// fewer vectors than inputs shifts every later vector onto the wrong file.
func TestEmbedRejectsAShortBatch(t *testing.T) {
	srv := fakeEmbeddings(t, func(w http.ResponseWriter, in []string) bool {
		if len(in) < 2 {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0]}]}`))
		return true
	})
	e := NewEmbed(srv.URL, "m", "", 5*time.Second)
	_, err := e.encode(context.Background(), []string{"a", "b", "c"})
	if err == nil {
		t.Fatal("a short batch must be an error, not a silently misaligned index")
	}
	if !strings.Contains(err.Error(), "1 vectors for 3 inputs") {
		t.Fatalf("error should name the mismatch: %v", err)
	}
}

// Some servers reorder; `index` is authoritative when present.
func TestEmbedHonoursTheReturnedIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Deliberately out of order: input 0 arrives second.
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"object":"embedding","index":1,"embedding":[0,1]},
			{"object":"embedding","index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()
	e := NewEmbed(srv.URL, "m", "", 5*time.Second)
	got, err := e.encode(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0][0] != 1 || got[1][1] != 1 {
		t.Fatalf("vectors were not reordered by index: %v", got)
	}
}

func TestEmbedSurfacesAnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()
	e := NewEmbed(srv.URL, "nope", "", 5*time.Second)
	err := e.Probe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("the service's own message must reach the operator: %v", err)
	}
}

func TestEmbedNormalisesSoSimilarityIsBounded(t *testing.T) {
	srv := fakeEmbeddings(t, nil)
	e := NewEmbed(srv.URL, "m", "", 5*time.Second)
	got, err := e.encode(context.Background(), []string{"alpha beta gamma"})
	if err != nil {
		t.Fatal(err)
	}
	if d := dot(got[0], got[0]); d < 0.999 || d > 1.001 {
		t.Fatalf("vectors must be unit length so a dot product is a cosine: %v", d)
	}
}

func TestEmbedIndexesARepoAndRetrievesFromIt(t *testing.T) {
	ctx := context.Background()
	repo := repoRoot(t)
	srv := fakeEmbeddings(t, nil)
	e := NewEmbed(srv.URL, "m", "", 30*time.Second)

	if _, err := e.Predict(ctx, "anything", 5); err == nil {
		t.Fatal("predicting before Build must fail loudly rather than return nothing")
	}
	if err := e.Build(ctx, repo); err != nil {
		t.Skipf("cannot index this checkout: %v", err)
	}
	if e.Chunks() == 0 {
		t.Fatal("index is empty")
	}
	pred, err := e.Predict(ctx, "claim guard denies edits to claimed paths", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pred.Files) == 0 {
		t.Fatal("a declaration in this repo's own vocabulary should retrieve something")
	}
	// Provenance names the MODEL, and the version identifies the index build,
	// both are recorded in the join op and must not be empty.
	if !strings.Contains(pred.ScorerID, "m") || pred.Version == "0" {
		t.Fatalf("provenance incomplete: %q / %q", pred.ScorerID, pred.Version)
	}
	for _, f := range pred.Files {
		if f.Weight <= 0 {
			t.Fatalf("negative similarity is noise and must be dropped: %+v", f)
		}
	}
}

// A file scores as its BEST chunk. Averaging would bury a large file that
// contains one highly relevant function under its own boilerplate.
func TestEmbedScoresAFileByItsBestChunk(t *testing.T) {
	e := &Embed{
		built: true,
		paths: []string{"big.go", "big.go", "big.go", "small.go"},
		vecs: [][]float32{
			normalise([]float32{0, 1}), // irrelevant chunk
			normalise([]float32{1, 0}), // the matching one
			normalise([]float32{0, 1}), // irrelevant chunk
			normalise([]float32{0.7, 0.7}),
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()
	e.base, e.client, e.model = srv.URL, srv.Client(), "m"

	pred, err := e.Predict(context.Background(), "q", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pred.Files) == 0 || pred.Files[0].Path != "big.go" {
		t.Fatalf("the file holding the best chunk must rank first: %+v", pred.Files)
	}
}

// Cosine similarity has a high floor, and renormalising against the maximum
// destroys the only signal that matters.
//
// Two unrelated English texts embed around 0.3–0.7 with a modern model; nothing
// lands near zero. topN maps [0,max] onto [0,1], which is right for tier-0 term
// counts (a file sharing no terms genuinely scores 0) and catastrophic here:
// chunks at 0.70 and 0.83 become 0.84 and 1.00. Measured on a three-file
// fixture before the fix, "writing release notes for the changelog" scored
// 0.729 against an authentication agent: a false positive confident enough to
// put every agent in one agent.
func TestEmbedRescalesAgainstTheQuerysOwnDistribution(t *testing.T) {
	// Four chunks. The query is strongly about the first, mildly about the
	// second, and unrelated to the rest, but every raw cosine is high, as real
	// ones are.
	e := &Embed{
		built: true,
		paths: []string{"auth.go", "retry.go", "style.css", "notes.md"},
		vecs: [][]float32{
			normalise([]float32{1.00, 0.10}),
			normalise([]float32{0.90, 0.30}),
			normalise([]float32{0.72, 0.69}),
			normalise([]float32{0.70, 0.71}),
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()
	e.base, e.client, e.model = srv.URL, srv.Client(), "m"

	pred, err := e.Predict(context.Background(), "q", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, f := range pred.Files {
		got[f.Path] = f.Weight
	}
	if got["auth.go"] <= 0 {
		t.Fatalf("the strongly-related chunk must survive: %+v", pred.Files)
	}
	// The unrelated ones sit at or below the query's own typical similarity and
	// must be dropped outright: weak evidence here is no evidence.
	for _, p := range []string{"style.css", "notes.md"} {
		if w, ok := got[p]; ok {
			t.Errorf("%s is unrelated and must not be predicted at all, got %.3f", p, w)
		}
	}
	if got["auth.go"] <= got["retry.go"] {
		t.Errorf("ordering must survive the rescale: auth=%.3f retry=%.3f",
			got["auth.go"], got["retry.go"])
	}
}

// A query that matches everything equally carries no information, and saying
// "everything at 1.0" would be the most confident possible way to be useless.
func TestEmbedReturnsNothingWhenEveryChunkIsEquallySimilar(t *testing.T) {
	v := normalise([]float32{1, 1})
	e := &Embed{built: true, paths: []string{"a.go", "b.go", "c.go"}, vecs: [][]float32{v, v, v}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,1]}]}`))
	}))
	defer srv.Close()
	e.base, e.client, e.model = srv.URL, srv.Client(), "m"

	pred, err := e.Predict(context.Background(), "q", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pred.Files) != 0 {
		t.Fatalf("no discrimination means no prediction, got %+v", pred.Files)
	}
}

// A deadline must scale with how much work was asked for.
//
// One flat timeout treats a one-word probe and a 64-chunk batch as the same
// request. Measured: both 4B models failed on chunk 0 of 449: the very first
// batch: with the client timeout exceeded, having succeeded at the same batch
// size on an idle machine. A production box under load hits this first, and
// "context deadline exceeded" names no knob.
func TestEmbedDeadlineScalesWithBatchSize(t *testing.T) {
	e := NewEmbed("http://x", "m", "", 30*time.Second)
	one := e.deadlineFor(1)
	batch := e.deadlineFor(64)
	if batch <= one {
		t.Fatalf("a 64-chunk batch must get longer than a single probe: %s vs %s", batch, one)
	}
	if capped := e.deadlineFor(1_000_000); capped > maxEncodeDeadline {
		t.Fatalf("must stay bounded, got %s", capped)
	}
}

// A zero timeout means "use the default", never "already expired". net/http
// read a zero Timeout as no-timeout, so anything building Embed directly used
// to work and would now fail instantly with a message blaming a slow model.
func TestEmbedZeroTimeoutDoesNotExpireInstantly(t *testing.T) {
	e := &Embed{} // no NewEmbed, so timeout is the zero value
	if d := e.deadlineFor(1); d <= 0 {
		t.Fatalf("a zero timeout must fall back to a usable default, got %s", d)
	}
}
