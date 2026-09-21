package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/overlap"
	"github.com/agenxy/dibs/internal/paths"
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
		agent, cwd, repoRoot, host, err := eng.AgentLocation(ctx, body.Token)
		if err != nil || agent == "" {
			refuse(w, http.StatusUnauthorized, "the token names no agent; register first, "+
				"and ship the index with the token register returned")
			return
		}
		// Portable, so a UNC root keeps the slashes the registration
		// recorded: filepath.Clean drops one on a unix hub, the upload
		// then installs the index under a root the agent is not in, and
		// scorerForLocation refuses it while the upload said accepted.
		// Round forty-four of the pre-release review.
		root := paths.Portable(body.Root)
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
		if repoRoot != "" && paths.Portable(repoRoot) != root {
			refuse(w, http.StatusForbidden, "agent "+agent+"'s repository is "+repoRoot+
				", not "+root+": an agent may supply the index for its own repository and no other")
			return
		}
		// And only for a tree the daemon has itself found unreadable. Anything
		// it can read, it reads; a shipment for a tree it has not tried is not
		// a gap but a race, and the bridge only ships on the verdict.
		// A tree on another machine is unreadable here by definition, and the
		// verdict is never recorded for it because the daemon does not go
		// looking (engine: no discovery for a remote agent's path).
		if host == "" && !eng.TreeIsUnreadable(root) {
			refuse(w, http.StatusConflict, "the daemon has not found "+root+" unreadable; "+
				"it indexes what it can read itself, and the bridge ships only on that verdict")
			return
		}
		outcome := f.installSupplied(ctx, eng, root, agent, host, &body.Payload)
		_ = json.NewEncoder(w).Encode(outcome)
	})
}

// rootIsTakenLocked says why a shipment for root cannot be held, or "" when
// it can. Caller holds discoverMu.
//
// Indexes are keyed by path, and one path holds one tree. A tree the daemon
// read itself outranks any copy. A member's tree at a path this daemon has a
// tree of its own at has nowhere to go: it is not scored by the daemon's
// index either (engine: indexSpeaksForHost), so the honest outcome is no
// matching for that agent, said plainly rather than the wrong index.
func (f *scorerFlags) rootIsTakenLocked(root, host string) string {
	if !f.indexed[root] {
		return ""
	}
	switch {
	case f.supplied[root] == "" && host != "":
		return "the daemon indexed a tree of its own at " + root + "; an index for another " +
			"machine's tree at the same path cannot be held beside it, so matching is " +
			"unavailable for that agent"
	case f.supplied[root] == "":
		return "the daemon indexed this tree itself; its own reading is used"
	case f.suppliedHost[root] != host:
		return "an index for " + root + " was already shipped from another machine; one path " +
			"holds one tree"
	}
	return ""
}

func refuse(w http.ResponseWriter, status int, why string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"accepted": false, "error": why})
}

// underDir reports whether p is dir or inside it.
//
// Both operands are made portable first, so the separator that joins them
// has to be the portable one. It was filepath.Separator, which is `\` on
// Windows: `C:/work/repo/pkg` was not beneath `C:/work/repo`, so a
// Windows hub answered 403 to an index shipped from a subdirectory and
// eviction did not count an agent in one as keeping the index alive.
// Invisible on a unix host, where the two separators are the same
// character, which is why this shipped. Round forty-five of the
// pre-release review.
func underDir(p, dir string) bool {
	p, dir = paths.Portable(p), paths.Portable(dir)
	return p == dir || strings.HasPrefix(p, dir+"/")
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
	ctx context.Context, eng *engine.Engine, root, agent, host string, p *overlap.Payload,
) map[string]any {
	// ONE build at a time, and none for an index already held. Building a
	// co-change index is real work, and a caller repeating a 16 MB payload
	// for a root already installed must not buy a rebuild per request.
	f.buildMu.Lock()
	defer f.buildMu.Unlock()
	// Ownership before the fingerprint shortcut. Two machines with the same
	// checkout at the same path ship the same fingerprint, and the second
	// was told "already installed" about an index held for the first, which
	// the scorer then refused it. Round four of the pre-release review.
	f.discoverMu.Lock()
	if why := f.rootIsTakenLocked(root, host); why != "" {
		f.discoverMu.Unlock()
		return map[string]any{"accepted": false, "reason": why}
	}
	if f.suppliedAt[root] == p.Fingerprint && p.Fingerprint != "" {
		f.discoverMu.Unlock()
		return map[string]any{"accepted": true, "root": root, "reason": "already installed at this fingerprint"}
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
	if f.suppliedHost == nil {
		f.suppliedHost = map[string]string{}
	}
	if !f.indexed[root] && len(f.indexed) >= maxIndexedRepos {
		// AT THE CEILING, LOOK FOR AN INDEX NOBODY IS IN ANY MORE, as
		// discovery does. This refused outright, and eviction ran only on
		// the local discovery path, which a remote tree never takes and an
		// unreadable local one fails before reaching: a fleet on supplied
		// indexes could not match its seventeenth repository until a
		// restart. Round ten of the pre-release review.
		f.discoverMu.Unlock()
		f.evictIdleIndexes(ctx, eng)
		f.discoverMu.Lock()
		if !f.indexed[root] && len(f.indexed) >= maxIndexedRepos {
			f.discoverMu.Unlock()
			return map[string]any{"accepted": false, "reason": "the daemon is at its repository " +
				"ceiling and every index still has an agent in it"}
		}
	}
	f.indexed[root] = true
	f.touchLocked(root)
	f.supplied[root] = agent
	f.suppliedHost[root] = host
	f.rootOf[root] = root
	gen := f.markClaimLocked(root)
	f.discoverMu.Unlock()

	cc := overlap.FromRecords(p.Commits, p.Fingerprint, overlap.CoChangeOptions{MaxCommits: f.history})
	lex := overlap.NewLexicalFromFiles(p.Files, cc)
	scorer := f.withSidecar(ctx, lex)
	// false: this root is the SHIPPER's path, and git here would either
	// read somebody else's checkout or retry an access that already
	// failed. See notifyFor.
	notify := f.notifyFor(ctx, root, cc, scorer, false)
	// No Identity: the payload's project fields are the agent's word, and an
	// index with an identity becomes a PEER of every other index of that
	// project (issue #39). A shipped index scores its own tree and nothing
	// else; the engine refuses supplied indexes as peers regardless, and it
	// never joins anyone on a supplied index's score (scorerForLocation),
	// whatever join threshold and auto-join policy are passed here.
	// STILL OURS? Building the scorer from the payload runs outside the
	// lock, and an eviction (or another shipment for this root) meanwhile
	// takes the slot: publishing anyway leaves an index the ceiling does
	// not know about and overwrites what replaced it. Round thirty-five of
	// the pre-release review found that; round thirty-seven found that
	// checking and then installing is still two operations, and eviction
	// fits between them. The claim, the index and the fingerprint move
	// together under one hold of discoverMu (publishUnderClaim).
	if !f.publishUnderClaim(root, gen, func() {
		eng.SetIndex(root, scorer, engine.MatchConfig{
			JoinThreshold: f.join, NotifyThreshold: notify, Deadline: f.deadline,
			DirectorRequired: f.director, AutoJoin: f.autoJoin, Repo: root,
		}, engine.IndexInfo{Fingerprint: p.Fingerprint, SuppliedBy: agent, SuppliedHost: host})
		f.suppliedAt[root] = p.Fingerprint
		// AND THE STATUS THAT SAYS SO, under the same hold. These ran
		// after the lock was released, so an eviction in between removed
		// the scorer and left the status saying this root has a supplied
		// index: the shipment answered accepted, matching was gone, and
		// the bridge then SKIPPED re-shipping because the status still
		// said its index was installed. Round fifty-one of the
		// pre-release review; round thirty-seven made the index and the
		// bookkeeping atomic and left the status outside.
		eng.NoteSuppliedIndexFrom(root, agent, host)
		// Suggest-only: a supplied index never joins anyone, see above.
		eng.SetMatchStatus(engine.MatchStatus{
			Phase: engine.MatchNoThreshold, Scorer: scorer.ID(), Repo: root,
			Files: lex.Files(), Commits: cc.Commits(),
		})
	}) {
		return map[string]any{"accepted": false, "reason": "this tree was claimed by another " +
			"index while yours was being installed; ship it again"}
	}
	slog.Info("work-overlap matching ready from an agent-supplied index",
		"repo", root, "agent", agent, "files", lex.Files(), "commits", cc.Commits(),
		"why", "the daemon cannot read this tree; the agent inside it shipped what the index is built from")
	return map[string]any{"accepted": true, "files": lex.Files(), "commits": cc.Commits(), "root": root}
}
