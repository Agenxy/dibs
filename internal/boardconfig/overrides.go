package boardconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Overrides are settings an admin agent changed while the board was running.
//
// A SEPARATE FILE, NOT A REWRITE OF dibs.toml, and the reason is the file
// rather than the format. dibs.toml is mostly the operator's comments: why
// this cooldown, which harness reports which name, what was measured. A
// daemon that rewrites TOML destroys every one of them and hands back a file
// a machine can read and a person cannot. That trade is never worth making
// silently, and an operator who wanted their reasoning kept would have no way
// to ask for it.
//
// So an override goes beside it and layers on top at boot. Three things fall
// out of that and all three are wanted: the hand-written file stays exactly as
// written; everything an agent changed is in ONE place, so `cat` answers "what
// has been done to this board"; and deleting that file reverts all of it
// without touching anything the operator wrote.
//
// Precedence is the same direction as everywhere else: the more specific and
// more recent wins, so an override beats the file. An operator who wants the
// file to win deletes the override, which is the same gesture as reverting it.
type Overrides struct {
	// Set is setting name to what it was set to, most recent wins.
	Set map[string]Override `json:"set"`
}

// Override is one change and its provenance.
//
// WHO AND WHEN ARE NOT BOOKKEEPING. The point of letting an agent change
// settings is that the operator does not have to, which means the operator is
// not watching when it happens. "The board behaves differently than I expect"
// has to be answerable, and `coordinator set identity.unidentified to strict
// at 14:02` answers it in one line.
type Override struct {
	Value string    `json:"value"`
	By    string    `json:"by"`
	At    time.Time `json:"at"`
}

// OverridesName is the file, beside dibs.toml in the data directory.
const OverridesName = "overrides.json"

// LoadOverrides reads the file. Absent is the ordinary case and is not an
// error; unreadable IS one, because silently ignoring a file that exists
// would have the board run with settings the operator can see written down
// and cannot see applied.
func LoadOverrides(dir string) (Overrides, error) {
	var o Overrides
	b, err := os.ReadFile(filepath.Join(dir, OverridesName)) // #nosec G304 -- composed from the data directory
	if os.IsNotExist(err) {
		return Overrides{Set: map[string]Override{}}, nil
	}
	if err != nil {
		return o, fmt.Errorf("reading %s: %w", OverridesName, err)
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return o, fmt.Errorf("%s is not readable as JSON: %w. Delete it to go back "+
			"to what dibs.toml says", OverridesName, err)
	}
	if o.Set == nil {
		o.Set = map[string]Override{}
	}
	return o, nil
}

// SaveOverride records one change, rewriting the file.
//
// WHOLE FILE, WRITTEN THEN RENAMED. A board that crashes mid-write must come
// back reading either the old set or the new one, never half of a JSON
// object: an unparseable overrides file would take the daemon's own
// configuration down with it, which is a worse failure than losing one
// setting.
func SaveOverride(dir, key, value, by string) error {
	o, err := LoadOverrides(dir)
	if err != nil {
		// A file that cannot be read is replaced rather than appended to: the
		// alternative is a board that can never save another setting because
		// of one bad byte, and the values are all recoverable from the running
		// engine, which is what is writing them.
		o = Overrides{Set: map[string]Override{}}
	}
	o.Set[key] = Override{Value: value, By: by, At: time.Now()}
	b, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, OverridesName+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, OverridesName))
}

// Keys lists the overridden settings in a stable order, so a listing and a
// log line do not disagree about what is set.
func (o Overrides) Keys() []string {
	out := make([]string, 0, len(o.Set))
	for k := range o.Set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
