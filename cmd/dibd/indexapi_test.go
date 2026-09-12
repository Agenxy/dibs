package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/overlap"
)

// An agent may supply the index for the tree it is registered in, and no
// other; the daemon keeps its own reading of a tree it could read itself.
// Issue #19: the daemon needs read access to nothing outside its own
// directory, and agent-supplied data authorises nothing.
func TestAnAgentSuppliesTheIndexForItsOwnTreeOnly(t *testing.T) {
	eng, ctx := testEngine(t)

	root := filepath.Join(t.TempDir(), "checkout")
	elsewhere := t.TempDir()
	reg := func(name, cwd string) string {
		res, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name, Agent: &core.AgentInfo{CWD: cwd}})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		return tok
	}
	inside := reg("inside", filepath.Join(root, "internal", "core"))
	outside := reg("outside", elsewhere)

	f := defaultScorerFlags()
	mux := http.NewServeMux()
	registerIndexAPI(mux, eng, f)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	payload := overlap.Payload{
		Root:        root,
		Fingerprint: "hist-1",
		Files:       []string{"internal/core/apply.go", "internal/ledger/ledger.go", "cmd/dibd/main.go"},
		Commits: []overlap.Commit{
			{Subject: "ledger replay applies every op", Files: []string{"internal/core/apply.go", "internal/ledger/ledger.go"}},
			{Subject: "daemon boots the ledger", Files: []string{"cmd/dibd/main.go", "internal/ledger/ledger.go"}},
		},
	}
	post := func(token string, p overlap.Payload) (int, map[string]any) {
		body, _ := json.Marshal(struct {
			Token string `json:"token"`
			overlap.Payload
		}{token, p})
		resp, err := http.Post(srv.URL+"/api/index", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// An agent elsewhere cannot supply this tree's index.
	if code, out := post(outside, payload); code != http.StatusForbidden {
		t.Fatalf("an agent outside the tree shipped its index: %d %v; one agent could "+
			"poison another project's matching", code, out)
	}
	// A stranger's token cannot either.
	if code, _ := post("not-a-token", payload); code != http.StatusUnauthorized {
		t.Fatalf("a token that names no agent was accepted: %d", code)
	}
	// The agent inside it can, and matching works there afterwards.
	code, out := post(inside, payload)
	if code != http.StatusOK || out["accepted"] != true {
		t.Fatalf("the agent in the tree was refused: %d %v", code, out)
	}
	if got := eng.IndexedRepos(); len(got) != 1 || got[0] != root {
		t.Fatalf("indexed repos = %v, want the shipped root", got)
	}
	if by := eng.IndexSuppliedBy(root); by != "inside" {
		t.Errorf("index provenance = %q, want the agent that shipped it", by)
	}
	if sup := eng.MatchStatus().Supplied; sup[root] != "inside" {
		t.Errorf("match status supplied = %v, want the root credited to the agent, for doctor", sup)
	}
	pred, _, _ := eng.Predict(ctx, "ledger replay")
	if len(pred) == 0 {
		t.Error("the shipped index predicts nothing for a sentence its own history describes")
	}

	// A second shipment for the same tree replaces the first (a newer
	// history), because the daemon still cannot read it.
	payload.Fingerprint = "hist-2"
	if code, out := post(inside, payload); code != http.StatusOK || out["accepted"] != true {
		t.Errorf("a refreshed shipment was refused: %d %v", code, out)
	}

	// But a tree the daemon indexed ITSELF keeps the daemon's reading.
	own := filepath.Join(t.TempDir(), "readable")
	owner := reg("owner", own)
	f.discoverMu.Lock()
	f.indexed[own] = true // as bringUp marks a tree it mined
	f.discoverMu.Unlock()
	ownPayload := payload
	ownPayload.Root = own
	if code, out := post(owner, ownPayload); code != http.StatusOK || out["accepted"] != false {
		t.Errorf("a shipment for a tree the daemon read itself was used: %d %v; the daemon's "+
			"own reading of which project a tree is must win", code, out)
	}

	// And a payload that could not have come from a checkout is refused
	// before any of that.
	bad := payload
	bad.Files = []string{"/etc/passwd"}
	if code, _ := post(inside, bad); code != http.StatusBadRequest {
		t.Errorf("an absolute path in the file list was accepted: %d", code)
	}
}
