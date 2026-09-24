package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// setupSeat registers one agent working in dir and returns the engine.
func setupSeat(t *testing.T, dir string) (*Engine, context.CancelFunc) {
	t.Helper()
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "seat", AgentKind: core.KindPersistent, Nonce: "n",
		SessionID: "sess-1", Agent: &core.AgentInfo{CWD: dir},
	}); err != nil {
		cancel()
		t.Fatal("setup:", err)
	}
	return e, cancel
}

// WHOSE GUESS IT IS.
//
// A hook carries a session id when its harness can interpolate one. Gemini
// CLI's hooks are plain commands with no template variables, so it has nothing
// to pass, and matching the single agent working in that directory is the only
// reason a Gemini agent can be woken at all. That fallback is a feature.
//
// It is also a guess, and an operator running several agents in one monorepo
// may want a new session to prove who it is instead. So it is a setting.
func TestTheUnidentifiedPolicyDecidesWhoAHooklessSessionIs(t *testing.T) {
	dir := "/repo"
	for _, c := range []struct {
		policy  string
		resolve bool
		why     string
	}{
		{"", true, "the default is directory, which is what a harness with no session id needs"},
		{"directory", true, "asked for the guess and did not get it"},
		{"strict", false, "asked for nobody and got somebody"},
		{"ask", false, "asked to be asked and the guess was taken anyway"},
		{"coordinator", false, "handed the decision to the coordinator and guessed as well"},
	} {
		t.Run(c.policy+"/"+c.why, func(t *testing.T) {
			e, cancel := setupSeat(t, dir)
			defer cancel()
			e.SetUnidentifiedPolicy(c.policy)
			// No session id: the case the policy governs.
			got := e.resolveHook("", dir, "")
			if (got != nil) != c.resolve {
				t.Errorf("policy %q resolved=%v, want %v: %s", c.policy, got != nil, c.resolve, c.why)
			}
		})
	}
}

// AND THE POLICY DOES NOT REACH THE RULE ABOVE IT.
//
// A session id that was SUPPLIED and matched nothing is positive evidence this
// is a different session, never a hint to go looking for a neighbour. That
// rule predates the setting and is not subject to it: without it, an
// unregistered session in a shared repo was attributed to whichever agent was
// registered there, and was handed that agent's private mail.
func TestASuppliedSessionIdIsStillEvidenceWhateverThePolicy(t *testing.T) {
	dir := "/repo"
	for _, policy := range []string{"", "directory", "strict", "ask", "coordinator"} {
		e, cancel := setupSeat(t, dir)
		e.SetUnidentifiedPolicy(policy)
		if got := e.resolveHook("a-session-nobody-registered", dir, ""); got != nil {
			t.Errorf("policy %q: a stranger naming its own session id in %s resolved "+
				"to %s, which is how one agent reads another's mail", policy, dir, got.ID)
		}
		// And the seat's OWN session still resolves, or the guard above has
		// been turned into a wall.
		if got := e.resolveHook("sess-1", dir, ""); got == nil {
			t.Errorf("policy %q: the agent's own session id no longer resolves", policy)
		}
		cancel()
	}
}

// The coordinator is told, once per directory, and only when it exists.
func TestTheCoordinatorPolicyLeavesANoticeRatherThanGuessing(t *testing.T) {
	dir := "/repo"
	e, cancel := setupSeat(t, dir)
	defer cancel()
	ctx := context.Background()
	r, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "boss", AgentKind: core.KindPersistent, Nonce: "nb",
		SessionID: "sess-boss", Agent: &core.AgentInfo{CWD: "/elsewhere"},
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	_ = r
	// The role directly, on the loop. Going through claim_coordinator would be
	// testing the claim path, which has its own tests and its own single-use
	// secret; what this test is about is what the POLICY does once a
	// coordinator exists.
	onLoop(t, ctx, e, func(st *core.State) { st.Agents["boss"].Role = core.RoleCoordinator })
	var isBoss bool
	onLoop(t, ctx, e, func(st *core.State) { isBoss = st.Agents["boss"].IsCoordinator() })
	if !isBoss {
		t.Fatal("setup: no coordinator, so the branch under test is not reached")
	}

	e.SetUnidentifiedPolicy("coordinator")
	if got := e.resolveHook("", dir, ""); got != nil {
		t.Fatalf("guessed anyway: %s", got.ID)
	}
	var texts []string
	for _, n := range e.takeNotices("boss") {
		texts = append(texts, n.Text)
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, dir) {
		t.Errorf("the coordinator was not told which directory asked:\n%s", joined)
	}
	if !strings.Contains(joined, "seat") {
		t.Error("the notice does not name who it would have been, so the " +
			"coordinator has to go and work it out")
	}

	// THROTTLED. A harness fires hooks continuously; an unidentified session
	// in a loop would otherwise notify once per turn boundary, which is the
	// shape of every alert nobody reads.
	//
	// COUNTED, NOT DRAINED. takeNotices copies and sorts; it does not consume,
	// which the first version of this assertion assumed and was wrong about.
	// The queue is cleared by the agent's own check_in, not by looking at it.
	before := len(e.takeNotices("boss"))
	e.resolveHook("", dir, "")
	if after := len(e.takeNotices("boss")); after != before {
		t.Errorf("the same directory raised another notice immediately (%d then %d)",
			before, after)
	}

	// And it comes back after the window, because an unidentified session that
	// is STILL unidentified half an hour later is news again.
	e.identity.mu.Lock()
	e.identity.asked[dir] = time.Now().Add(-askAgain - time.Minute)
	e.identity.mu.Unlock()
	e.resolveHook("", dir, "")
	if after := len(e.takeNotices("boss")); after == before {
		t.Error("the notice never comes back, so an operator who missed the " +
			"first one never hears again")
	}
}
