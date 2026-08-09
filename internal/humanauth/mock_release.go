//go:build !lanesdev

package humanauth

// This file is the release half of the presence mock, and its entire job is to
// not exist in the other one.
//
// The mock lets somebody drive the human flow with no finger on the sensor —
// necessary, because the flow is otherwise untestable without a person sitting
// there. But "a human is present" is the one assertion in Lanes that must not be
// forgeable by software, and an environment variable is software. So the switch
// is not a runtime check that a release build happens to leave off: the code
// that can answer "yes, mocked" is behind a build tag and is not compiled at
// all. There is no misconfiguration, no stray export, and no inherited
// environment that can turn it on in a shipped binary, because the branch that
// would read the variable is absent from the object file.
//
// The default `go build` and `go test ./...` both land here, so the guarantee is
// the one that holds by default rather than the one that holds if somebody
// remembers.

// mocked reports whether a scripted verdict is in force. Compile-time no.
func mocked() (Verdict, bool) { return Unavailable, false }

// Mocked reports whether presence is being answered by a script rather than by
// a person.
//
// Callers surface this in their results rather than keeping it to themselves. A
// mocked unlock that looked identical to a real one would be evidence of
// nothing, and the person reading the transcript later — quite possibly the
// person who set the variable — has no other way to tell the two apart.
func Mocked() bool { return false }
