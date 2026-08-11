// agents-presence: proves a HUMAN is at this machine, right now.
//
// Dibs' panel runs inside an agent's MCP host and acts with that agent's own
// token. That is fine for answering the agent's mail: the agent handed the token
// over. It is NOT fine for speaking AS the operator. "Stand down, this is your
// operator" is exactly the message that must never be forgeable, and nothing in
// the transport can tell "the human clicked Broadcast" from "an agent called the
// tool and set a flag": both arrive on the same connection, with the same
// credential.
//
// So the proof has to come from outside the transport. Touch ID is the right
// primitive because an agent confined to that transport cannot produce it: one
// that tried to unlock would raise this sheet on the human's own Mac, and the
// human would decline it. Presence is verified rather than asserted, and the
// failure mode is a visible prompt rather than a silent escalation.
//
// The bound is the transport, not the machine. Code already running as the user
// can replace this binary in the directory it was installed into and return
// success without asking anybody: see findHelper in presence.go for what that
// does and does not cost. Saying "software cannot produce a fingerprint", as an
// earlier version of this comment did, overstated it.
//
// A separate binary rather than cgo, deliberately: Dibs ships CGO_ENABLED=0 and
// cross-compiles to four targets, so linking LocalAuthentication into the daemon
// would break the build everywhere it is not macOS. The daemon execs this and
// reads the exit code, which also means a missing or unrunnable helper degrades
// to the password path instead of taking the daemon with it.
//
// Exit codes are the whole API:
//
//	0  a human authenticated just now
//	1  a human was asked and did not authenticate (declined, failed, cancelled)
//	2  biometrics are unavailable here: the caller should fall back to the
//	   admin password, which is a different sentence to say to the user
//
// The distinction between 1 and 2 is load-bearing. "You cancelled" and "this Mac
// cannot do this" are different facts, and telling somebody to try their finger
// again on a machine with no sensor is the kind of unhelpful advice this project
// treats as a defect.
import Foundation
import LocalAuthentication

// Biometrics ONLY, never .deviceOwnerAuthentication. The broader policy falls
// back to the login password on failure, and a login password proves possession
// of a credential an agent could in principle have been given: the point here
// is a fingerprint, which an agent on the transport cannot supply. When there is no sensor we exit 2 and let
// Dibs ask for its own admin password, so the fallback stays explicit and
// visible rather than silently swapping one factor for another.
let policy: LAPolicy = .deviceOwnerAuthenticationWithBiometrics

let context = LAContext()
context.localizedCancelTitle = "Cancel"

var probe: NSError?
guard context.canEvaluatePolicy(policy, error: &probe) else {
    if let probe { FileHandle.standardError.write(Data("\(probe.localizedDescription)\n".utf8)) }
    exit(2)
}

// The reason string is shown to the human inside the system sheet, so it is the
// one chance to say what they are approving. Passed in by the daemon so the
// sentence can name the actual action ("post to the agent 'auth-work'") rather
// than a generic one.
let reason = CommandLine.arguments.count > 1 && !CommandLine.arguments[1].isEmpty
    ? CommandLine.arguments[1]
    : "act as the human on the Dibs board"

let done = DispatchSemaphore(value: 0)
var verified = false
context.evaluatePolicy(policy, localizedReason: reason) { ok, err in
    verified = ok
    if let err { FileHandle.standardError.write(Data("\(err.localizedDescription)\n".utf8)) }
    done.signal()
}

// The daemon already bounds this with its own timeout and kills us; waiting
// forever here would only matter if it did not, and a sheet the human never
// answers should not become a process that never exits.
if done.wait(timeout: .now() + 120) == .timedOut { exit(1) }
exit(verified ? 0 : 1)
