// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

enum NativeHumanFailure: Error, Equatable { case unavailable, boardChanged }

// Resolve a fresh board route on each opening. New notifications retain their
// posting profile as non-secret routing data. Legacy notifications use the
// bundled daemon's existing service-pin selector, never a copied credential.
func nativeHumanProfile(_ hint: String?) throws -> String {
    if let hint, hint.hasPrefix("/"), hint.utf8.count <= 4096 { return hint }
    guard let executable = Bundle.main.executableURL else { throw NativeHumanFailure.unavailable }
    let daemon = executable.deletingLastPathComponent().appendingPathComponent("dibd")
    let process = Process(), pipe = Pipe(), finished = DispatchSemaphore(value: 0)
    process.executableURL = daemon; process.arguments = ["--notification-profile"]
    process.standardOutput = pipe; process.standardError = FileHandle.nullDevice
    process.terminationHandler = { _ in finished.signal() }
    try process.run()
    guard finished.wait(timeout: .now() + 5) == .success else { process.terminate(); throw NativeHumanFailure.unavailable }
    let raw = pipe.fileHandleForReading.readData(ofLength: 8193)
    guard process.terminationStatus == 0, raw.count <= 8192,
          let value = try JSONSerialization.jsonObject(with: raw) as? [String: Any],
          let dir = value["dir"] as? String, dir.hasPrefix("/") else { throw NativeHumanFailure.unavailable }
    return dir
}

func nativeHumanRPC(profile: String, method: String, payload: [String: Any]) throws -> [String: Any] {
    let path = profile + "/dibd.sock"
    var info = stat()
    guard lstat(profile, &info) == 0, info.st_mode & S_IFMT == S_IFDIR,
          info.st_uid == geteuid(), info.st_mode & 0o077 == 0 else { throw NativeHumanFailure.unavailable }
    var address = sockaddr_un()
    address.sun_family = sa_family_t(AF_UNIX)
    let bytes = Array(path.utf8) + [0]
    guard bytes.count <= MemoryLayout.size(ofValue: address.sun_path) else { throw NativeHumanFailure.unavailable }
    withUnsafeMutableBytes(of: &address.sun_path) { $0.copyBytes(from: bytes) }
    let fd = socket(AF_UNIX, SOCK_STREAM, 0)
    guard fd >= 0 else { throw NativeHumanFailure.unavailable }
    defer { Darwin.close(fd) }
    let original = fcntl(fd, F_GETFL)
    guard original >= 0, fcntl(fd, F_SETFL, original | O_NONBLOCK) == 0 else { throw NativeHumanFailure.unavailable }
    let result = withUnsafePointer(to: &address) { pointer in
        pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
    }
    if result != 0 {
        guard errno == EINPROGRESS else { throw NativeHumanFailure.unavailable }
        var event = pollfd(fd: fd, events: Int16(POLLOUT), revents: 0)
        guard poll(&event, 1, 5000) > 0 else { throw NativeHumanFailure.unavailable }
        var error: Int32 = 0, length = socklen_t(MemoryLayout<Int32>.size)
        guard getsockopt(fd, SOL_SOCKET, SO_ERROR, &error, &length) == 0, error == 0 else { throw NativeHumanFailure.unavailable }
    }
    guard fcntl(fd, F_SETFL, original) == 0 else { throw NativeHumanFailure.unavailable }
    var uid: uid_t = 0, gid: gid_t = 0
    guard getpeereid(fd, &uid, &gid) == 0, uid == geteuid() else { throw NativeHumanFailure.unavailable }
    var audit = [UInt8](repeating: 0, count: 32), auditLength = socklen_t(32)
    guard getsockopt(fd, SOL_LOCAL, LOCAL_PEERTOKEN, &audit, &auditLength) == 0, auditLength == 32,
          nativeBoardPeerAttested(Data(audit)) else { throw NativeHumanFailure.boardChanged }
    var timeout = timeval(tv_sec: 15, tv_usec: 0), noSignal: Int32 = 1
    _ = setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, socklen_t(MemoryLayout<timeval>.size))
    _ = setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &timeout, socklen_t(MemoryLayout<timeval>.size))
    _ = setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSignal, socklen_t(MemoryLayout<Int32>.size))
    let body = try JSONSerialization.data(withJSONObject: payload)
    guard body.count <= 262144 else { throw NativeHumanFailure.unavailable }
    var request = Data("POST /api/human/native/\(method) HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: \(body.count)\r\nConnection: close\r\n\r\n".utf8)
    request.append(body)
    try request.withUnsafeBytes { buffer in
        var written = 0
        while written < buffer.count {
            let count = Darwin.write(fd, buffer.baseAddress!.advanced(by: written), buffer.count - written)
            if count < 0 && errno == EINTR { continue }
            guard count > 0 else { throw NativeHumanFailure.unavailable }
            written += count
        }
    }
    var response = Data(), chunk = [UInt8](repeating: 0, count: 16384)
    while true {
        let count = Darwin.read(fd, &chunk, chunk.count)
        if count < 0 && errno == EINTR { continue }
        guard count >= 0 else { throw NativeHumanFailure.unavailable }
        if count == 0 { break }
        response.append(contentsOf: chunk.prefix(count))
        guard response.count <= 1048576 else { throw NativeHumanFailure.unavailable }
    }
    guard let boundary = response.range(of: Data("\r\n\r\n".utf8)),
          let header = String(data: response[..<boundary.lowerBound], encoding: .utf8),
          let code = header.components(separatedBy: " ").dropFirst().first, let status = Int(code),
          let value = try JSONSerialization.jsonObject(with: response[boundary.upperBound...]) as? [String: Any] else { throw NativeHumanFailure.unavailable }
    var resultValue = value; resultValue["httpStatus"] = status
    return resultValue
}

enum NativeHumanStorage {
    static var windows = [String: NativeHumanController]() // main-thread only, lazily initialised before app.run
    #if DIBS_BROKER_FIXTURE || DIBS_READING_FIXTURE
    static var fixtureEvent: ((String, String, [String: Any]) -> Void)?
    #endif
}
var nativeHumanWindows: [String: NativeHumanController] {
    get { NativeHumanStorage.windows }
    set { NativeHumanStorage.windows = newValue }
}

// Only notification response handlers call this in production. There is no
// argv/stdin command that can submit a human reply without the native window.
func openNativeHumanMessage(id: String, profile: String?, title: String, detail: AskReadingDetail) -> Bool {
    guard validMessageID(id), detail.kind == "question" || detail.kind == "request" else { return false }
    #if DIBS_READING_FIXTURE
    // Existing offline reading fixtures have no daemon. A routed fixture is
    // explicit and cannot accidentally connect to the operator's real board.
    guard profile != nil || ProcessInfo.processInfo.environment["DIBS_NATIVE_PROFILE_DISCOVERY"] == "1" else { return false }
    #endif
    if let existing = nativeHumanWindows[id] { existing.refresh(); return true }
    let controller = NativeHumanController(id: id, profile: profile, title: title, detail: detail)
    nativeHumanWindows[id] = controller; controller.refresh()
    return true
}

final class NativeHumanController: @unchecked Sendable {
    let id: String, node: String
    let serial: UInt64
    let profileHint: String?
    let submission = UUID().uuidString // idempotency only, never authority
    var profile = ""
    var title: String
    var detail: AskReadingDetail
    var window: HumanPromptWindow?
    var refusedReply: HumanPromptReply?
    var refusalHelp = ""
    var pending = true, confirmed = false
    var choices = [String](), state = "unknown"
    init(id: String, profile: String?, title: String, detail: AskReadingDetail) {
        self.id = id; self.profileHint = profile; self.title = title; self.detail = detail
        let parts = id.components(separatedBy: ".")
        self.node = parts[2]; self.serial = UInt64(parts[3]) ?? 0
    }
    func refresh() {
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                let profile = try nativeHumanProfile(self.profileHint)
                let value = try nativeHumanRPC(profile: profile, method: "message", payload: ["node": self.node, "serial": self.serial])
                DispatchQueue.main.async { self.profile = profile; self.receive(value) }
            } catch { DispatchQueue.main.async { self.render(error: true) } }
        }
    }
    func receive(_ value: [String: Any]) {
        guard value["ok"] as? Bool == true, let view = value["view"] as? [String: Any],
              let notice = view["notice"] as? [String: Any], let body = notice["body"] as? String,
              let kind = notice["type"] as? String, let pending = view["pending"] as? Bool,
              let state = view["state"] as? String, let title = value["title"] as? String else { render(error: true); return }
        self.title = title; self.pending = pending; self.confirmed = true; self.state = state
        self.detail = AskReadingDetail(sender: notice["who"] as? String ?? "", body: body, kind: kind)
        self.choices = notice["choices"] as? [String] ?? []
        render()
    }
    func render(error: Bool = false) {
        window?.close()
        let label = state.replacingOccurrences(of: "_", with: " ")
        var body = pending ? detail.body : "This message is \(label).\n\n" + detail.body
        if error { body = "The board is restarting or changed. Answer on the Dibs board.\n\n" + body }
        let reading = AskReadingDetail(sender: detail.sender, body: body, kind: detail.kind)
        let prompt = HumanPromptWindow(title: title, detail: reading, buttons: ["Close"],
                                       replyKind: pending ? detail.kind ?? "" : "", choices: choices) { [weak self] reply in self?.submit(reply) }
        window = prompt
        if pending, let reply = refusedReply {
            prompt.composer.insertText(reply.text, replacementRange: NSRange(location: 0, length: 0))
            if let index = choices.firstIndex(of: reply.choice) { prompt.radios[index].performClick(nil) }
            prompt.hint.stringValue = "Not sent · use the Dibs board"
            prompt.hint.setAccessibilityHelp(refusalHelp)
        }
        if error { prompt.hint.stringValue = "Use the Dibs board" }
        #if DIBS_BROKER_FIXTURE
        if brokerLaunch { nativeHumanFixtureEvent?("native_reading", id, prompt.viewInfo); return }
        #endif
        #if DIBS_READING_FIXTURE
        if nativeHumanFixtureRender(self, prompt) { return }
        #endif
        prompt.show()
    }
    func submit(_ reply: HumanPromptReply) {
        guard !reply.action.isEmpty, reply.action != "Close", pending else { close(); return }
        guard confirmed else { window?.finished = false; refresh(); return } // show current authoritative text before sending
        window?.actionButtons.forEach { $0.isEnabled = false }
        let payload: [String: Any] = ["node": node, "serial": serial, "action": reply.action,
                                      "text": reply.text, "choice": reply.choice, "submission": submission]
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                let value = try nativeHumanRPC(profile: self.profile, method: "answer", payload: payload)
                DispatchQueue.main.async {
                    if value["ok"] as? Bool == true {
                        #if DIBS_READING_FIXTURE
                        nativeHumanFixtureSent(self.id)
                        #endif
                        self.close()
                    } else {
                        self.refusedReply = reply
                        self.refusalHelp = (value["error"] as? String ?? "Reply refused") + ". " + (value["hint"] as? String ?? "Use the Dibs board")
                        self.window?.finished = false; self.refresh()
                    }
                }
            } catch {
                let changed = (error as? NativeHumanFailure) == .boardChanged
                DispatchQueue.main.async {
                    self.window?.finished = false; self.window?.sent = false
                    if changed {
                        self.confirmed = false; self.refusedReply = reply
                        self.render(error: true)
                        #if DIBS_READING_FIXTURE
                        nativeHumanFixtureFailed(self.id)
                        #endif
                        return
                    }
                    self.window?.hint.stringValue = "Not confirmed · try again"
                    self.window?.updateEnabled()
                    #if DIBS_READING_FIXTURE
                    nativeHumanFixtureFailed(self.id)
                    #endif
                }
            }
        }
    }
    func close() {
        window?.close(); nativeHumanWindows.removeValue(forKey: id)
        if coldLaunch && nativeHumanWindows.isEmpty { exit(0) }
    }
}

#if DIBS_BROKER_FIXTURE || DIBS_READING_FIXTURE
var nativeHumanFixtureEvent: ((String, String, [String: Any]) -> Void)? {
    get { NativeHumanStorage.fixtureEvent }
    set { NativeHumanStorage.fixtureEvent = newValue }
}
#endif

#if DIBS_READING_FIXTURE
func nativeHumanFixtureRender(_ owner: NativeHumanController, _ window: HumanPromptWindow) -> Bool {
    guard let path = ProcessInfo.processInfo.environment["DIBS_NATIVE_REOPEN_VIEW"] else { return false }
    window.panel.appearance = NSAppearance(named: ProcessInfo.processInfo.environment["DIBS_NATIVE_REOPEN_APPEARANCE"] == "light" ? .aqua : .darkAqua)
    window.panel.effectiveAppearance.performAsCurrentDrawingAppearance { window.layout(fit: true) }
    var view = window.viewInfo; view["state"] = owner.state; view["confirmed"] = owner.confirmed
    try! JSONSerialization.data(withJSONObject: view).write(to: URL(fileURLWithPath: path))
    if let path = ProcessInfo.processInfo.environment["DIBS_NATIVE_REOPEN_SCREENSHOT"] {
        window.panel.effectiveAppearance.performAsCurrentDrawingAppearance {
            window.layout(fit: true); window.root.displayIfNeeded()
            if let rep = window.root.bitmapImageRepForCachingDisplay(in: window.root.bounds) {
                window.root.cacheDisplay(in: window.root.bounds, to: rep)
                try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: path))
            }
        }
    }
    guard owner.confirmed else { nativeHumanFixtureFailed(owner.id); return true }
    guard owner.pending, let action = ProcessInfo.processInfo.environment["DIBS_NATIVE_REOPEN_ACTION"], !action.isEmpty else {
        if !brokerLaunch { exit(0) }; return true
    }
    if let text = ProcessInfo.processInfo.environment["DIBS_NATIVE_REOPEN_TEXT"] { window.composer.insertText(text, replacementRange: NSRange(location: 0, length: 0)) }
    if let raw = ProcessInfo.processInfo.environment["DIBS_NATIVE_REOPEN_CHOICE"], let index = Int(raw), window.radios.indices.contains(index) { window.radios[index].performClick(nil) }
    guard let button = window.actionButtons.first(where: { $0.title == action }), button.isEnabled else { nativeHumanFixtureFailed(owner.id); return true }
    button.performClick(nil)
    return true
}
func nativeHumanFixtureSent(_ id: String) {
    nativeHumanFixtureEvent?("native_sent", id, [:])
    if !brokerLaunch { exit(0) }
}
func nativeHumanFixtureFailed(_ id: String) {
    nativeHumanFixtureEvent?("native_failed", id, [:])
    if !brokerLaunch { exit(2) }
}
#endif
