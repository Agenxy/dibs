package boardconfig

import (
	"strings"
	"testing"
)

// The fallback wake command obeys the same rule as the primary.
//
// It has the same power: argv handed to exec on the operator's behalf, with
// values substituted from what agents said. A second key that skipped the
// checks the first one has would be the exact shape of bug this repository
// keeps paying for, one rule implemented twice, so both go through one
// function and this asserts the second one really does.
func TestTheFallbackWakeCommandIsValidatedLikeThePrimary(t *testing.T) {
	good := []string{"codex", "exec", "resume", "{thread}", "{message}"}
	for _, c := range []struct {
		name     string
		fallback []string
		wantErr  string
	}{
		{"none is fine", nil, ""},
		{"a real command is fine", []string{"codex", "queue", "--thread", "{thread}"}, ""},
		{"a blank program is refused", []string{" ", "x"}, "fallback starts with an empty string"},
		{"a placeholder program is refused", []string{"{agent}", "x"}, "fallback[0] is"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := validateWakeEntry("codex", WakeExec{Argv: good, Fallback: c.fallback},
				map[string]WakeExec{"codex": {Argv: good, Fallback: c.fallback}})
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("refused a valid entry: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("the fallback escaped the rule the primary is held to: got %v, "+
					"want an error mentioning %q. A second key with exec's power and "+
					"none of its checks lets an agent's name choose the executable", err, c.wantErr)
			}
		})
	}
}
