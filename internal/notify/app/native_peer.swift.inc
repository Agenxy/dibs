// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

import Security

// The daemon supplies LOCAL_PEERTOKEN bytes on a private pipe. The Security
// framework resolves the dynamic guest; neither PID lookup nor its disk path
// is used as proof of the peer's identity.
func nativeCodeIdentityStatus(_ token: Data, expected url: URL) -> OSStatus {
    guard token.count == 32 else { return errSecParam }
    var expected: SecStaticCode?
    var guest: SecCode?
    var requirement: SecRequirement?
    var info: CFDictionary?
    let flags = SecCSFlags()
    var status = SecStaticCodeCreateWithPath(url as CFURL, flags, &expected)
    if status == errSecSuccess, let expected { status = SecStaticCodeCheckValidity(expected, flags, nil) }
    if status == errSecSuccess, let expected {
        status = SecCodeCopySigningInformation(expected, SecCSFlags(rawValue: kSecCSSigningInformation), &info)
    }
    if status == errSecSuccess, let info = info as? [String: Any], let expected {
        let signing = (info[kSecCodeInfoFlags as String] as? NSNumber)?.uint32Value ?? 0
        if signing & 2 != 0 { // kSecCodeSignatureAdhoc
            guard let hash = info[kSecCodeInfoUnique as String] as? Data, !hash.isEmpty else { return errSecCSUnsigned }
            let hex = hash.map { String(format: "%02x", $0) }.joined()
            status = SecRequirementCreateWithString(("cdhash H\"" + hex + "\"") as CFString, flags, &requirement)
        } else {
            status = SecCodeCopyDesignatedRequirement(expected, flags, &requirement)
        }
    } else if status == errSecSuccess { status = errSecCSUnsigned }
    if status == errSecSuccess {
        status = SecCodeCopyGuestWithAttributes(nil, [kSecGuestAttributeAudit as String: token] as CFDictionary, flags, &guest)
    }
    if status == errSecSuccess, let guest, let requirement {
        status = SecCodeCheckValidity(guest, flags, requirement)
    } else if status == errSecSuccess { status = errSecCSUnsigned }
    return status
}

func verifyNativeNotificationPeer(board: Bool = false) -> Int32 {
    let token = FileHandle.standardInput.readData(ofLength: 33)
    guard let executable = Bundle.main.executableURL else { return 2 }
    let expected = board ? executable.deletingLastPathComponent().appendingPathComponent("dibd") : executable
    let status = nativeCodeIdentityStatus(token, expected: expected)
    if status != errSecSuccess {
        FileHandle.standardError.write(Data("Dibs native peer signature refused (\(status)); use the installed Dibs app or the board\n".utf8))
        return 2
    }
    print(#"{"attested":true}"#)
    return 0
}

// Isolate Security's lookup behind a bounded, read-only helper invocation, as
// the daemon does for the reverse direction. No PID or expected path is input.
func nativeBoardPeerAttested(_ token: Data) -> Bool {
    guard token.count == 32, let executable = Bundle.main.executableURL else { return false }
    let process = Process(), input = Pipe(), output = Pipe(), done = DispatchSemaphore(value: 0)
    process.executableURL = executable; process.arguments = ["--verify-notification-board"]
    process.standardInput = input; process.standardOutput = output; process.standardError = FileHandle.nullDevice
    process.terminationHandler = { _ in done.signal() }
    do { try process.run() } catch { return false }
    input.fileHandleForWriting.write(token); input.fileHandleForWriting.closeFile()
    guard done.wait(timeout: .now() + 5) == .success else { process.terminate(); return false }
    let bytes = output.fileHandleForReading.readData(ofLength: 4097)
    guard process.terminationStatus == 0, bytes.count <= 4096,
          let value = (try? JSONSerialization.jsonObject(with: bytes)) as? [String: Any] else { return false }
    return value["attested"] as? Bool == true
}
