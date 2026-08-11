package overlap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Embed is the tier-2/3 scorer: Dibs owns the index, an external service owns
// the model.
//
// THE BOUNDARY, and why it moved here.
//
// The first version put the repository index inside a sidecar we shipped, and
// asked it a bespoke question, "given this declaration, which files?", over an
// endpoint we invented. That was incoherent in a way worth recording, because
// the shape is tempting:
//
//   - It claimed to let you plug in any inference service, but no inference
//     service on earth speaks "declaration in, repo paths out". You could only
//     ever point it at ours.
//   - So we shipped the service AND treated it as foreign: configured by URL,
//     lifecycle unmanaged, and with no authentication, because it was "local".
//   - And it duplicated work already done in Go. Tier 0 already walks the
//     repository, already reads git, already ranks files.
//
// The split that actually holds: LANES OWNS THE INDEX, the chunking, and the
// similarity maths. The only thing it cannot do in-process is turn text into a
// vector: that needs a model, and a model needs a runtime we will not link.
//
// So the external contract shrinks to the one operation that is genuinely
// foreign, and that operation already has a universal API:
//
//	POST {base}/v1/embeddings   {"model": …, "input": [...]}
//	                         →  {"data": [{"embedding": [...]}, …]}
//
// Ollama, vLLM, text-embeddings-inference, LM Studio, llama.cpp's server, and
// every hosted provider speak exactly this. Dibs ships no service, invents no
// protocol, and the port belongs to whatever the operator already runs.
type Embed struct {
	base    string
	model   string
	key     string
	timeout time.Duration
	client  *http.Client
	affix   affixes

	mu    sync.RWMutex
	paths []string    // chunk i belongs to file paths[i]
	vecs  [][]float32 // unit-normalised
	dims  int         // the index's vector width; a query must match it
	// unreadable are tracked files the index could not cover. Kept because an
	// index quietly smaller than the repository fails to match work touching
	// them, and reports READY while doing it.
	unreadable []string
	built      bool
	buildAt    time.Time
	// digest fingerprints the index contents, so Version distinguishes two
	// builds that share a second and a shape.
	digest string
}

// maxChunkChars bounds one embedding input. Large enough that a short file is
// one chunk, small enough to stay inside a typical model's context.
const maxChunkChars = 2000

// embedBatch is how many chunks travel per request. Batching matters: a
// thousand one-chunk requests spends its life in HTTP round-trips, and every
// server in the ecosystem accepts an array.
const embedBatch = 64

// NewEmbed builds a scorer over an OpenAI-compatible embeddings endpoint.
//
// base is the API root, with or without the /v1 suffix. "http://localhost:11434"
// and "http://localhost:11434/v1" both work, because getting that wrong is the
// single most likely configuration mistake and it costs nothing to accept both.
func NewEmbed(base, model, key string, timeout time.Duration) *Embed {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	base = strings.TrimSuffix(base, "/v1")
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Embed{
		base: base, model: model, key: key, timeout: timeout,
		affix: affixesFor(model),
		// No Timeout on the client itself: it applies to the WHOLE request and
		// cannot see how much work was asked for. A one-item probe and a
		// 64-chunk batch are not the same request, and one flat value either
		// strangles the batch or lets a hung probe sit. The deadline is set per
		// call instead, scaled by batch size: see encode.
		client: &http.Client{},
	}
}

// ID reports the scorer identity recorded in provenance. It names the MODEL,
// not the endpoint: which host served it is an operational detail, while which
// model produced a score is what makes that score comparable later.
func (e *Embed) ID() string {
	if e.model == "" {
		return "embed"
	}
	return "embed:" + e.model
}

// Version is the index build, not the code version: rebuilding the index over
// changed files changes what the same declaration retrieves, and a recorded
// score should say which index answered.
func (e *Embed) Version() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.buildAt.IsZero() {
		return "0"
	}
	// CONTENT, and nothing else. A version is an identity, and an identity has
	// to satisfy both halves: different indexes get different versions, and the
	// SAME index gets the same version.
	//
	// Two earlier attempts each satisfied one half. A timestamp alone collided,
	// a second-resolution clock cannot tell two rebuilds of a small repository
	// apart. Adding chunk count and width still collided, because two builds a
	// moment apart over edited files share both. Adding a content digest fixed
	// that half and broke the other: with buildAt still in the string, rebuilding
	// byte-identical content in a later second produced a different version, so
	// provenance that was genuinely still accurate read as stale.
	//
	// The build time is real and worth having, but it is metadata about WHEN the
	// index was made, not part of what it IS. BuildAt reports it separately.
	return fmt.Sprintf("idx-%dx%d-%s", len(e.vecs), e.dims, e.digest)
}

// BuildAt reports when the index was built. Metadata, deliberately kept out of
// Version: when an index was made says nothing about what it would answer.
func (e *Embed) BuildAt() time.Time {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.buildAt
}

// Chunks reports how many chunks are indexed, for the boot log.
func (e *Embed) Chunks() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.vecs)
}

// embedReq/embedResp are the OpenAI embeddings shapes, and deliberately no more
// of them than Dibs uses. Fields nobody reads are fields that rot.
type embedReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// deadlineFor scales the timeout with how much was asked for.
//
// A 4B model encoding 64 chunks is not the same request as a one-word probe,
// and a flat timeout treats them identically. Measured: the two 4B models both
// failed on chunk 0 of 449 (the very first batch) with the client timeout
// exceeded, while the same models had succeeded at the same batch size on an
// otherwise idle machine. A production box under load would hit this and the
// operator would see "context deadline exceeded" with nothing actionable in it.
func (e *Embed) deadlineFor(n int) time.Duration {
	base := e.timeout
	if base <= 0 {
		// A zero here used to be harmless: net/http reads it as "no timeout". As
		// a context deadline it means "already expired", so every request fails
		// instantly with a message about slow models, which is the opposite of
		// the truth. Anything constructing Embed directly rather than through
		// NewEmbed lands here.
		base = defaultEncodeTimeout
	}
	if n <= 1 {
		return base
	}
	// The base covers connection and model warm-up; the per-item allowance
	// covers the actual encoding. Generous on purpose: being slow is a
	// performance problem, and timing out mid-index is a correctness one,
	// a half-built index silently matches nothing.
	d := base + time.Duration(n)*perItemAllowance
	if d > maxEncodeDeadline {
		d = maxEncodeDeadline
	}
	return d
}

const (
	defaultEncodeTimeout = 30 * time.Second
	perItemAllowance     = 3 * time.Second
	maxEncodeDeadline    = 20 * time.Minute
)

// Retrieval embedding models are ASYMMETRIC, and we were using them symmetrically.
//
// A query ("reworking token validation") and a document (a chunk of Go) are not
// the same kind of text, and every serious retrieval model is trained with a
// marker that says which one it is being given. Qwen3-Embedding's card
// specifies `Instruct: {task}\nQuery: {q}` for queries; nomic was trained with
// literal `search_query:` / `search_document:` prefixes; BGE families use an
// instruction on the query side only.
//
// We sent raw text for both. The model therefore embedded a task description
// and a code chunk into the same undifferentiated space, which still RANKS
// tolerably, because similar topics are still similar, but it collapses the
// margin between related and unrelated work. That is exactly the symptom
// measured on this repository: recall improved with a bigger model while
// separation got WORSE (29% at 0.6b, 22% at 4b). More parameters cannot
// recover a distinction the input never encoded.
//
// Detected from the model name, because an operator should not have to know
// each family's convention. Overridable with SetAffixes, because a model family
// Dibs has never heard of still has a convention and its operator knows it,
// and without an override that operator silently gets half the separation with
// nothing on screen to explain it.
// affixes are the retrieval markers for one model family.
//
// known is carried EXPLICITLY rather than inferred from the strings being
// non-empty, because "recognised, and the convention is no marker" is a real
// answer (bge-m3 documents exactly that) and it is not the same answer as
// "never heard of this model". Inferring it from emptiness collapsed the two
// and made a correctly-configured model warn.
type affixes struct {
	query, doc string
	known      bool
}

func affixesFor(model string) affixes {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "qwen3-embedding"), strings.Contains(m, "qwen3embedding"):
		// The card's format. The task line matters: it is what tells the model
		// what KIND of retrieval this is.
		return affixes{known: true, query: "Instruct: Given a description of a software task, " +
			"retrieve the source files that task would change\nQuery: "}
	case strings.Contains(m, "nomic-embed"):
		return affixes{known: true, query: "search_query: ", doc: "search_document: "}
	case strings.Contains(m, "bge-m3"), strings.Contains(m, "bge_m3"):
		// bge-m3 is RECOGNISED and takes no marker. The card is explicit that it
		// "no longer requires adding instruction to the queries", so the family
		// prefix below would be an untrained string on its input.
		// https://huggingface.co/BAAI/bge-m3
		//
		// Recognised-with-no-marker and unrecognised are different answers, and
		// the difference is whether Dibs warns: there is nothing to warn about
		// here, because the absence IS the convention.
		return affixes{known: true}
	case strings.Contains(m, "bge-code"), strings.Contains(m, "bge-multilingual-gemma"),
		strings.Contains(m, "bge-en-icl"):
		// The instruct generation of BGE. All three cards specify the same
		// shape ("<instruct>{instruction}\n<query>{query}") with documents
		// left bare.
		// https://huggingface.co/BAAI/bge-code-v1
		// https://huggingface.co/BAAI/bge-multilingual-gemma2
		// https://huggingface.co/BAAI/bge-en-icl
		//
		// bge-en-icl was briefly excluded here on the grounds that its
		// convention is few-shot and Dibs has no examples to supply. That was
		// wrong: its card gives the ZERO-SHOT form explicitly, and it is this
		// one. Few-shot appends worked <response> examples on top: an
		// enhancement to a documented format, not a precondition for it.
		return affixes{known: true, query: "<instruct>Given a description of a software task, " +
			"retrieve the source files that task would change\n<query>"}
	case strings.Contains(m, "bge") && strings.Contains(m, "zh"):
		// The Chinese v1.5 models are trained with their own instruction. The
		// English string is not a translation of it, it is a different input.
		// https://huggingface.co/BAAI/bge-large-zh-v1.5
		return affixes{known: true, query: "为这个句子生成表示以用于检索相关文章："}
	case strings.Contains(m, "bge-small-en"), strings.Contains(m, "bge-base-en"),
		strings.Contains(m, "bge-large-en"):
		// The v1/v1.5 English generation, and ONLY that generation.
		//
		// This case used to be a bare Contains(m, "bge"), which is how a family
		// match goes wrong: BGE now spans four incompatible conventions, and the
		// broad branch handed this legacy string to bge-m3 (needs none),
		// bge-code-v1 and bge-multilingual-gemma2 (instruct tags) and bge-en-icl
		// (few-shot). Naming the models is more typing and cannot silently
		// capture the next release.
		//
		// The instruction is a TRAINED STRING, not a description of intent: a
		// paraphrase is a different input and the model has no reason to treat
		// it as the marker. This is the card's wording verbatim.
		// https://huggingface.co/BAAI/bge-large-en-v1.5
		return affixes{known: true, query: "Represent this sentence for searching relevant passages: "}
	case strings.Contains(m, "arctic-embed") && (strings.Contains(m, "v2") || strings.Contains(m, "embed2")):
		// v2 only. "use the query prefix below (just on the query)": documents
		// are UNPREFIXED.
		// https://huggingface.co/Snowflake/snowflake-arctic-embed-m-v2.0
		return affixes{known: true, query: "query: "}
	case strings.Contains(m, "arctic-embed"):
		// v1 is a DIFFERENT prefix: the legacy BGE-style instruction, which is
		// what v1 was trained with. Same family, same vendor, one version apart,
		// and the strings share nothing.
		// https://huggingface.co/Snowflake/snowflake-arctic-embed-l
		return affixes{known: true, query: "Represent this sentence for searching relevant passages: "}
	case strings.Contains(m, "e5-mistral"), strings.Contains(m, "e5") && strings.Contains(m, "instruct"):
		// The instruct member of the e5 family: "Instruct: {task}\nQuery: {query}",
		// and explicitly "No need to add instruction for retrieval documents",
		// so it marks ONE side, unlike the rest of e5, which marks both.
		// https://huggingface.co/intfloat/e5-mistral-7b-instruct
		return affixes{known: true, query: "Instruct: Given a description of a software task, " +
			"retrieve the source files that task would change\nQuery: "}
	case strings.Contains(m, "e5"):
		// The symmetric e5 models are the one family that marks both sides:
		// "Each input text should start with query: or passage:".
		// https://huggingface.co/intfloat/e5-large-v2
		return affixes{known: true, query: "query: ", doc: "passage: "}
	}
	// Deliberately NOT listed: gte. Its card documents no prefix for
	// gte-large-en-v1.5, and the only instruction-tuned member of that family is
	// a different model (gte-Qwen*-instruct). Guessing one for the whole family
	// installed a marker the model was never trained on AND suppressed the
	// unknown-model warning: worse than doing nothing, because it was silent.
	//
	// The rule this encodes: only claim a convention that a model card states.
	// An unrecognised model warns, which is recoverable; a wrongly-marked one
	// looks configured and quietly scores worse.
	return affixes{}
}

// SetAffixes overrides the detected query/document markers.
//
// Empty strings mean "no marker", which is a legitimate choice: a symmetric
// model given retrieval markers is worse off than one given none. Passing both
// empty therefore DISABLES markers rather than restoring detection.
func (e *Embed) SetAffixes(query, doc string) {
	e.affix = affixes{query: query, doc: doc}
}

// Affixes reports the markers in use, so `dibs doctor` and `calibrate` can say
// whether a model is being addressed the way it was trained.
func (e *Embed) Affixes() (query, doc string) { return e.affix.query, e.affix.doc }

// Recognised reports whether the model name matched a known convention. An
// unrecognised model is not an error (it may genuinely be symmetric) but it
// is worth telling somebody about, because the most likely explanation is a
// family we have not listed and the cost is roughly half the separation.
func (e *Embed) Recognised() bool { return affixesFor(e.model).known }

// encodeAs applies the right side's affix before encoding.
func (e *Embed) encodeAs(ctx context.Context, texts []string, isQuery bool) ([][]float32, error) {
	a := e.affix
	pre := a.doc
	if isQuery {
		pre = a.query
	}
	if pre == "" {
		return e.encode(ctx, texts)
	}
	tagged := make([]string, len(texts))
	for i, t := range texts {
		tagged[i] = pre + t
	}
	return e.encode(ctx, tagged)
}

// checkVectors rejects a reply that cannot be compared with the index.
//
// dot() walked min(len(a), len(b)), so a query embedded at a different width
// than the index was silently scored over a PREFIX of both: plausible numbers
// from incompatible vector spaces, and auto-joins made from them. That happens
// for real: a model alias repointed, an endpoint swapped, a service upgraded
// between Build and Predict. Nothing errored, the scores just quietly stopped
// meaning anything.
//
// An empty or non-finite vector is rejected for the same reason: it scores 0
// against everything, which is indistinguishable from "no opinion" and would
// make a broken service look like a well-behaved one that found nothing.
func checkVectors(vs [][]float32, want int) error {
	for i, v := range vs {
		if len(v) == 0 {
			return fmt.Errorf("embedding %d is empty: the service returned no vector, "+
				"which scores zero against everything and is indistinguishable from a "+
				"scorer with no opinion", i)
		}
		if want > 0 && len(v) != want {
			return fmt.Errorf("embedding %d has %d dimensions but the index was built with "+
				"%d: the model or endpoint changed since indexing. Comparing them comes to "+
				"a number, and that number means nothing. Rebuild the index, or point "+
				"-match-embed-model back at what built it", i, len(v), want)
		}
		for j, x := range v {
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				return fmt.Errorf("embedding %d component %d is not a finite number (%v)", i, j, x)
			}
		}
	}
	return nil
}

// encode turns texts into unit vectors via the remote service.
func (e *Embed) encode(ctx context.Context, texts []string) ([][]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, e.deadlineFor(len(texts)))
	defer cancel()
	body, err := json.Marshal(embedReq{Model: e.model, Input: texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	res, err := e.client.Do(req)
	if err != nil {
		// Name the fix. "context deadline exceeded" tells an operator nothing
		// about which knob moves it, and this is the failure a bigger model or a
		// busier machine hits first.
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf(
				"embeddings service did not answer within %s for a batch of %d. "+
					"a larger model on a busy machine needs longer: raise -match-deadline "+
					"(daemon) or use a smaller model: %w",
				e.deadlineFor(len(texts)), len(texts), err,
			)
		}
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return nil, fmt.Errorf("embeddings endpoint returned %s: %s", res.Status, msg)
	}
	return decodeEmbeddings(raw, len(texts))
}

// decodeEmbeddings maps a response back onto the inputs that produced it.
//
// Count and order are load-bearing: Dibs matches vector i to chunk i, so a
// service returning fewer vectors (or reordering them) would shift every
// later vector onto the wrong file and corrupt the whole index silently. Both
// are checked rather than trusted.
func decodeEmbeddings(raw []byte, want int) ([][]float32, error) {
	var out embedResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("embeddings response is not JSON: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("embeddings endpoint: %s", out.Error.Message)
	}
	if len(out.Data) != want {
		return nil, fmt.Errorf("embeddings endpoint returned %d vectors for %d inputs",
			len(out.Data), want)
	}
	vecs := make([][]float32, want)
	for i, d := range out.Data {
		at := d.Index // authoritative when present; some servers reorder
		if at < 0 || at >= want {
			at = i
		}
		vecs[at] = normalise(d.Embedding)
	}
	for i, v := range vecs {
		if v == nil {
			return nil, fmt.Errorf("embeddings endpoint left input %d unanswered", i)
		}
	}
	return vecs, nil
}

// normalise scales a vector to unit length so cosine similarity is a dot
// product. Doing it once at index time turns every later comparison into a
// multiply-add loop with no square roots in it.
func normalise(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

var skipBinary = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|ico|pdf|zip|gz|woff2?|ttf|mp4|wasm|bin|lock|sum)$`)

// Build indexes the repository. Slow by nature (it embeds every chunk) so
// callers run it off the request path.
//
// The chunking deliberately matches what produced the published measurements:
// the PATH is prepended to every chunk, because it is the single most
// informative token a code chunk carries, and a chunk from the middle of a file
// otherwise gives the model no clue which file it came from.
func (e *Embed) Build(ctx context.Context, repo string) error {
	// #nosec G204,G702 -- no shell: exec.Command passes argv directly, and the
	// repository path comes from operator config, never from an agent.
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "ls-files").Output()
	if err != nil {
		return fmt.Errorf("listing files: %w", err)
	}
	chunks, owners, unreadable := chunkRepo(repo, string(out), nil, nil)
	var vecs [][]float32

	// An index that silently covers less than the repository is an index that
	// silently fails to match. Recorded so Build can report it and Evidence can
	// carry it, and recorded UNCONDITIONALLY, because the assignment used to be
	// guarded on there being something to record, which meant a build that fixed
	// the problem left the previous run's warning standing. A stale warning is a
	// worse failure than a missing one: it names files that are fine, so the
	// operator who checks finds nothing wrong and stops believing the warning.
	e.mu.Lock()
	e.unreadable = append([]string(nil), unreadable...)
	e.mu.Unlock()

	// Nothing readable is not a thin index, it is no index. Left to continue,
	// Build would mark the scorer built with zero chunks: every Predict returns
	// nothing, every declaration matches nothing, and the board reports a
	// working embedding scorer. Failing here is what makes the daemon fall back
	// to the built-in scorer, which is the honest outcome.
	if len(chunks) == 0 {
		if len(unreadable) > 0 {
			return fmt.Errorf("no readable files: %d tracked file(s) could not be read (e.g. %s)",
				len(unreadable), unreadable[0])
		}
		return fmt.Errorf("no files to index in %s (is it a git repository with tracked files?)", repo)
	}
	dims := 0
	for i := 0; i < len(chunks); i += embedBatch {
		end := min(i+embedBatch, len(chunks))
		batch, err := e.encodeAs(ctx, chunks[i:end], false)
		if err != nil {
			return fmt.Errorf("embedding chunk %d/%d: %w", i, len(chunks), err)
		}
		// Every batch must be the same width as the first, or the index itself
		// is a mixture of incompatible spaces before a query ever arrives.
		if len(vecs) == 0 && len(batch) > 0 {
			dims = len(batch[0])
		}
		if err := checkVectors(batch, dims); err != nil {
			return fmt.Errorf("embedding chunk %d/%d: %w", i, len(chunks), err)
		}
		vecs = append(vecs, batch...)
	}

	digest := indexDigest(owners, vecs)

	e.mu.Lock()
	e.paths, e.vecs, e.built, e.buildAt, e.dims = owners, vecs, true, time.Now(), dims
	e.digest = digest
	e.mu.Unlock()
	return nil
}

// indexDigest fingerprints what the index actually CONTAINS: which file owns
// each chunk, and the vector that chunk embedded to. Two builds agree on this
// only if they would answer a query identically, which is exactly the identity
// a provenance field needs.
//
// Vector bytes rather than file contents, deliberately: the same source
// re-embedded by a different model, or by the same model with different
// retrieval markers, is a DIFFERENT index for retrieval purposes, and hashing
// the source would call those two the same.
func indexDigest(owners []string, vecs [][]float32) string {
	h := sha256.New()
	for i, o := range owners {
		_, _ = h.Write([]byte(o))
		_, _ = h.Write([]byte{0})
		if i < len(vecs) {
			for _, f := range vecs[i] {
				var b [4]byte
				binary.LittleEndian.PutUint32(b[:], math.Float32bits(f))
				_, _ = h.Write(b[:])
			}
		}
		_, _ = h.Write([]byte{0})
	}
	// The full 256-bit digest, not a truncation. It was 48 bits, which reads as
	// plenty for an accident and is not: a chosen collision is birthday work of
	// about 2^24, trivial for anyone who can write to the repository being
	// indexed. Provenance is exactly the field where "unlikely by accident" is
	// the wrong bar.
	return hex.EncodeToString(h.Sum(nil))
}

// chunkRepo splits every readable tracked file into embeddable chunks, and
// reports the ones it could not read.
//
// Skipping an unreadable file is right: a broken symlink or a file removed
// between `git ls-files` and the read is not an error worth failing a whole
// index over. Skipping it SILENTLY is not: the scorer then reports READY over
// an index that can never match work touching those files.
func chunkRepo(repo, out string, chunks, owners []string) (c, o, unreadable []string) {
	c, o = chunks, owners
	for _, f := range strings.Split(out, "\n") {
		f = strings.TrimSpace(f)
		if f == "" || skipBinary.MatchString(f) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(repo, f)) // #nosec G304 -- paths come from git ls-files
		if err != nil {
			unreadable = append(unreadable, f)
			continue
		}
		s := string(body)
		if s == "" {
			c, o = append(c, f), append(o, f)
			continue
		}
		for i := 0; i < len(s); i += maxChunkChars {
			end := min(i+maxChunkChars, len(s))
			c = append(c, f+"\n"+s[i:end])
			o = append(o, f)
		}
	}
	return c, o, unreadable
}

// evidence describes the index a prediction came from, including what it could
// NOT cover. An index quietly smaller than the repository fails to match work
// touching the missing files, and would otherwise report itself as complete.
func (e *Embed) evidence(chunks, files int) []string {
	out := []string{fmt.Sprintf("%d chunks over %d files", chunks, files)}
	e.mu.RLock()
	missing := len(e.unreadable)
	first := ""
	if missing > 0 {
		first = e.unreadable[0]
	}
	e.mu.RUnlock()
	if missing > 0 {
		out = append(out, fmt.Sprintf(
			"%d tracked file(s) could not be read and are NOT in this index (e.g. %s). "+
				"work touching them cannot match", missing, first,
		))
	}
	return out
}

// Unreadable lists tracked files the index could not cover.
func (e *Embed) Unreadable() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]string(nil), e.unreadable...)
}

// Predict embeds the declaration and ranks files by their best-matching chunk.
//
// A file scores as its BEST chunk, not its mean. A 2,000-line file with one
// highly relevant function is relevant; averaging would bury it under its own
// boilerplate, and averaging is what makes large files systematically invisible.
func (e *Embed) Predict(ctx context.Context, declaration string, limit int) (Prediction, error) {
	if limit <= 0 {
		limit = 40
	}
	e.mu.RLock()
	built, paths, vecs, dims := e.built, e.paths, e.vecs, e.dims
	e.mu.RUnlock()
	if !built {
		return Prediction{}, fmt.Errorf("index not built")
	}
	qs, err := e.encodeAs(ctx, []string{declaration}, true)
	if err != nil {
		return Prediction{}, err
	}
	// The query must be comparable with the index. Silently scoring a prefix of
	// two different vector spaces produces confident numbers about nothing.
	if err := checkVectors(qs, dims); err != nil {
		return Prediction{}, fmt.Errorf("scoring a declaration: %w", err)
	}
	q := qs[0]

	// Cosine similarity has a HIGH FLOOR. Two unrelated English texts embed
	// around 0.3–0.7 with a modern model; nothing lands near zero. So the raw
	// value is not the signal. "how much better than typical" is.
	//
	// This matters because topN renormalises against the maximum, mapping
	// [0, max] onto [0, 1]. For tier 0 that is right: a file sharing no terms
	// genuinely scores 0. For embeddings it is a disaster: chunks at 0.70 and
	// 0.83 become 0.84 and 1.00, and the discrimination is gone. Measured on a
	// three-file fixture: "writing release notes for the changelog" scored
	// 0.729 against an authentication agent, comfortably above any sane join
	// threshold. A false positive that confident would put every agent in one
	// agent.
	//
	// So rescale against the query's OWN distribution before aggregating:
	// typical becomes 0, best becomes 1. A query that matches everything
	// equally now correctly matches nothing in particular.
	sims := make([]float64, 0, len(vecs))
	for _, v := range vecs {
		sims = append(sims, dot(q, v))
	}
	baseline, top := distributionFloor(sims)
	span := top - baseline
	if span <= 0 {
		// Every chunk is equally similar, which is the honest definition of no
		// information. Returning nothing beats returning everything at 1.0.
		return Prediction{ScorerID: e.ID(), Version: e.Version()}, nil
	}

	best := make(map[string]float64, len(paths)/4+1)
	for i, s := range sims {
		w := (s - baseline) / span
		if w <= 0 {
			continue // at or below typical: no evidence, not weak evidence
		}
		if w > best[paths[i]] {
			best[paths[i]] = w
		}
	}
	files := make([]File, 0, len(best))
	for p, w := range best {
		files = append(files, File{Path: p, Weight: w})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Weight != files[j].Weight {
			return files[i].Weight > files[j].Weight
		}
		return files[i].Path < files[j].Path
	})
	files = topN(files, limit)
	return Prediction{
		Files: files, ScorerID: e.ID(), Version: e.Version(),
		Evidence: e.evidence(len(vecs), countPaths(paths)),
	}, nil
}

// baselinePct is the percentile treated as "unrelated".
//
// The median was tried first and is too aggressive: it assumes at most half the
// corpus relates to any query, which holds for a real repository and fails on a
// small one, where a query genuinely touching two files in three has its true
// matches zeroed. The 25th percentile assumes at most three quarters relate,
// safe on a large index, survivable on a tiny one. Tuned by measurement on both
// (see the fleet scenario's semantic-separation probes), not by taste.
var baselinePct = 25

// distributionFloor returns what "typical" and "best" look like for one query.
//
// The floor is the MEDIAN similarity, not the minimum: a minimum is one
// outlier and moves with the corpus, while the median answers "what does this
// query score against a chunk it has nothing to do with", which is exactly
// the baseline that must map to zero.
func distributionFloor(sims []float64) (baseline, top float64) {
	if len(sims) == 0 {
		return 0, 0
	}
	sorted := make([]float64, len(sims))
	copy(sorted, sims)
	sort.Float64s(sorted)
	return sorted[len(sorted)*baselinePct/100], sorted[len(sorted)-1]
}

func dot(a, b []float32) float64 {
	n := min(len(a), len(b))
	var s float32
	for i := 0; i < n; i++ {
		s += a[i] * b[i]
	}
	return float64(s)
}

func countPaths(paths []string) int {
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		seen[p] = true
	}
	return len(seen)
}

// Probe checks the endpoint answers before the daemon commits to it, so a typo
// in a URL is a startup warning rather than a mystery at first use.
func (e *Embed) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	_, err := e.encode(ctx, []string{"probe"})
	return err
}
