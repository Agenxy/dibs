package main

import (
	"encoding/json"
	"fmt"
)

// jsonHelp is the sentence every read-only verb's --json flag carries.
//
// The compatibility promise lives in the help text deliberately: a JSON
// surface becomes an interface the moment somebody scripts against it, and
// the place they will read the promise is the flag they are about to script.
// Holding to it means fields may be ADDED to these documents, and none
// removed or renamed, within a major version.
const jsonHelp = "emit JSON on stdout instead of the human text (fields may be " +
	"added over time; none will be removed or renamed within a major version)"

// printJSON emits one compact document per call: the same shape `dibs probe
// --json` already emits, so everything machine-readable here parses the same
// way.
func printJSON(v any) error {
	out, err := json.Marshal(v)
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// reportedError tells main a failure is already carried by the JSON document
// on stdout. The exit status must still say failure, but repeating the
// message as `agents: ...` would hand a script two copies of one fact: the
// same reasoning earlyDoctorError applies to doctor's prose.
type reportedError struct{ error }

func (reportedError) exitOnly() {}
