package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Settings an admin agent may change while the board is running.
//
// WHY THIS EXISTS. Configuration lived in one place, dibs.toml, read at boot,
// which meant every adjustment was a person opening an editor and a daemon
// restart. That is the wrong shape for a coordination tool whose whole premise
// is that agents handle things: an operator who has granted an agent admin
// should not then have to go and dig through a file on its behalf.
//
// WHY ONLY THESE FIVE. Every one has an engine setter that takes effect on the
// next decision. An address or a TLS certificate does not: changing one would
// report success and do nothing until a restart, which is the class of bug
// this repository keeps paying for. So the boundary is honest rather than
// generous, and it is checkable: a setting belongs here when the engine can
// apply it now.
//
// WHY NOT REWRITE dibs.toml. That file is the operator's, and it is mostly
// their comments and reasoning. A daemon that rewrites TOML destroys them,
// trading a file a person can read for one a machine can. Overrides go to
// their own file beside it, layered on top at boot, which keeps the hand
// written one intact and puts everything an agent changed in one place that
// can be deleted to revert.
type setting struct {
	// apply takes the value to the engine. Returns the normalised value that
	// was stored, or an error a drifted agent can act on.
	apply func(e *Engine, v string) (string, error)
	// describe is what the value means, for the listing.
	describe string
}

var settings = map[string]setting{
	"wake.extend_turn_for": {
		describe: `which news may extend a turn: "all", "urgent" or "none"`,
		apply: func(e *Engine, v string) (string, error) {
			switch WakePhase(v) {
			case WakeAll, WakeUrgent, WakeNone:
				e.SetWakePolicy(WakePhase(v))
				return v, nil
			}
			return "", fmt.Errorf(`use "all", "urgent" or "none"`)
		},
	},
	"wake.notices_wake": {
		describe: "whether situational awareness alone may extend a turn",
		apply:    boolSetting((*Engine).SetNoticesWake),
	},
	"wake.sockets": {
		describe: "whether the harness session socket may be used to reach an agent",
		apply:    boolSetting((*Engine).SetSocketWakes),
	},
	"hooks.mail_bodies": {
		describe: "whether a hook delivery carries the message text or a pointer to it",
		apply:    boolSetting((*Engine).SetMailBodies),
	},
	"identity.unidentified": {
		describe: `who a hook with no session id resolves to: "directory", "strict", "ask" or "coordinator"`,
		apply: func(e *Engine, v string) (string, error) {
			for _, p := range []string{"directory", "strict", "ask", "coordinator"} {
				if v == p {
					e.SetUnidentifiedPolicy(v)
					return v, nil
				}
			}
			return "", fmt.Errorf(`use "directory", "strict", "ask" or "coordinator"`)
		},
	},
}

func boolSetting(set func(*Engine, bool)) func(*Engine, string) (string, error) {
	return func(e *Engine, v string) (string, error) {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return "", fmt.Errorf(`use "true" or "false"`)
		}
		set(e, b)
		return strconv.FormatBool(b), nil
	}
}

// settingsState is the current effective value of each setting and who last
// set it, so a listing can answer "what is it now" and "who did that".
type settingsState struct {
	mu   sync.RWMutex
	now  map[string]string
	by   map[string]string
	when map[string]time.Time
}

// SetSettingStore hands the engine somewhere to persist an override.
//
// A callback rather than a path, because writing a file is not the engine's
// business and doing it on the writer loop would be worse: the loop is
// single-writer for the board, and a slow disk must not stop agents
// coordinating. dibd wires this to the overrides file.
func (e *Engine) SetSettingStore(save func(key, value, by string) error) {
	e.settings.mu.Lock()
	defer e.settings.mu.Unlock()
	e.saveSetting = save
}

// noteSetting records the effective value without persisting it: what boot
// does, so a listing is right before anybody has changed anything.
func (e *Engine) noteSetting(key, value, by string) {
	e.settings.mu.Lock()
	defer e.settings.mu.Unlock()
	if e.settings.now == nil {
		e.settings.now, e.settings.by = map[string]string{}, map[string]string{}
		e.settings.when = map[string]time.Time{}
	}
	e.settings.now[key], e.settings.by[key] = value, by
	e.settings.when[key] = time.Now()
}

// ApplySetting is boot's path: apply and record, without persisting.
func (e *Engine) ApplySetting(key, value string) {
	s, ok := settings[key]
	if !ok {
		return
	}
	got, err := s.apply(e, value)
	if err != nil {
		slog.Warn("a configured setting was refused at boot and the default stands",
			"setting", key, "value", value, "want", err.Error())
		return
	}
	e.noteSetting(key, got, "the configuration file")
}

// Configure reads or changes a setting. Reading needs a token; changing needs
// admin.
//
// ADMIN, NOT COORDINATOR, and the difference is the point. A coordinator runs
// the fleet: it evicts, adopts and force-releases, all of which are visible on
// the board and undoable from it. Changing a setting changes how the board
// itself behaves for every agent on it, including the ones that will never
// look at this file, so it is the grant a human makes deliberately.
func (e *Engine) Configure(ctx context.Context, token, key, value string) (core.Result, error) {
	change := key != "" && value != ""
	res, err := e.query(ctx, func() core.Result {
		l := e.state.AgentByToken(token)
		if l == nil {
			return core.Result{"error": core.ErrBadToken}
		}
		if change && !l.IsAdmin() {
			return core.Result{"error": core.ErrNotAdmin}
		}
		return core.Result{"agent": l.ID}
	})
	// The loop turns a result-carried error into a returned one, so a refusal
	// arrives here as err and res is nil. Every refusal below is returned the
	// same way rather than as a field, because a caller that has to check two
	// places checks one of them.
	if err != nil {
		return nil, err
	}
	who, _ := res["agent"].(string)
	if !change {
		return core.Result{"settings": e.listSettings()}, nil
	}

	s, ok := settings[key]
	if !ok {
		return nil, &core.Error{
			Code: "E_NO_SETTING",
			Msg:  fmt.Sprintf("there is no setting %q that takes effect while the board runs", key),
			Hint: "call configure with no setting to list the ones there are. " +
				"An address or a certificate is not among them: those need a restart, " +
				"and a call that reported success and did nothing would be worse than this",
		}
	}
	got, err := s.apply(e, value)
	if err != nil {
		return nil, &core.Error{
			Code: "E_BAD_SETTING",
			Msg:  fmt.Sprintf("%s = %q: %s", key, value, err),
			Hint: s.describe,
		}
	}
	e.noteSetting(key, got, who)

	// PERSISTED AFTER IT IS APPLIED, and the order matters. A value that the
	// engine refused must not survive in a file that boot will read back: that
	// is how a board comes up refusing its own configuration.
	e.settings.mu.RLock()
	save := e.saveSetting
	e.settings.mu.RUnlock()
	if save != nil {
		if err := save(key, got, who); err != nil {
			slog.Warn("a setting was applied and could not be written down, so it "+
				"will not survive a restart", "setting", key, "value", got,
				"by", who, "err", err)
			return core.Result{
				"setting": key, "value": got, "by": who,
				"warning": "applied now and NOT saved (" + err.Error() + "), so a " +
					"restart puts it back: fix the data directory or set it in dibs.toml",
			}, nil
		}
	}
	slog.Info("an admin changed a board setting", "setting", key, "value", got, "by", who)
	return core.Result{"setting": key, "value": got, "by": who, "saved": save != nil}, nil
}

// listSettings is every setting, its value now, and who last set it.
func (e *Engine) listSettings() []map[string]any {
	e.settings.mu.RLock()
	defer e.settings.mu.RUnlock()
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		row := map[string]any{"setting": k, "means": settings[k].describe}
		if v, ok := e.settings.now[k]; ok {
			row["value"] = v
			row["set_by"] = e.settings.by[k]
			if t, ok := e.settings.when[k]; ok {
				row["set_at"] = t
			}
		} else {
			row["value"] = "(the default; nothing has set it)"
		}
		out = append(out, row)
	}
	return out
}
