// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

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
import CryptoKit
import Foundation
import LocalAuthentication
import Security

if CommandLine.arguments.count > 1 && CommandLine.arguments[1] == "--key" {
    runKey(Array(CommandLine.arguments.dropFirst(2)))
}

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

// ── the person's key ────────────────────────────────────────────────────────
//
// `dibs-presence --key …` holds the person's key in this Mac's Secure Enclave,
// for a board that runs somewhere else and so cannot read this Mac's sensor:
// presence has to arrive as something it can CHECK, a signature from a key
// that signs only after Touch ID. docs/NETWORK.md §8 is the argument.
//
// In this binary rather than its own because it is the same job, proving a
// person, and this one already ships, signed, everywhere a Mac build goes.
//
// The key is created with a biometric access control and the Secure Enclave
// enforces it, not this program. So, unlike the check above, replacing this
// binary buys an attacker nothing here: whatever runs, the chip still asks for
// a finger before it signs, and the private half never leaves it. What is
// stored on disk is the Enclave's own wrapped form, useless on any other Mac.
//
//	dibs-presence --key create <file>          writes the key, prints its public half
//	dibs-presence --key public <file>          prints the public half
//	dibs-presence --key sign <file> <reason>   signs stdin after Touch ID, prints the signature
//
//	0  done; the output is base64 (DER SubjectPublicKeyInfo, or a DER ECDSA signature)
//	1  a person was asked and did not authenticate (declined, failed, cancelled)
//	2  this Mac cannot do it (no Secure Enclave or no Touch ID, no key file,
//	   or a key from another Mac)

func fail(_ code: Int32, _ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8))
    exit(code)
}

func publicB64(_ key: SecureEnclave.P256.Signing.PrivateKey) -> String {
    key.publicKey.derRepresentation.base64EncodedString()
}

func runKey(_ args: [String]) -> Never {
guard args.count >= 2 else {
    fail(2, "usage: dibs-presence --key create|public|sign <file> [reason]")
}
guard SecureEnclave.isAvailable else {
    fail(2, "this Mac has no Secure Enclave")
}
let file = URL(fileURLWithPath: args[1])

switch args[0] {
case "create":
    var err: Unmanaged<CFError>?
    // A finger, and only a finger: biometryAny, not userPresence. The
    // broader flag falls back to the login password, which proves possession
    // of something an agent could in principle have been given, the same
    // reason dibs-presence refuses .deviceOwnerAuthentication. biometryAny
    // rather than biometryCurrentSet so a finger enrolled later still works.
    guard let access = SecAccessControlCreateWithFlags(
        nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
        [.privateKeyUsage, .biometryAny], &err)
    else {
        fail(2, "could not describe the key's access control: \(String(describing: err?.takeRetainedValue()))")
    }
    do {
        let key = try SecureEnclave.P256.Signing.PrivateKey(accessControl: access)
        try FileManager.default.createDirectory(
            at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
        try key.dataRepresentation.write(to: file, options: [.atomic])
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
        print(publicB64(key))
    } catch {
        fail(2, "could not create the key: \(error.localizedDescription)")
    }

case "public":
    guard let blob = try? Data(contentsOf: file) else { fail(2, "no key at \(file.path)") }
    guard let key = try? SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: blob) else {
        fail(2, "the key at \(file.path) does not belong to this Mac's Secure Enclave")
    }
    print(publicB64(key))

case "sign":
    guard let blob = try? Data(contentsOf: file) else { fail(2, "no key at \(file.path)") }
    let message = FileHandle.standardInput.readDataToEndOfFile()
    // The reason is the one sentence the person reads in the system sheet, so
    // the caller names the actual act ("approve: make reviewer coordinator").
    let context = LAContext()
    context.localizedReason = args.count > 2 && !args[2].isEmpty ? args[2] : "answer as you on the Dibs board"
    context.localizedCancelTitle = "Cancel"
    let key: SecureEnclave.P256.Signing.PrivateKey
    do {
        key = try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: blob, authenticationContext: context)
    } catch {
        fail(2, "the key at \(file.path) does not belong to this Mac's Secure Enclave")
    }
    do {
        let sig = try key.signature(for: message)
        print(sig.derRepresentation.base64EncodedString())
    } catch {
        // Declined, cancelled or failed: the Enclave refused to sign without a
        // person, which is the guarantee working.
        fail(1, "not signed: \(error.localizedDescription)")
    }

default:
    fail(2, "unknown command \(args[0]): create, public or sign")
}
exit(0)
}
