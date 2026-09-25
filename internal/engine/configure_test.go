package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func configureBoard(t *testing.T) (*Engine, context.Context, string, string) {
	t.Helper()
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	reg := func(name, nonce string) string {
		r, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, AgentKind: core.KindPersistent, Nonce: nonce,
		})
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok, _ := r["token"].(string)
		return tok
	}
	admin, worker := reg("boss", "nb"), reg("hand", "nh")
	onLoop(t, ctx, e, func(st *core.State) { st.Agents["boss"].Role = core.RoleAdmin })
	return e, ctx, admin, worker
}

// SETTINGS ARE THE ADMIN'S, AND READING THEM IS EVERYONE'S.
//
// The point of letting an agent change a setting is that the operator does
// not have to go and dig through a file on its behalf. The point of gating it
// on ADMIN rather than coordinator is that a coordinator runs the fleet with
// moves that are visible on the board and undoable from it, while a setting
// changes how the board behaves for every agent on it, including the ones
// that will never look.
func TestOnlyAnAdminChangesASetting(t *testing.T) {
	e, ctx, admin, worker := configureBoard(t)

	// Anybody with a token may read.
	r, err := e.Configure(ctx, worker, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if r["error"] != nil {
		t.Fatalf("a worker could not read the settings: %v", r["error"])
	}
	rows, _ := r["settings"].([]map[string]any)
	if len(rows) == 0 {
		t.Fatal("the listing is empty, so nothing can be discovered")
	}

	// A worker may not change one. The refusal arrives as a returned error,
	// which is how the engine loop surfaces every refusal.
	if _, err := e.Configure(ctx, worker, "hooks.mail_bodies", "false"); err == nil {
		t.Error("a non-admin changed a board setting")
	}
	if !e.mailBodies() {
		t.Error("and the change took effect anyway, which is worse than the " +
			"error being missing")
	}

	// An admin may.
	r, err = e.Configure(ctx, admin, "hooks.mail_bodies", "false")
	if err != nil {
		t.Fatalf("the admin was refused: %v", err)
	}
	if e.mailBodies() {
		t.Error("the call reported success and the setting did not change, which " +
			"is the failure mode this tool exists to avoid")
	}
	if r["by"] != "boss" {
		t.Errorf("the change is recorded as by %v, so an operator asking who did "+
			"this gets the wrong answer", r["by"])
	}
}

// A SETTING THAT NEEDS A RESTART IS REFUSED, NOT QUIETLY ACCEPTED.
//
// The boundary is "the engine can apply it now". Accepting `addr` and doing
// nothing until a restart is exactly the shape of bug this repository keeps
// paying for: a call that reports success and has no effect.
func TestASettingTheEngineCannotApplyIsRefused(t *testing.T) {
	e, ctx, admin, _ := configureBoard(t)
	_, err := e.Configure(ctx, admin, "addr", "0.0.0.0:9999")
	var cerr *core.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("a restart-only setting was accepted (err=%v)", err)
	}
	if cerr.Code != "E_NO_SETTING" {
		t.Errorf("code %q, want E_NO_SETTING", cerr.Code)
	}
	// The TOOL's name, which is `settings`: `configure` is the CLI wizard that
	// writes dibs.toml before a board runs, and an agent sent to that one is
	// being sent to a different program.
	if !strings.Contains(cerr.Hint, "settings") {
		t.Errorf("the hint does not name the call that lists what IS settable:\n%s", cerr.Hint)
	}
}

// A BAD VALUE DOES NOT REACH THE ENGINE.
func TestABadValueIsRefusedAndChangesNothing(t *testing.T) {
	e, ctx, admin, _ := configureBoard(t)
	e.SetUnidentifiedPolicy("directory")
	_, err := e.Configure(ctx, admin, "identity.unidentified", "whatever")
	var cerr *core.Error
	if !errors.As(err, &cerr) || cerr.Code != "E_BAD_SETTING" {
		t.Fatalf("a nonsense value was accepted (err=%v)", err)
	}
	if !strings.Contains(cerr.Hint, "directory") {
		t.Errorf("the hint does not say what the values are:\n%s", cerr.Hint)
	}
	e.identity.mu.RLock()
	p := e.identity.policy
	e.identity.mu.RUnlock()
	if p != takeDirectory {
		t.Error("the refused value was applied anyway")
	}
}

// APPLIED FIRST, PERSISTED SECOND, and a failure to persist is reported
// rather than swallowed: a setting that is live now and gone after a restart
// is a board that disagrees with itself, and the operator has to be told.
func TestAnUnwritableStoreIsReportedNotSwallowed(t *testing.T) {
	e, ctx, admin, _ := configureBoard(t)
	e.SetSettingStore(func(_, _, _ string) error { return errStore })
	r, err := e.Configure(ctx, admin, "wake.sockets", "false")
	if err != nil {
		t.Fatalf("the change was refused because it could not be saved: %v", err)
	}
	if e.socketWakesOn() {
		t.Error("the setting was not applied")
	}
	w, _ := r["warning"].(string)
	if !strings.Contains(w, "restart") {
		t.Errorf("nothing says the change will not survive a restart: %q", w)
	}
}

var errStore = &core.Error{Code: "E_TEST", Msg: "no disk"}

// The listing says where a value came from, because "the board behaves
// differently than I expect" has to be answerable in one line.
func TestTheListingSaysWhoSetEachValue(t *testing.T) {
	e, ctx, admin, _ := configureBoard(t)
	e.ApplySetting("wake.extend_turn_for", "urgent")
	if _, err := e.Configure(ctx, admin, "hooks.mail_bodies", "false"); err != nil {
		t.Fatal(err)
	}
	r, err := e.Configure(ctx, admin, "", "")
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := r["settings"].([]map[string]any)
	got := map[string]string{}
	for _, row := range rows {
		k, _ := row["setting"].(string)
		by, _ := row["set_by"].(string)
		got[k] = by
	}
	if got["wake.extend_turn_for"] != "the configuration file" {
		t.Errorf("a value from dibs.toml is attributed to %q", got["wake.extend_turn_for"])
	}
	if got["hooks.mail_bodies"] != "boss" {
		t.Errorf("a value an admin set is attributed to %q", got["hooks.mail_bodies"])
	}
}
