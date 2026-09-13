package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/overlap"
)

// registerIndexAPI accepts an index an agent mined from its own checkout, for
// a tree this daemon cannot read (issue #19).
//
// POST /api/index, on the coordination tier: the local secret gets a caller
// this far, and the agent token in the body decides whose index it is. The
// rule that keeps agent-supplied data from authorising anything is one
// check: the token's agent must be registered from INSIDE the root the
// payload names. An agent can ship the index for the tree it is in and no
// other, so one agent cannot poison another project's matching, and the
// daemon keeps its own reading of any tree it can read: a payload for a tree
// the daemon indexed itself is acknowledged and not used.
func registerIndexAPI(mux *http.ServeMux, eng *engine.Engine, f *scorerFlags) {
	mux.HandleFunc("POST /api/index", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		var body struct {
			Token string `json:"token"`
			overlap.Payload
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, overlap.MaxPayloadBytes)).Decode(&body); err != nil {
			refuse(w, http.StatusBadRequest, "the index did not parse, or exceeds "+
				"the size the daemon will hold for one: "+err.Error())
			return
		}
		if err := body.Validate(); err != nil {
			refuse(w, http.StatusBadRequest, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		agent, cwd, repoRoot, err := eng.AgentLocation(ctx, body.Token)
		if err != nil || agent == "" {
			refuse(w, http.StatusUnauthorized, "the token names no agent; register first, "+
				"and ship the index with the token register returned")
			return
		}
		root := filepath.Clean(body.Root)
		if !underDir(cwd, root) {
			refuse(w, http.StatusForbidden, "agent "+agent+" is registered from "+cwd+
				", which is not inside "+root+": an agent may supply the index for the "+
				"tree it is in and no other")
			return
		}
		// The daemon's own knowledge outranks the payload. An agent whose
		// repository root the daemon resolved may ship only THAT root: a parent
		// directory claimed as a root would otherwise shape suggestions for
		// every tree beneath it.
		if repoRoot != "" && filepath.Clean(repoRoot) != root {
			refuse(w, http.StatusForbidden, "agent "+agent+"'s repository is "+repoRoot+
				", not "+root+": an agent may supply the index for its own repository and no other")
			return
		}
		// And only for a tree the daemon has itself found unreadable. Anything
		// it can read, it reads; a shipment for a tree it has not tried is not
		// a gap but a race, and the bridge only ships on the verdict.
		if !eng.TreeIsUnreadable(root) {
			refuse(w, http.StatusConflict, "the daemon has not found "+root+" unreadable; "+
				"it indexes what it can read itself, and the bridge ships only on that verdict")
			return
		}
		outcome := f.installSupplied(ctx, eng, root, agent, &body.Payload)
		_ = json.NewEncoder(w).Encode(outcome)
	})
}

func refuse(w http.ResponseWriter, status int, why string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"accepted": false, "error": why})
}

// underDir reports whether p is dir or inside it.
func underDir(p, dir string) bool {
	p, dir = filepath.Clean(p), filepath.Clean(dir)
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// installSupplied builds the index from a shipped payload and publishes it,
// unless the daemon already holds one it mined for that tree itself.
//
// The daemon's own index wins whenever it exists: it was read from the tree
// by the process that will be asked about it, and "keep believing the daemon
// about which project a tree is" is the guard the issue asked for. A shipped
// index fills the one gap, a tree the daemon cannot read, and is released
// like any other when the tree's agents are gone (evictIdleIndexes).
func (f *scorerFlags) installSupplied(
	ctx context.Context, eng *engine.Engine, root, agent string, p *overlap.Payload,
) map[string]any {
	// ONE build at a time, and none for an index already held. Building a
	// co-change index is real work, and a caller repeating a 16 MB payload
	// for a root already installed must not buy a rebuild per request.
	f.buildMu.Lock()
	defer f.buildMu.Unlock()
	if f.suppliedFingerprint(root) == p.Fingerprint && p.Fingerprint != "" {
		return map[string]any{"accepted": true, "root": root, "reason": "already installed at this fingerprint"}
	}
	f.discoverMu.Lock()
	if f.indexed[root] && f.supplied[root] == "" {
		f.discoverMu.Unlock()
		return map[string]any{"accepted": false, "reason": "the daemon indexed this tree itself; " +
			"its own reading is used"}
	}
	if f.indexed == nil {
		f.indexed = map[string]bool{}
	}
	if f.rootOf == nil {
		f.rootOf = map[string]string{}
	}
	if f.supplied == nil {
		f.supplied = map[string]string{}
	}
	if f.suppliedAt == nil {
		f.suppliedAt = map[string]string{}
	}
	if !f.indexed[root] && len(f.indexed) >= maxIndexedRepos {
		f.discoverMu.Unlock()
		return map[string]any{"accepted": false, "reason": "the daemon is at its repository ceiling"}
	}
	f.indexed[root] = true
	f.supplied[root] = agent
	f.rootOf[root] = root
	f.discoverMu.Unlock()

	cc := overlap.FromRecords(p.Commits, p.Fingerprint, overlap.CoChangeOptions{MaxCommits: f.history})
	lex := overlap.NewLexicalFromFiles(p.Files, cc)
	scorer := f.withSidecar(ctx, lex)
	notify := f.notifyFor(ctx, root, cc, scorer)
	// No Identity: the payload's project fields are the agent's word, and an
	// index with an identity becomes a PEER of every other index of that
	// project (issue #39). A shipped index scores its own tree and nothing
	// else; the engine refuses supplied indexes as peers regardless.
	eng.SetIndex(root, scorer, engine.MatchConfig{
		JoinThreshold: f.join, NotifyThreshold: notify, Deadline: f.deadline,
		DirectorRequired: f.director, AutoJoin: f.autoJoin, Repo: root,
	}, engine.IndexInfo{Fingerprint: p.Fingerprint, SuppliedBy: agent})
	f.discoverMu.Lock()
	f.suppliedAt[root] = p.Fingerprint
	f.discoverMu.Unlock()
	eng.NoteSuppliedIndex(root, agent)
	phase := engine.MatchReady
	if f.join == 0 {
		phase = engine.MatchNoThreshold
	}
	eng.SetMatchStatus(engine.MatchStatus{
		Phase: phase, Scorer: scorer.ID(), Repo: root, Files: lex.Files(), Commits: cc.Commits(),
	})
	slog.Info("work-overlap matching ready from an agent-supplied index",
		"repo", root, "agent", agent, "files", lex.Files(), "commits", cc.Commits(),
		"why", "the daemon cannot read this tree; the agent inside it shipped what the index is built from")
	return map[string]any{"accepted": true, "files": lex.Files(), "commits": cc.Commits(), "root": root}
}

func (f *scorerFlags) suppliedFingerprint(root string) string {
	f.discoverMu.Lock()
	defer f.discoverMu.Unlock()
	return f.suppliedAt[root]
}
