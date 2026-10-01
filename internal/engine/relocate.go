package engine

import (
	"context"
	"log/slog"
	"sort"
	"strings"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/wakeexec"
)

// Relocation: the one deliberate way an agent runs somewhere other than where
// it last ran. See core/relocate.go for who may do it and why it is not a wake.
//
// The environments are of two kinds. The ChatGPT app is built in, because Dibs
// knows how to put a Codex thread there (its own codex:// route) and it needs
// no configuration. Every other one is the operator's, as argv under
// [relocate.<name>] in dibs.toml: `codex exec resume`, `claude --resume`, any
// command that runs a thread. Those are exactly the commands a wake refuses
// (boardconfig.HostsAnAgent), and this is the only path that runs them, behind
// a permission and in the ledger.

// RelocateCommand is one operator-configured environment.
type RelocateCommand struct {
	Argv []string
}

// SetRelocations installs the operator's [relocate.*] table.
func (e *Engine) SetRelocations(envs map[string]RelocateCommand) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	e.wakers.relocations = map[string]RelocateCommand{}
	for name, c := range envs {
		e.wakers.relocations[strings.ToLower(name)] = c
	}
}

// relocationRunner starts the command; replaced by tests, which must never run
// `open codex://...` or a real agent.
var relocationRunner = func(argv []string, agent, dir string) bool {
	return wakeexec.Run(argv, agent, dir, wakeexec.Timeout, wakeexec.Grace)
}

// Relocate moves an agent at another agent's request; the fold checks that
// the caller may.
func (e *Engine) Relocate(ctx context.Context, token, agent, env string) (core.Result, error) {
	return e.relocate(ctx, &core.Op{Kind: core.OpRelocate, Token: token, To: agent, Mode: env})
}

// RelocateByHuman is the same decision made by a person on the admin path.
func (e *Engine) RelocateByHuman(ctx context.Context, agent, env string) (core.Result, error) {
	return e.relocate(ctx, &core.Op{Kind: core.OpRelocateByHuman, To: agent, Mode: env})
}

// GrantPermissionByHuman grants a permission beyond the role; the admin path's.
func (e *Engine) GrantPermissionByHuman(ctx context.Context, agent, perm string) (core.Result, error) {
	return e.Do(ctx, &core.Op{Kind: core.OpGrantPermission, To: agent, Mode: perm})
}

// RevokePermissionByHuman takes a grant back.
func (e *Engine) RevokePermissionByHuman(ctx context.Context, agent, perm string) (core.Result, error) {
	return e.Do(ctx, &core.Op{Kind: core.OpRevokePermission, To: agent, Mode: perm})
}

// relocation is what to run, decided before the op is ledgered so that a move
// that cannot happen is refused rather than recorded.
type relocation struct {
	argv  []string
	cwd   string
	agent string
}

func (e *Engine) relocate(ctx context.Context, op *core.Op) (core.Result, error) {
	op.Mode = strings.ToLower(strings.TrimSpace(op.Mode))
	if op.To == "" || op.Mode == "" {
		return nil, &core.Error{
			Code: "E_BAD_REQUEST", Msg: "relocate needs an agent and an environment",
			Hint: "name the agent to move and the environment to move it to",
		}
	}
	planned, err := e.query(ctx, func() core.Result {
		r, err := e.planRelocation(op)
		if err != nil {
			return core.Result{"error": err}
		}
		return core.Result{"plan": r}
	})
	if err != nil {
		return nil, err
	}
	if perr, ok := planned["error"].(error); ok {
		return nil, perr
	}
	r, _ := planned["plan"].(relocation)
	res, err := e.Do(ctx, op)
	if err != nil {
		return nil, err
	}
	// Recorded first, run second: the ledger says a person or an agent
	// decided this, whatever the command then does. Off the writer loop, like
	// every command Dibs runs.
	go func() {
		ok := relocationRunner(r.argv, r.agent, r.cwd)
		if ok {
			slog.Info("relocated an agent", "agent", r.agent, "to", op.Mode)
		} else {
			slog.Warn("the relocation command failed; the agent did not start there",
				"agent", r.agent, "to", op.Mode, "argv0", r.argv[0])
		}
	}()
	res["started"] = true
	res["note"] = "the command was started; it runs the agent's thread in " + op.Mode +
		", and the daemon log records how it exited"
	return res, nil
}

// planRelocation is everything that can be known before the move: on the loop.
func (e *Engine) planRelocation(op *core.Op) (relocation, error) {
	// The permission first, so a caller who may not move anybody learns that
	// and nothing about the agent or the board's environments. The fold
	// checks it again, against the state it applies to.
	if op.Kind == core.OpRelocate {
		by := e.state.AgentByToken(op.Token)
		if by == nil {
			return relocation{}, core.ErrBadToken
		}
		if !by.MayRelocate() {
			return relocation{}, core.ErrNotPermittedToRelocate
		}
	}
	l := e.state.Agents[op.To]
	if l == nil {
		return relocation{}, &core.Error{
			Code: "E_NO_AGENT", Msg: "no agent " + op.To,
			Hint: "check the board for the agent's id",
		}
	}
	if err := e.refuseToMove(l, op.Mode); err != nil {
		return relocation{}, err
	}
	argv, err := e.relocationCommand(l, op.Mode)
	if err != nil {
		return relocation{}, err
	}
	return relocation{argv: argv, cwd: cwdOf(l), agent: l.ID}, nil
}

// refuseToMove is every reason this agent cannot be moved at all.
func (e *Engine) refuseToMove(l *core.Agent, env string) error {
	if human := e.humanIdentityLocked(); human != "" && l.ID == human {
		return &core.Error{
			Code: "E_IS_HUMAN", Msg: l.ID + " is the person's own row",
			Hint: "the human decides where they work; send them a message instead",
		}
	}
	if l.Status == core.StatusActive {
		return &core.Error{
			Code: "E_AGENT_RUNNING",
			Msg:  l.ID + " is running where it is",
			Hint: "a message reaches it there, and starting its thread somewhere else as well " +
				"would put two writers on one thread. Relocate an agent whose harness is closed",
		}
	}
	if host := e.remoteHostOf(l); host != "" {
		return &core.Error{
			Code: "E_AGENT_ELSEWHERE",
			Msg:  l.ID + " runs on " + host + ", and a relocation runs a command on this machine",
			Hint: "relocate it from the board on " + host,
		}
	}
	if threadIDOf(l) == "" {
		return &core.Error{
			Code: "E_NO_THREAD",
			Msg:  l.ID + " has no harness thread on record",
			Hint: "only an agent whose harness reported a thread can be resumed elsewhere",
		}
	}
	if l.Agent != nil && l.Agent.Surface == env {
		return &core.Error{
			Code: "E_SAME_ENVIRONMENT",
			Msg:  l.ID + " already runs in " + env,
			Hint: "a wake reaches it there; relocation is for moving it somewhere else",
		}
	}
	return nil
}

// relocationCommand is what runs the agent's thread in env.
func (e *Engine) relocationCommand(l *core.Agent, env string) ([]string, error) {
	thread := threadIDOf(l)
	if env == harnessenv.ChatGPTApp {
		if !strings.Contains(wakeHarness(l), "codex") {
			return nil, &core.Error{
				Code: "E_WRONG_HARNESS",
				Msg:  "the ChatGPT app runs Codex threads, and " + l.ID + " is not one",
				Hint: "relocate it to an environment configured for its own harness",
			}
		}
		argv := harnessenv.ChatGPTOpenArgv(thread)
		if argv == nil {
			return nil, &core.Error{
				Code: "E_NO_THREAD",
				Msg:  "the thread on record is not one the app can open", Hint: "nothing was moved",
			}
		}
		return argv, nil
	}
	e.wakers.mu.Lock()
	c, ok := e.wakers.relocations[env]
	e.wakers.mu.Unlock()
	if !ok || len(c.Argv) == 0 {
		return nil, &core.Error{
			Code: "E_NO_ENVIRONMENT",
			Msg:  "no environment called " + env,
			Hint: "the environments on this board are " + strings.Join(e.environments(), ", "),
		}
	}
	f := wakeexec.Fields{Thread: thread, Agent: l.ID, Message: wakeexec.Compose("")}
	return f.Apply(c.Argv), nil
}

// environments lists where an agent can be moved to, for a hint.
func (e *Engine) environments() []string {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	out := []string{harnessenv.ChatGPTApp}
	for name := range e.wakers.relocations {
		out = append(out, name)
	}
	sort.Strings(out[1:])
	return out
}
