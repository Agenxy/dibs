// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// dibs-notify: the process that speaks to the PERSON.
//
// It lives inside an application bundle, and that is the whole reason it
// exists as a separate binary rather than a few lines of osascript in the
// daemon.
//
// A notification has an identity: whoever posts it lends it their name and
// their icon. A daemon shelling out to osascript borrows Script Editor's, so
// every message from an agent arrived branded "osascript" with osascript's
// icon, which is what the operator saw and correctly called out. There is no
// flag that changes that; the poster's bundle IS the identity.
//
// A bundle buys the other half too. UNUserNotificationCenter needs a bundle
// identifier and crashes without one, and it is the only API that carries
// ACTION BUTTONS on the banner itself. So "make it look like Dibs" and "let the
// human answer without opening anything" turn out to be the same change.
//
// Authorisation is requested once and remembered against the bundle id and its
// signature. An ad-hoc signature changes on every build, so a rebuilt Dibs asks
// again: the same trade `tools/signcheck` describes for the Touch ID grant, and
// the same fix, which is an identity of Dibs' own.
//
// Exit codes are the API, as with the presence helper:
//
//	0  posted, or the human chose something (the choice is printed on stdout)
//	1  the human dismissed it without choosing
//	2  this machine will not let us notify (authorisation refused, no bundle)
//
// `--status` answers the same question WITHOUT posting anything, printing one
// word and exiting 0 if notifications would be shown and 2 if they would not.
// It exists because 1 and 2 were indistinguishable to the caller, which made a
// silenced Dibs look exactly like a person ignoring it: a request that could not
// be delivered sat waiting out its deadline while the board said "delivered",
// and the operator asked why they never saw anything. Checking has to be
// possible without raising a banner, or the check is itself an interruption.
import IOKit
import CoreGraphics
import AppKit
import Foundation
import UserNotifications
import Intents
import ServiceManagement
import Darwin

// No UI, notification authorization or client-selected executable participates.
if CommandLine.arguments == [CommandLine.arguments[0], "--verify-notification-peer"] {
    exit(verifyNativeNotificationPeer())
}
if CommandLine.arguments == [CommandLine.arguments[0], "--verify-notification-board"] {
    exit(verifyNativeNotificationPeer(board: true))
}

// These modes belong to the app that owns the background item. They do not
// initialize notification delivery or create a permission/decision window.
let serviceModes = ["--service-status", "--service-register", "--service-unregister"]
if CommandLine.arguments.count == 2, serviceModes.contains(CommandLine.arguments[1]) {
    if #available(macOS 13.0, *) {
        let service = SMAppService.agent(plistName: "org.agenxy.dibs.daemon.plist")
        do {
            switch CommandLine.arguments[1] {
            case "--service-register": if service.status != .enabled { try service.register() }
            case "--service-unregister": if service.status != .notRegistered { try service.unregister() }
            default: break
            }
            let state: String
            switch service.status {
            case .notRegistered: state = "not-registered"
            case .enabled: state = "enabled"
            case .requiresApproval: state = "requires-approval"
            case .notFound: state = "not-found"
            @unknown default: state = "unknown"
            }
            let result: [String: Any] = ["status": state, "approval_required": service.status == .requiresApproval]
            let data = try JSONSerialization.data(withJSONObject: result, options: [.sortedKeys])
            print(String(decoding: data, as: UTF8.self))
            exit(0)
        } catch {
            FileHandle.standardError.write(Data("Dibs app service: \(error)\n".utf8))
            exit(2)
        }
    }
    FileHandle.standardError.write(Data("Dibs app service requires macOS 13 or newer\n".utf8))
    exit(2)
}

func notificationSetting(_ value: UNNotificationSetting) -> String {
    switch value {
    case .enabled: return "enabled"
    case .disabled: return "disabled"
    case .notSupported: return "not-supported"
    @unknown default: return "unknown"
    }
}

func notificationAuthorization(_ value: UNAuthorizationStatus) -> String {
    switch value {
    case .authorized: return "authorized"
    case .denied: return "denied"
    case .notDetermined: return "not-determined"
    case .provisional: return "provisional"
    @unknown default: return "unknown"
    }
}

func notificationStyle(_ value: UNAlertStyle) -> String {
    switch value {
    case .none: return "none"
    case .banner: return "banner"
    case .alert: return "alert"
    @unknown default: return "unknown"
    }
}

func focusObservation() -> [String: Any] {
    let centre = INFocusStatusCenter.default
    let authorization: String
    switch centre.authorizationStatus {
    case .authorized: authorization = "authorized"
    case .denied: authorization = "denied"
    case .notDetermined: authorization = "not-determined"
    case .restricted: authorization = "restricted"
    @unknown default: authorization = "unknown"
    }
    var result: [String: Any] = ["authorization": authorization, "observable": false]
    // Never request access. Not-determined, denied and nil are UNKNOWN,
    // rather than an invented observation that Focus is off.
    if centre.authorizationStatus == .authorized,
       let focused = centre.focusStatus.isFocused {
        result["observable"] = true
        result["is_focused"] = focused
    }
    return result
}

func notificationSettings(_ s: UNNotificationSettings) -> [String: Any] {
    [
        "version": 1,
        "authorization_status": notificationAuthorization(s.authorizationStatus),
        "alert_style": notificationStyle(s.alertStyle),
        "alert_setting": notificationSetting(s.alertSetting),
        "notification_center_setting": notificationSetting(s.notificationCenterSetting),
        "lock_screen_setting": notificationSetting(s.lockScreenSetting),
        "time_sensitive_setting": notificationSetting(s.timeSensitiveSetting),
        "focus": focusObservation()
    ]
}

var receiptSettings: [String: Any]?
var receiptInterruptionLevel = "unknown"

// Additive receipt protocol. Older callers supply no path; older helpers ignore
// it. OS acceptance is not evidence that a banner was visible under Focus.
func receipt(_ state: String) {
    guard let path = ProcessInfo.processInfo.environment["DIBS_NOTIFY_RECEIPT"] else { return }
    var payload: [String: Any] = ["state": state, "interruption_level": receiptInterruptionLevel]
    if let settings = receiptSettings { payload["settings"] = settings }
    else { payload["settings"] = NSNull() }
    guard let data = try? JSONSerialization.data(withJSONObject: payload) else { return }
    try? data.write(to: URL(fileURLWithPath: path), options: .atomic)
    try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: path)
}

let args = Array(CommandLine.arguments.dropFirst())
#if DIBS_BROKER_FIXTURE
if args == ["--broker-fixture-capability"] { print("broker-fixture-v1"); exit(0) }
#endif
let brokerLaunch = args == ["--broker"]
if args == ["--broker-status"] {
    let value = brokerLockStatus()
    print(String(decoding: try! JSONSerialization.data(withJSONObject: value), as: UTF8.self))
    exit(0)
}

#if DIBS_ASK_FIXTURE || DIBS_READING_FIXTURE
// Compile-time transport double: the actual ask input, posting callback and
// expiry run, but no permission request, native notification or window opens.
let askFixture = ProcessInfo.processInfo.environment["DIBS_ASK_FIXTURE"] == "timeout" || ProcessInfo.processInfo.environment["DIBS_READING_FIXTURE"] == "1"
if args == ["--ask-fixture-capability"] { print("ask-fixture-v1"); exit(0) }
#if DIBS_READING_FIXTURE
if args == ["--reading-fixture-capability"] { print("reading-fixture-v1"); exit(0) }
#endif
#else
let askFixture = false
#endif

// One policy for action prompts and explicitly high-priority passive alerts.
// The additive environment key is harmless to a dormant older helper; a new
// caller can distinguish its absence in the posting receipt.
func requestedInterruptionLevel(buttons: [String], environment: [String: String]) -> UNNotificationInterruptionLevel {
    if !buttons.isEmpty || environment["DIBS_NOTIFY_TIME_SENSITIVE"] == "1" {
        return .timeSensitive
    }
    return .active
}

#if DIBS_PRIORITY_FIXTURE
if args == ["--priority-fixture"] {
    let content = UNMutableNotificationContent()
    content.interruptionLevel = requestedInterruptionLevel(buttons: [], environment: ProcessInfo.processInfo.environment)
    print(content.interruptionLevel == .timeSensitive ? "timeSensitive" : "active")
    exit(0)
}
#endif

// Capability negotiation must precede every other status extension, including
// one inherited from the caller's environment. It never touches notification
// settings, asks permission, or creates an application window.
if args == ["--status"], ProcessInfo.processInfo.environment["DIBS_NOTIFY_PROMPT_V1"] == "1" {
    printDesk(["reply": 1, "fonts": PromptFonts.families, "bundledFonts": PromptFonts.urls.count]); exit(0)
}
if args == ["--status"], ProcessInfo.processInfo.environment["DIBS_BACKGROUND_OPEN_V1"] == "1" {
    printDesk(["background_open": 1])
    exit(0)
}

// Existing mode plus an additive environment capability. An old helper ignores
// the environment and returns its old word; callers retain UNKNOWN metadata.
if args == ["--status"], ProcessInfo.processInfo.environment["DIBS_NOTIFY_SETTINGS_V1"] == "1" {
    let app = NSApplication.shared
    app.setActivationPolicy(.accessory)
    UNUserNotificationCenter.current().getNotificationSettings { s in
        guard let data = try? JSONSerialization.data(withJSONObject: notificationSettings(s), options: [.sortedKeys]),
              let text = String(data: data, encoding: .utf8) else { exit(2) }
        print(text)
        let authorized = s.authorizationStatus == .authorized || s.authorizationStatus == .provisional
        exit(authorized && s.alertSetting == .enabled ? 0 : 2)
    }
    DispatchQueue.main.asyncAfter(deadline: .now() + 10) { exit(2) }
    app.run()
}

func validMessageID(_ id: String) -> Bool {
    id.range(of: #"^dibs\.msg\.[A-Za-z0-9-]{1,128}\.[1-9][0-9]{0,19}$"#,
             options: .regularExpression) != nil
}

if args.count == 1, args[0].hasPrefix("--message-delivered=") {
    let id = String(args[0].dropFirst("--message-delivered=".count))
    guard validMessageID(id) else { exit(2) }
    let app = NSApplication.shared
    app.setActivationPolicy(.accessory)
    UNUserNotificationCenter.current().getDeliveredNotifications { notices in
        let present = notices.contains { $0.request.identifier == id }
        guard let data = try? JSONSerialization.data(withJSONObject: ["present": present]),
              let text = String(data: data, encoding: .utf8) else { exit(2) }
        print(text)
        exit(0)
    }
    DispatchQueue.main.asyncAfter(deadline: .now() + 5) { exit(2) }
    app.run()
}

if args.count == 1, args[0].hasPrefix("--remove-messages=") {
    let ids = String(args[0].dropFirst("--remove-messages=".count)).components(separatedBy: ",")
    guard !ids.isEmpty, ids.count <= 64, ids.allSatisfy(validMessageID) else { exit(2) }
    // Asynchronous, with no completion callback: this confirms the request,
    // never that a person did not see the notification or that it is absent.
    let app = NSApplication.shared
    app.setActivationPolicy(.accessory)
    UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: ids)
    DispatchQueue.main.asyncAfter(deadline: .now() + 0.25) {
        print(#"{"cleanup":"requested"}"#)
        exit(0)
    }
    app.run()
}

// These modes use the already signed helper without asking for notification
// permission or drawing UI. An older helper refuses them; callers must not
// fall back to an activating open when they are unavailable.
func hidIdleSeconds() -> Double? {
    let service = IOServiceGetMatchingService(kIOMainPortDefault, IOServiceMatching("IOHIDSystem"))
    guard service != 0 else { return nil }
    defer { IOObjectRelease(service) }
    guard let value = IORegistryEntryCreateCFProperty(service, "HIDIdleTime" as CFString,
                                                     kCFAllocatorDefault, 0)?.takeRetainedValue() as? NSNumber
        else { return nil }
    let seconds = value.doubleValue / 1_000_000_000
    return seconds.isFinite && seconds >= 0 ? seconds : nil
}

func deskState() -> [String: Any] {
    let session = CGSessionCopyCurrentDictionary() as? [String: Any]
    var count: UInt32 = 0
    let status = CGGetOnlineDisplayList(0, nil, &count)
    var displays = [CGDirectDisplayID](repeating: 0, count: Int(count))
    let listed = CGGetOnlineDisplayList(count, &displays, &count)
    let idle = hidIdleSeconds()
    return [
        "idle_known": idle != nil,
        "idle_seconds": idle ?? 0,
        "session_known": session != nil,
        "locked": session?["CGSSessionScreenIsLocked"] as? Bool ?? false,
        "display_known": status == .success && listed == .success && count > 0,
        "displays_asleep": !displays.isEmpty && displays.allSatisfy { CGDisplayIsAsleep($0) != 0 },
        "frontmost_pid": NSWorkspace.shared.frontmostApplication?.processIdentifier ?? 0
    ]
}

func isAway(_ state: [String: Any], minIdle: Double) -> Bool {
    return (state["session_known"] as? Bool == true && state["locked"] as? Bool == true)
        || (state["display_known"] as? Bool == true && state["displays_asleep"] as? Bool == true)
        || (state["idle_known"] as? Bool == true && (state["idle_seconds"] as? Double ?? -1) >= minIdle)
}

func printDesk(_ state: [String: Any]) {
    guard let data = try? JSONSerialization.data(withJSONObject: state, options: [.sortedKeys]),
          let text = String(data: data, encoding: .utf8) else { exit(2) }
    print(text)
}

// Background-open decisions and their native contacts share this one path.
// The compile-time fixture factory is absent from the shipped binary; tests
// drive the real mode with fake OS observations rather than opening an app.
struct BackgroundApp: Codable, Equatable, Sendable {
    let pid: Int32
    let launch: String
    let bundle: String
}

struct BackgroundObservation: Codable {
    let app: BackgroundApp?
    let counter: UInt32?
    let idle: Double?
    let interveningActivation: Bool
}

struct BackgroundOpenContact {
    let target: BackgroundApp?
    let observe: () -> BackgroundObservation
    let elapsed: () -> Double
    let wait: () -> Void
    let open: (URL) -> Bool
    let live: (BackgroundApp) -> Bool
    let restore: (BackgroundApp) -> Bool
    let cleanup: () -> Void
}

func backgroundOpen(_ url: URL, contact: BackgroundOpenContact) -> [String: Any] {
    defer { contact.cleanup() }
    let before = contact.observe()
    let start = contact.elapsed()
    var result: [String: Any] = ["version": 1, "opened": false,
        "restore_attempted": false, "restore_accepted": false, "reason": "open-failed",
        "previous_pid": before.app?.pid ?? 0, "frontmost_pid": before.app?.pid ?? 0]
    guard contact.open(url) else { return result }
    result["opened"] = true
    guard let previous = before.app else { result["reason"] = "previous-app-unknown"; return result }
    guard previous.bundle != "com.openai.codex" else {
        result["reason"] = "already-foreground"; return result
    }
    guard let target = contact.target else { result["reason"] = "target-incarnation-unknown"; return result }
    // Input counters omit auto-repeat (Apple's documented contract). Require
    // idle evidence too; activity already in progress must win over restoration.
    guard let counter = before.counter, let idle = before.idle, idle.isFinite, idle >= 0.25 else {
        result["reason"] = "input-unknown-or-active"; return result
    }
    while contact.elapsed() - start <= 0.25 {
        let now = contact.observe()
        result["frontmost_pid"] = now.app?.pid ?? 0
        guard let current = now.app, let currentCounter = now.counter,
              let currentIdle = now.idle, currentIdle.isFinite, currentIdle >= 0 else {
            result["reason"] = "observation-unknown"; return result
        }
        guard currentCounter == counter,
              currentIdle + 0.01 >= idle + (contact.elapsed() - start) else {
            result["reason"] = "user-input"; return result
        }
        guard !now.interveningActivation else {
            result["reason"] = "user-app-change"; return result
        }
        guard contact.live(previous) else { result["reason"] = "previous-app-gone"; return result }
        if current == previous { contact.wait(); continue }
        guard current == target else {
            result["reason"] = "user-app-change"; return result
        }
        // Recheck input and incarnation immediately at the restoration boundary.
        let final = contact.observe()
        guard final.app == current, final.counter == counter,
              let finalIdle = final.idle, finalIdle.isFinite,
              finalIdle + 0.01 >= idle + (contact.elapsed() - start),
              !final.interveningActivation, contact.live(previous) else {
            result["reason"] = "restore-boundary-changed"; return result
        }
        result["restore_attempted"] = true
        let restored = contact.restore(previous)
        result["restore_accepted"] = restored
        result["reason"] = restored ? "restore-accepted" : "restore-refused"
        result["frontmost_pid"] = contact.observe().app?.pid ?? 0
        return result // exactly one attempt, even when activation is refused
    }
    result["reason"] = "activation-timeout"
    return result
}

func backgroundIdentity(_ app: NSRunningApplication?) -> BackgroundApp? {
    guard let app, !app.isTerminated, let launched = app.launchDate,
          let bundle = app.bundleIdentifier else { return nil }
    return BackgroundApp(pid: app.processIdentifier,
        launch: String(launched.timeIntervalSince1970), bundle: bundle)
}

// Workspace notifications may be delivered on another thread. Preserve a
// single cancellation bit with a lock; it is never reset during an open.
final class BackgroundActivation: @unchecked Sendable {
    private let lock = NSLock()
    private var changed = false
    func cancel() { lock.lock(); changed = true; lock.unlock() }
    func cancelled() -> Bool { lock.lock(); defer { lock.unlock() }; return changed }
}

func nativeBackgroundContact() -> BackgroundOpenContact {
    let workspace = NSWorkspace.shared
    let previous = backgroundIdentity(workspace.frontmostApplication)
    let target = backgroundIdentity(NSRunningApplication.runningApplications(withBundleIdentifier: "com.openai.codex").first)
    let activation = BackgroundActivation()
    let observer = workspace.notificationCenter.addObserver(
        forName: NSWorkspace.didActivateApplicationNotification, object: nil, queue: nil) { notification in
        let activated = backgroundIdentity(notification.userInfo?[NSWorkspace.applicationUserInfoKey] as? NSRunningApplication)
        if activated == nil || (activated != previous && activated?.bundle != "com.openai.codex") {
            activation.cancel()
        }
    }
    return BackgroundOpenContact(target: target, observe: {
        // kCGAnyInputEventType is (~0), from Apple's CGEventTypes.h. This
        // observes counts, never key contents, and requests no event-tap access.
        let anyInput = CGEventType(rawValue: UInt32.max)
        let session = CGSessionCopyCurrentDictionary() as? [String: Any]
        let counter: UInt32? = anyInput.flatMap { type in
            session == nil ? nil : CGEventSource.counterForEventType(.combinedSessionState, eventType: type)
        }
        return BackgroundObservation(app: backgroundIdentity(workspace.frontmostApplication),
            counter: counter, idle: hidIdleSeconds(), interveningActivation: activation.cancelled())
    }, elapsed: { ProcessInfo.processInfo.systemUptime }, wait: {
        RunLoop.current.run(until: Date(timeIntervalSinceNow: 0.01))
    }, open: { workspace.open($0) }, live: { identity in
        backgroundIdentity(NSRunningApplication(processIdentifier: identity.pid)) == identity
    }, restore: { identity in
        guard let app = NSRunningApplication(processIdentifier: identity.pid),
              backgroundIdentity(app) == identity else { return false }
        return app.activate(options: [])
    }, cleanup: { workspace.notificationCenter.removeObserver(observer) })
}

#if DIBS_BACKGROUND_OPEN_FIXTURE
// Compiled only by hosted regression tests. The normal mode dispatcher and
// decision above stay identical; every contact with the desktop is replaced.
struct BackgroundFixture: Decodable {
    let target: BackgroundApp?
    let observations: [BackgroundObservation]
    let openSuccess: Bool
    let previousLive: Bool
    let restoreSuccess: Bool
}
var backgroundFixtureRestoreCount = 0
var backgroundFixtureOpenCount = 0
func fixtureBackgroundContact() -> BackgroundOpenContact? {
    guard let raw = ProcessInfo.processInfo.environment["DIBS_BACKGROUND_OPEN_FIXTURE"],
          let data = raw.data(using: .utf8),
          let fixture = try? JSONDecoder().decode(BackgroundFixture.self, from: data),
          !fixture.observations.isEmpty else { return nil }
    var index = 0
    var time = 0.0
    return BackgroundOpenContact(target: fixture.target, observe: {
        let value = fixture.observations[min(index, fixture.observations.count - 1)]
        index += 1
        return BackgroundObservation(app: value.app, counter: value.counter,
            idle: value.idle.map { $0 + time }, interveningActivation: value.interveningActivation)
    }, elapsed: { time }, wait: { time += 0.01 }, open: { _ in
        backgroundFixtureOpenCount += 1
        return fixture.openSuccess
    }, live: { _ in fixture.previousLive }, restore: { _ in
        backgroundFixtureRestoreCount += 1
        return fixture.restoreSuccess
    }, cleanup: {})
}
#endif

if args.first == "--open-background" {
    guard args.count == 2, let url = URL(string: args[1]),
          args[1].range(of: #"^codex://threads/[A-Za-z0-9-]{1,128}$"#,
                        options: .regularExpression) != nil else { exit(2) }
    let contact: BackgroundOpenContact
    #if DIBS_BACKGROUND_OPEN_FIXTURE
    guard let fixture = fixtureBackgroundContact() else { exit(2) }
    contact = fixture
    #else
    guard ProcessInfo.processInfo.environment["DIBS_TEST_FORBID_APP_OPEN"] != "1" else { exit(2) }
    contact = nativeBackgroundContact()
    #endif
    var result = backgroundOpen(url, contact: contact)
    #if DIBS_BACKGROUND_OPEN_FIXTURE
    result["fixture_restore_count"] = backgroundFixtureRestoreCount
    result["fixture_open_count"] = backgroundFixtureOpenCount
    #endif
    printDesk(result)
    exit(result["opened"] as? Bool == true ? 0 : 2)
}

if args.first == "--desk-state" {
    printDesk(deskState())
    exit(0)
}

if args.first == "--open-away" {
    guard args.count == 3,
          let minIdle = Double(args[2]), minIdle.isFinite, minIdle >= 0,
          let url = URL(string: args[1]),
          args[1].range(of: #"^(codex://threads/[A-Za-z0-9-]{1,128}|claude://code/continue\?session=local_[A-Za-z0-9-]{1,128})$"#,
                       options: .regularExpression) != nil else { exit(2) }
    guard isAway(deskState(), minIdle: minIdle) else { exit(3) }
    let previous = NSWorkspace.shared.frontmostApplication
    // Check again at the actual open boundary. The waiter cannot authorize an
    // open after the person returned between its probe and this invocation.
    guard isAway(deskState(), minIdle: minIdle) else { exit(3) }
    let opened = NSWorkspace.shared.open(url)
    guard opened else { exit(2) }
    // URL handling may activate the recipient more than once while loading.
    // Restore only while away: once present, choosing a frontmost app is theirs.
    var restored = false
    for _ in 0..<(previous == nil ? 0 : 24) {
        Thread.sleep(forTimeInterval: 0.25)
        guard isAway(deskState(), minIdle: minIdle) else { break }
        if NSWorkspace.shared.frontmostApplication?.processIdentifier != previous?.processIdentifier {
            restored = previous?.activate(options: []) ?? false
        }
    }
    printDesk(["opened": true, "restored": restored,
               "previous_pid": previous?.processIdentifier ?? 0,
               "frontmost_pid": NSWorkspace.shared.frontmostApplication?.processIdentifier ?? 0])
    exit(0)
}

if args.first == "--status" {
    let centre = UNUserNotificationCenter.current()
    let done = DispatchSemaphore(value: 0)
    var word = "unknown"
    var code: Int32 = 2
    centre.getNotificationSettings { s in
        switch s.authorizationStatus {
        case .authorized, .provisional: word = "authorized"; code = 0
        case .denied: word = "denied"
        case .notDetermined: word = "not-determined"
        @unknown default: word = "unknown"
        }
        // Authorised and yet unable to show anything is a real state: an app can
        // hold permission with every presentation style switched off, and then
        // nothing appears while every API reports success.
        if code == 0 && s.alertSetting == .disabled {
            word = "alerts-off"
            code = 2
        }
        done.signal()
    }
    // Safe here, unlike in the posting path: getNotificationSettings answers on
    // its own queue and there is no delegate callback waiting on the main one.
    _ = done.wait(timeout: .now() + 10)
    print(word)
    exit(code)
}

// --prompt and --pick: the SECOND half of answering, and it used to be
// somewhere else entirely.
//
// The notification comes from this bundle, because only UNUserNotificationCenter
// carries buttons and only a bundle carries an identity. But the text box that
// opens when somebody presses "Answer…" was an osascript `display dialog`, and a
// background LaunchAgent has no foreground application for a dialog to belong
// to. So the notification dismissed itself on the press, osascript ran, nothing
// appeared, and the operator reported exactly that: "when I clicked answer it
// just went away, there was nowhere to put an answer."
//
// Native, and activated, so it comes to the front of whatever they were doing.
// Stealing focus is correct HERE and nowhere else in this file: they pressed a
// button asking for it, one gesture ago.
func askOnScreen(_ heading: String, _ detail: String, choices: [String]) -> Int32 {
    let app = NSApplication.shared
    app.setActivationPolicy(.accessory)
    var result = HumanPromptReply()
    let window = HumanPromptWindow(title: heading, detail: AskReadingDetail(sender: "", body: detail, kind: "question"),
                                   buttons: [], replyKind: "question", choices: choices) { reply in
        result = reply; NSApp.stopModal()
    }
    window.show(); app.runModal(for: window.panel); window.close()
    var text = result.choice
    if !result.text.isEmpty { text += (text.isEmpty ? "" : "\n\n") + result.text }
    guard !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return 1 }
    print(text); return 0
}

// --delivered: post one notification, then ask macOS what it actually holds.
//
// Diagnosis, because "posted" and "visible" turned out to be different things
// and nothing here could tell them apart. UNUserNotificationCenter accepts a
// request and reports no error whether the banner is shown, silenced by a Focus
// mode, or dropped for a reason it does not surface. getDeliveredNotifications
// answers the only question that matters afterwards: is it in Notification
// Centre, where a person could still find it, or nowhere at all.
if args.first == "--delivered" {
    let centre = UNUserNotificationCenter.current()
    let app = NSApplication.shared
    app.setActivationPolicy(.accessory)

    centre.requestAuthorization(options: [.alert, .sound]) { granted, err in
        guard granted else {
            FileHandle.standardError.write(Data("authorisation refused: \(err?.localizedDescription ?? "")\n".utf8))
            exit(2)
        }
        let c = UNMutableNotificationContent()
        c.title = "Dibs · delivery probe"
        c.body = "If you can see this, notifications are reaching the screen."
        c.interruptionLevel = .timeSensitive
        let id = "dibs-probe"
        centre.add(UNNotificationRequest(identifier: id, content: c, trigger: nil)) { addErr in
            if let addErr { print("add error: \(addErr.localizedDescription)") }
            DispatchQueue.main.asyncAfter(deadline: .now() + 2) {
                centre.getDeliveredNotifications { delivered in
                    print("delivered notifications held by macOS: \(delivered.count)")
                    for n in delivered {
                        print("  - \(n.request.identifier): \(n.request.content.title)")
                    }
                    centre.getNotificationSettings { st in
                        print("authorization=\(st.authorizationStatus.rawValue) alert=\(st.alertSetting.rawValue) " +
                              "notificationCentre=\(st.notificationCenterSetting.rawValue) " +
                              "lockScreen=\(st.lockScreenSetting.rawValue) " +
                              "timeSensitive=\(st.timeSensitiveSetting.rawValue)")
                        exit(delivered.isEmpty ? 3 : 0)
                    }
                }
            }
        }
    }
    app.run()
}

// --settings: the notification settings as one line, posting nothing.
//
// `--delivered` answers the same question by posting a probe, which is exactly
// what a diagnostic must not do when the caller is deciding how to deliver a
// real message.
if args.first == "--settings" {
    let centre = UNUserNotificationCenter.current()
    let done = DispatchSemaphore(value: 0)
    centre.getNotificationSettings { st in
        print("authorization=\(st.authorizationStatus.rawValue) alert=\(st.alertSetting.rawValue) " +
              "timeSensitive=\(st.timeSensitiveSetting.rawValue)")
        done.signal()
    }
    _ = done.wait(timeout: .now() + 10)
    exit(0)
}

if args.first == "--prompt" || args.first == "--pick" {
    let rest = Array(args.dropFirst())
    guard rest.count >= 2 else {
        FileHandle.standardError.write("usage: dibs-notify --prompt|--pick <title> <body> [choice…]\n".data(using: .utf8)!)
        exit(2)
    }
    exit(askOnScreen(rest[0], rest[1], choices: Array(rest.dropFirst(2))))
}

guard args.isEmpty || brokerLaunch || args.count >= 3 else {
    FileHandle.standardError.write("usage: dibs-notify <title> <subtitle> <body> [button…]\n".data(using: .utf8)!)
    exit(2)
}
// LaunchServices opens the bundle executable without the posting arguments.
// Do not post or request authorization on that launch: install the delegate
// before app.run finishes launching and wait for its notification response.
let coldLaunch = args.isEmpty
let title = coldLaunch || brokerLaunch ? "Dibs" : args[0]
let subtitle = coldLaunch || brokerLaunch ? "" : args[1]
let body = coldLaunch || brokerLaunch ? "" : args[2]
let buttons = Array(args.dropFirst(3))

struct AskReadingDetail: Codable {
    let sender: String
    let body: String
    let kind: String?
}

// Persist text already shown to this human and a plain local profile route,
// never executable links, credentials or an answer capability. macOS stores
// this in its notification database, with the banner's privacy boundary.
struct StoredReadingDetail: Codable {
    var title: String
    var sender: String
    var body: String
    let kind: String
    var profile: String? = nil
}
let readingKey = "org.agenxy.dibs.reading.v1"
let fullReadingKey = "org.agenxy.dibs.reading.v2"

func storedReading(_ heading: String, detail: AskReadingDetail) -> Data {
    var value = StoredReadingDetail(title: heading, sender: detail.sender, body: detail.body,
                                    kind: String((detail.kind ?? "ask").prefix(32)))
    let rest = "\n\nRest on the Dibs board."
    var text = value.body
    while true {
        let data = try! JSONEncoder().encode(value) // strings only
        if data.count <= 8192 { return data }
        if !text.isEmpty {
            text = String(text.dropLast(max(1, text.count / 8)))
            value.body = text + rest
        } else if value.title.count > 64 {
            value.title = String(value.title.prefix(value.title.count / 2))
        } else {
            value.sender = String(value.sender.prefix(value.sender.count / 2))
        }
    }
}

func persistedReading(_ content: UNNotificationContent) -> StoredReadingDetail? {
    if let bytes = content.userInfo[fullReadingKey] as? Data, bytes.count <= 1048576 {
        return try? JSONDecoder().decode(StoredReadingDetail.self, from: bytes)
    }
    guard let bytes = content.userInfo[readingKey] as? Data, bytes.count <= 8192 else { return nil }
    return try? JSONDecoder().decode(StoredReadingDetail.self, from: bytes)
}

// Only the additive detail protocol reads stdin. Diagnostics and old callers
// keep their old argument and lifetime contracts.
func readAskDetail(_ environment: [String: String]) -> AskReadingDetail? {
    guard environment["DIBS_NOTIFY_DETAIL"] == "stdin" else { return nil }
    var bytes = Data()
    do {
        while let part = try FileHandle.standardInput.read(upToCount: 8193 - bytes.count), !part.isEmpty {
            bytes.append(part)
            guard bytes.count <= 8192 else { throw CocoaError(.fileReadTooLarge) }
        }
        return try JSONDecoder().decode(AskReadingDetail.self, from: bytes)
    } catch {
        FileHandle.standardError.write(Data("Dibs ask detail was invalid or exceeded 8 KiB\n".utf8))
        exit(2)
    }
}

func askLifetime(_ environment: [String: String], now: Date) -> TimeInterval? {
    guard environment["DIBS_NOTIFY_DETAIL"] == "stdin" else { return 120 }
    guard let raw = environment["DIBS_NOTIFY_UNTIL"], let until = Double(raw), until.isFinite else { return nil }
    return max(0, min(24 * 60 * 60, until - now.timeIntervalSince1970))
}

final class AskReadingClose: NSObject, NSWindowDelegate {
    func windowShouldClose(_ sender: NSWindow) -> Bool {
        NSApp.abortModal()
        return true
    }
}

// Called only by the banner's default action: the person asked to read it.
// The daemon's effect title and sender stay outside the sender-written text.
func readAskOnScreen(_ heading: String, detail: AskReadingDetail, buttons: [String], storedBytes: Int = 0) -> String {
    var result = ""
    let window = HumanPromptWindow(title: heading, detail: detail, buttons: buttons) { reply in
        result = reply.action; NSApp.stopModal()
    }
    #if DIBS_READING_FIXTURE
    if askFixture, let path = ProcessInfo.processInfo.environment["DIBS_READING_WINDOW"] {
        let view: [String: Any] = ["Title": heading, "Sender": detail.sender, "Body": detail.body, "RenderedBody": window.message.string,
                                 "Buttons": window.actionButtons.map { $0.title }, "StoredBytes": storedBytes]
        try! JSONSerialization.data(withJSONObject: view).write(to: URL(fileURLWithPath: path))
        let selected = ProcessInfo.processInfo.environment["DIBS_READING_CHOICE"] ?? ""
        return buttons.contains(selected) ? selected : ""
    }
    #endif
    window.show(); NSApp.runModal(for: window.panel); window.close()
    return result
}

final class BrokerEntry {
    let id: String
    let title: String
    let subtitle: String
    let excerpt: String
    let detail: AskReadingDetail
    let buttons: [String]
    let until: Date
    let timeSensitive: Bool
    var timer: DispatchSourceTimer?
    var beganPosting = false
    var replyKind = ""
    var choices = [String]()
    var profile: String? = nil
    init(id: String, title: String, subtitle: String, excerpt: String, detail: AskReadingDetail,
         buttons: [String], until: Date, timeSensitive: Bool) {
        self.id = id; self.title = title; self.subtitle = subtitle; self.excerpt = excerpt
        self.detail = detail; self.buttons = buttons; self.until = until; self.timeSensitive = timeSensitive
    }
}

func brokerLockPath() -> String? {
    #if DIBS_BROKER_FIXTURE
    if let directory = ProcessInfo.processInfo.environment["DIBS_BROKER_FIXTURE_DIR"] {
        return directory + "/notifier.lock"
    }
    #endif
    // Deliberately independent of DIBS_DIR: a scratch board is still on the
    // same Mac and may not compete for this user's native notification route.
    guard let record = getpwuid(getuid()), let home = record.pointee.pw_dir else { return nil }
    return String(cString: home) + "/.dibs/notifier.lock"
}

func acquireBrokerLock() -> Int32? {
    guard let path = brokerLockPath() else { return nil }
    do {
        try FileManager.default.createDirectory(atPath: (path as NSString).deletingLastPathComponent,
                                               withIntermediateDirectories: true,
                                               attributes: [.posixPermissions: 0o700])
    } catch { return nil }
    let fd = open(path, O_CREAT | O_RDWR | O_NOFOLLOW | O_CLOEXEC, 0o600)
    guard fd >= 0 else { return nil }
    var info = stat()
    guard fstat(fd, &info) == 0, info.st_uid == getuid(), info.st_mode & 0o077 == 0,
          flock(fd, LOCK_EX | LOCK_NB) == 0 else { close(fd); return nil }
    return fd // lifetime lock only; its contents are never read or written
}

// Read-only diagnostic: an existing lock is probed then immediately closed.
// lsof inspects its holder; the file never becomes a PID or command mailbox.
func brokerLockStatus() -> [String: Any] {
    guard let path = brokerLockPath() else { return ["error": "cannot resolve this user's notifier lock"] }
    let fd = open(path, O_RDONLY | O_NOFOLLOW | O_CLOEXEC)
    if fd < 0 {
        if errno == ENOENT { return ["held": false, "pids": [Int]()] }
        return ["error": "cannot inspect Dibs notifier lock"]
    }
    let result = flock(fd, LOCK_EX | LOCK_NB)
    let failure = errno
    close(fd)
    if result == 0 { return ["held": false, "pids": [Int]()] }
    guard failure == EWOULDBLOCK else { return ["error": "cannot inspect Dibs notifier lock"] }
    let process = Process()
    let output = Pipe()
    process.executableURL = URL(fileURLWithPath: "/usr/sbin/lsof")
    process.arguments = ["-F", "p", "--", path]
    process.standardOutput = output
    process.standardError = FileHandle.nullDevice
    do {
        try process.run()
        let data = output.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        let pids = String(decoding: data, as: UTF8.self).split(separator: "\n").compactMap { line -> Int? in
            guard line.first == "p" else { return nil }
            return Int(line.dropFirst())
        }
        return ["held": true, "pids": pids]
    } catch { return ["held": true, "pids": [Int](), "error": "holder PID unavailable"] }
}

final class NativeNotificationBroker: NSObject, UNUserNotificationCenterDelegate {
    let centre: UNUserNotificationCenter
    var entries = [String: BrokerEntry]()
    var windows = [String: HumanPromptWindow]()
    let lockFD: Int32
    var caller: DispatchSourceProcess?
    init?(centre: UNUserNotificationCenter) {
        guard let fd = acquireBrokerLock() else { return nil }
        self.centre = centre; lockFD = fd
        super.init()
    }
    func emit(_ event: String, id: String = "", action: String? = nil,
              receipt: [String: Any]? = nil, error: String? = nil, view: [String: Any]? = nil) {
        var value: [String: Any] = ["v": 1, "event": event, "id": id]
        if event == "ready" { value["reply"] = 1 }
        if let action { value["action"] = action }
        if let receipt { value["receipt"] = receipt }
        if let view { value["view"] = view }
        if let error { value["error"] = error }
        guard let data = try? JSONSerialization.data(withJSONObject: value), data.count < 1048576 else { exit(2) }
        FileHandle.standardOutput.write(data + Data([10]))
    }
    func start() {
        #if DIBS_BROKER_FIXTURE
        nativeHumanFixtureEvent = { [weak self] event, id, view in self?.emit(event, id: id, view: view) }
        if let path = ProcessInfo.processInfo.environment["DIBS_BROKER_PID_FILE"] {
            try! Data(String(getpid()).utf8).write(to: URL(fileURLWithPath: path))
        }
        #endif
        let parent = getppid()
        guard parent > 1 else { exit(1) }
        let source = DispatchSource.makeProcessSource(identifier: parent, eventMask: .exit,
                                                      queue: .global(qos: .utility))
        source.setEventHandler { exit(1) }; source.resume(); caller = source
        if getppid() != parent { exit(1) }
        emit("ready", view: ["reply": 1])
        DispatchQueue.global(qos: .utility).async {
            var buffer = Data()
            while true {
                let part = FileHandle.standardInput.availableData
                if part.isEmpty { exit(0) } // parent left; never leave an answer owner orphaned
                for byte in part {
                    if byte == 10 {
                        let line = buffer; buffer.removeAll(keepingCapacity: true)
                        DispatchQueue.main.sync { self.command(line) }
                    } else {
                        buffer.append(byte)
                        if buffer.count > 1048576 { exit(2) }
                    }
                }
            }
        }
    }
    func command(_ line: Data) {
        guard let value = (try? JSONSerialization.jsonObject(with: line)) as? [String: Any],
              value["v"] as? Int == 1, let type = value["type"] as? String,
              let id = value["id"] as? String, validMessageID(id) else {
            emit("protocol_error", error: "invalid v1 notification command"); return
        }
        if type == "cancel" { remove(id, removeBanner: true); return }
        #if DIBS_BROKER_FIXTURE
        if type == "fixture_reopen", let dir = ProcessInfo.processInfo.environment["DIBS_BROKER_POSTED_DIR"],
           let bytes = try? Data(contentsOf: URL(fileURLWithPath: dir).appendingPathComponent(id + ".plist")),
           let saved = (try? PropertyListSerialization.propertyList(from: bytes, format: nil)) as? [String: Any] {
            let content = UNMutableNotificationContent()
            content.title = saved["title"] as? String ?? "Dibs"
            content.subtitle = saved["subtitle"] as? String ?? ""
            content.body = saved["body"] as? String ?? ""
            content.userInfo = saved["userInfo"] as? [AnyHashable: Any] ?? [:]
            response(id: id, action: UNNotificationDefaultActionIdentifier, content: content); return
        }
        let fixtureWindow = windows[id] ?? nativeHumanWindows[id]?.window
        if type == "fixture_response", let action = value["action"] as? String {
            response(id: id, action: action, content: nil); return
        }
        if type == "fixture_edit", let window = fixtureWindow, let text = value["text"] as? String {
            window.composer.insertText(text, replacementRange: NSRange(location: 0, length: (window.composer.string as NSString).length))
            emit("editing", id: id, view: window.viewInfo); return
        }
        if type == "fixture_select", let window = fixtureWindow, let index = value["index"] as? Int,
           window.radios.indices.contains(index) {
            window.radios[index].performClick(nil); emit("selected", id: id, view: window.viewInfo); return
        }
        if type == "fixture_return", let window = fixtureWindow {
            if let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [], timestamp: 0,
                                           windowNumber: window.panel.windowNumber, context: nil, characters: "\r",
                                           charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36) {
                window.composer.keyDown(with: event)
            }
            if windows[id] != nil { emit("unchanged", id: id, view: window.viewInfo) }
            return
        }
        if type == "fixture_end", let window = fixtureWindow {
            window.revealMessageEnd(); emit("end", id: id, view: window.viewInfo); return
        }
        if type == "fixture_tab_choices", let window = fixtureWindow {
            window.panel.makeFirstResponder(window.message); window.panel.selectNextKeyView(nil)
            emit("focused", id: id, view: window.viewInfo); return
        }
        if type == "fixture_key", let window = fixtureWindow, let code = value["keyCode"] as? Int {
            if value["focus"] as? String == "reply" { window.panel.makeFirstResponder(window.composer) }
            var modifiers: NSEvent.ModifierFlags = []
            for flag in value["modifiers"] as? [String] ?? [] { if flag == "shift" { modifiers.insert(.shift) }; if flag == "option" { modifiers.insert(.option) } }
            if let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: modifiers, timestamp: 0,
                                        windowNumber: window.panel.windowNumber, context: nil,
                                        characters: value["characters"] as? String ?? "", charactersIgnoringModifiers: value["characters"] as? String ?? "",
                                        isARepeat: false, keyCode: UInt16(code)) {
                _ = window.panel.performKeyEquivalent(with: event)
            }
            if windows[id] != nil { emit("keyed", id: id, view: window.viewInfo) }
            return
        }
        if type == "fixture_snapshot", let window = fixtureWindow, let path = value["path"] as? String {
            if let scale = value["scale"] as? Double { window.fixtureScale = scale }
            if let width = value["screenWidth"] as? Double, let height = value["screenHeight"] as? Double { window.fixtureScreen = NSSize(width: width, height: height) }
            if let appearance = value["appearance"] as? String { window.panel.appearance = NSAppearance(named: appearance == "dark" ? .darkAqua : .aqua) }
            window.panel.effectiveAppearance.performAsCurrentDrawingAppearance {
                window.layout(fit: value["preserveScroll"] as? Bool != true)
                window.root.layoutSubtreeIfNeeded(); window.root.displayIfNeeded()
                var bitmap = window.root.bitmapImageRepForCachingDisplay(in: window.root.bounds)
                let rasterScale = value["rasterScale"] as? Double ?? Double(ProcessInfo.processInfo.environment["DIBS_PROMPT_RASTER_SCALE"] ?? "")
                if rasterScale == 1 {
                    bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(window.root.bounds.width), pixelsHigh: Int(window.root.bounds.height), bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .calibratedRGB, bytesPerRow: 0, bitsPerPixel: 0)
                    bitmap?.size = window.root.bounds.size
                }
                if let bitmap {
                    window.root.cacheDisplay(in: window.root.bounds, to: bitmap)
                    try! bitmap.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: path))
                }
            }
            emit("snapshot", id: id, view: window.viewInfo); return
        }
        if type == "fixture_window", let action = value["action"] as? String {
            if let window = fixtureWindow, let button = window.actionButtons.first(where: { $0.title == action }) {
                button.performClick(nil)
            } else { emit("refused", id: id) }
            return
        }
        #endif
        guard type == "post" else { emit("protocol_error", id: id, error: "only post and cancel are accepted"); return }
        guard entries[id] == nil, let title = value["title"] as? String,
              let excerpt = value["excerpt"] as? String,
              let rawDetail = value["detail"],
              let data = try? JSONSerialization.data(withJSONObject: rawDetail, options: [.withoutEscapingSlashes]),
              data.count <= (value["replyKind"] == nil ? 8192 : 524288), let detail = try? JSONDecoder().decode(AskReadingDetail.self, from: data),
              let until = value["until"] as? Double, until.isFinite else {
            emit("failed", id: id, error: "invalid or duplicate notification detail; read the Dibs board"); return
        }
        guard value["buttons"] == nil || value["buttons"] is [String] else {
            emit("failed", id: id, error: "invalid notification button list"); return
        }
        let buttons = value["buttons"] as? [String] ?? []
        guard buttons.count <= 3, Set(buttons).count == buttons.count,
              buttons.allSatisfy({ !$0.isEmpty && $0.utf8.count <= 1024 }) else {
            emit("failed", id: id, error: "notification takes at most three distinct bounded buttons"); return
        }
        guard value["replyKind"] == nil || value["replyKind"] is String,
              value["choices"] == nil || value["choices"] is [String] else {
            emit("failed", id: id, error: "invalid typed prompt fields"); return
        }
        let replyKind = value["replyKind"] as? String ?? ""
        let choices = value["choices"] as? [String] ?? []
        guard replyKind.isEmpty || ((replyKind == "question" || replyKind == "request") && replyKind == detail.kind),
              choices.count <= 4, (replyKind != "request" || choices.isEmpty),
              choices.allSatisfy({ !$0.isEmpty && $0.utf8.count <= 1024 }) else {
            emit("failed", id: id, error: "invalid typed prompt"); return
        }
        let cap: TimeInterval = detail.kind == "contact" ? 120 : 24 * 60 * 60
        let entry = BrokerEntry(id: id, title: title, subtitle: value["subtitle"] as? String ?? "",
                                excerpt: excerpt, detail: detail, buttons: buttons,
                                until: min(Date(timeIntervalSince1970: until), Date().addingTimeInterval(cap)),
                                timeSensitive: value["timeSensitive"] as? Bool == true || !buttons.isEmpty)
        entry.replyKind = replyKind; entry.choices = choices
        if let profile = value["profile"] as? String, profile.hasPrefix("/"), profile.utf8.count <= 4096 { entry.profile = profile }
        if !buttons.isEmpty && entry.until <= Date() { emit("expired", id: id); return }
        entries[id] = entry
        if !buttons.isEmpty {
            let timer = DispatchSource.makeTimerSource(queue: .main)
            timer.schedule(deadline: .now() + max(0, entry.until.timeIntervalSinceNow))
            timer.setEventHandler { [weak self, weak entry] in
                guard let self, let entry, self.entries[id] === entry else { return }
                self.remove(id, removeBanner: true); self.emit("expired", id: id)
            }
            entry.timer = timer; timer.resume()
        }
        #if DIBS_BROKER_FIXTURE
        post(entry, settings: nil); return
        #endif
        centre.requestAuthorization(options: [.alert, .sound]) { granted, _ in
            DispatchQueue.main.async {
                guard self.entries[id] === entry else { return }
                guard granted else {
                    self.remove(id, removeBanner: false)
                    self.emit("failed", id: id, error: "native notifications refused; run dibs doctor"); return
                }
                self.centre.getNotificationSettings { settings in
                    let observation = notificationSettings(settings)
                    DispatchQueue.main.async { self.post(entry, settings: observation) }
                }
                DispatchQueue.main.asyncAfter(deadline: .now() + 2) { self.post(entry, settings: nil) }
            }
        }
    }
    func post(_ entry: BrokerEntry, settings: [String: Any]?) {
        guard entries[entry.id] === entry, !entry.beganPosting else { return }
        entry.beganPosting = true
        let content = UNMutableNotificationContent()
        content.title = entry.title; content.subtitle = entry.subtitle; content.body = entry.excerpt
        content.userInfo[readingKey] = storedReading(entry.title, detail: entry.detail)
        if !entry.replyKind.isEmpty {
            var full = StoredReadingDetail(title: entry.title, sender: entry.detail.sender, body: entry.detail.body, kind: entry.detail.kind ?? "message")
            full.profile = entry.profile
            guard let bytes = try? JSONEncoder().encode(full), bytes.count <= 1048576 else {
                remove(entry.id, removeBanner: false); emit("failed", id: entry.id, error: "full notification text exceeds the private frame bound"); return
            }
            content.userInfo[fullReadingKey] = bytes
        }
        #if DIBS_BROKER_FIXTURE
        if let directory = ProcessInfo.processInfo.environment["DIBS_BROKER_POSTED_DIR"] {
            let saved: [String: Any] = ["title": content.title, "subtitle": content.subtitle,
                                      "body": content.body, "userInfo": content.userInfo]
            try! PropertyListSerialization.data(fromPropertyList: saved, format: .binary, options: 0)
                .write(to: URL(fileURLWithPath: directory + "/" + entry.id + ".plist"))
        }
        #endif
        if entry.timeSensitive { content.interruptionLevel = .timeSensitive }
        #if DIBS_BROKER_FIXTURE
        add(entry, content: content, category: nil, settings: settings); return
        #endif
        let categories = entries.values.filter { !$0.buttons.isEmpty }.map { live in
            UNNotificationCategory(identifier: "dibs.ask." + live.id,
                actions: live.buttons.map { UNNotificationAction(identifier: $0, title: $0, options: [.foreground]) },
                intentIdentifiers: [], options: [.customDismissAction])
        }
        // One owner sets the complete live set on the main queue. An async
        // read/union/write per post would let concurrent A and B erase each
        // other's category even with distinct category identifiers.
        centre.setNotificationCategories(Set(categories))
        add(entry, content: content, category: entry.buttons.isEmpty ? nil : "dibs.ask." + entry.id, settings: settings)
    }
    func add(_ entry: BrokerEntry, content: UNMutableNotificationContent, category: String?, settings: [String: Any]?) {
        if let category { content.categoryIdentifier = category }
        let completed: (Error?) -> Void = { error in
            DispatchQueue.main.async {
                guard self.entries[entry.id] === entry else { return }
                if let error {
                    self.remove(entry.id, removeBanner: false)
                    self.emit("failed", id: entry.id, error: "native posting failed: \(error)"); return
                }
                let level = entry.timeSensitive ? "timeSensitive" : "active"
                self.emit("posted", id: entry.id, receipt: ["state": "posted", "settings": settings as Any? ?? NSNull(),
                          "interruption_level": level, "requested_interruption_level": level])
                #if DIBS_BROKER_FIXTURE
                if ProcessInfo.processInfo.environment["DIBS_BROKER_DISMISS_POSTED"] == "1" && !entry.buttons.isEmpty {
                    self.response(id: entry.id, action: UNNotificationDismissActionIdentifier, content: content)
                }
                #endif
                if entry.buttons.isEmpty { self.entries.removeValue(forKey: entry.id) }
            }
        }
        #if DIBS_BROKER_FIXTURE
        completed(nil); return
        #endif
        centre.add(UNNotificationRequest(identifier: entry.id, content: content, trigger: nil), withCompletionHandler: completed)
    }
    func remove(_ id: String, removeBanner: Bool) {
        entries.removeValue(forKey: id)?.timer?.cancel()
        windows.removeValue(forKey: id)?.close()
        #if !DIBS_BROKER_FIXTURE
        if removeBanner { centre.removeDeliveredNotifications(withIdentifiers: [id]) }
        #endif
    }
    func userNotificationCenter(_ centre: UNUserNotificationCenter, willPresent notification: UNNotification,
                                withCompletionHandler done: @escaping (UNNotificationPresentationOptions) -> Void) {
        done([.banner, .sound])
    }
    func userNotificationCenter(_ centre: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                withCompletionHandler done: @escaping () -> Void) {
        done()
        DispatchQueue.main.async {
            self.response(id: response.notification.request.identifier, action: response.actionIdentifier,
                          content: response.notification.request.content)
        }
    }
    func response(id: String, action: String, content: UNNotificationContent?) {
        if let entry = entries[id] {
            if (action == UNNotificationDefaultActionIdentifier || action == "Reply…"), entry.profile != nil,
               openNativeHumanMessage(id: id, profile: entry.profile, title: entry.title, detail: entry.detail) { return }
            if !entry.buttons.isEmpty && entry.until <= Date() {
                remove(id, removeBanner: true); emit("expired", id: id); return
            }
            if action == UNNotificationDefaultActionIdentifier || (!entry.replyKind.isEmpty && action == "Reply…") {
                show(id: id, title: entry.title, detail: entry.detail, buttons: entry.buttons.isEmpty ? ["Close"] : entry.buttons)
            } else if action == UNNotificationDismissActionIdentifier {
                remove(id, removeBanner: false); emit("dismissed", id: id)
            } else if entry.buttons.contains(action) {
                remove(id, removeBanner: true); emit("action", id: id, action: action)
            } else { emit("refused", id: id) }
            return
        }
        guard let content, action != UNNotificationDismissActionIdentifier else { return }
        let saved = persistedReading(content)
        if openNativeHumanMessage(id: id, profile: saved?.profile, title: saved?.title ?? content.title,
                                  detail: AskReadingDetail(sender: saved?.sender ?? content.subtitle, body: saved?.body ?? content.body, kind: saved?.kind)) { return }
        var text = saved?.body ?? content.body
        if saved?.kind != "notify" && saved?.kind != "handoff" {
            text += "\n\nThis window cannot confirm the live answer route; continue on the Dibs board."
        }
        show(id: id, title: saved?.title ?? content.title,
             detail: AskReadingDetail(sender: saved?.sender ?? content.subtitle, body: text, kind: saved?.kind), buttons: ["Close"])
    }
    func show(id: String, title: String, detail: AskReadingDetail, buttons: [String]) {
        if let window = windows[id] { window.show(); return }
        let live = entries[id]
        let window = HumanPromptWindow(title: title, detail: detail, buttons: buttons,
                                       replyKind: live?.replyKind ?? "", choices: live?.choices ?? []) { [weak self] reply in
            guard let self else { return }
            self.windows.removeValue(forKey: id)?.close()
            guard let entry = self.entries[id] else { return }
            if reply.action.isEmpty || entry.buttons.isEmpty {
                self.remove(id, removeBanner: false); self.emit("dismissed", id: id)
            } else if !entry.replyKind.isEmpty {
                self.remove(id, removeBanner: true)
                self.emitReply(id: id, reply: reply)
            } else if entry.buttons.contains(reply.action) {
                self.remove(id, removeBanner: true); self.emit("action", id: id, action: reply.action)
            }
        }
        windows[id] = window
        #if DIBS_BROKER_FIXTURE
        emit("reading", id: id, view: window.viewInfo); return
        #endif
        window.show()
    }
    func emitReply(id: String, reply: HumanPromptReply) {
        let value: [String: Any] = ["v": 1, "event": "reply", "id": id, "action": reply.action,
                                    "text": reply.text, "choice": reply.choice]
        guard let data = try? JSONSerialization.data(withJSONObject: value), data.count < 1048576 else { exit(2) }
        FileHandle.standardOutput.write(data + Data([10]))
    }

}

let askEnvironment = ProcessInfo.processInfo.environment
let readingDetail = coldLaunch || brokerLaunch ? nil : readAskDetail(askEnvironment)
let notificationID = askEnvironment["DIBS_NOTIFY_ID"] ?? UUID().uuidString
guard let requestLifetime = askLifetime(coldLaunch || brokerLaunch ? [:] : askEnvironment, now: Date()) else { exit(2) }
let lifetime = readingDetail?.kind == "contact" ? min(120, requestLifetime) : requestLifetime
if !buttons.isEmpty && lifetime <= 0 { exit(1) }

// An NSApplication with a live run loop, not a semaphore.
//
// This is the bug that got shipped for ten minutes: the first version posted
// the banner and then blocked the main thread waiting on a DispatchSemaphore.
// UNUserNotificationCenter delivers its delegate callbacks ON THE MAIN QUEUE,
// so a blocked main thread means the button press has nowhere to land. The
// operator pressed Approve, nothing happened, and the process sat there until
// its own timeout. Posting worked, which is what made it look fine.
//
// .accessory keeps it out of the Dock: a coordination service that bounces an
// icon and steals focus to tell you something is the interruption the
// notification exists to avoid.
let app = NSApplication.shared
app.setActivationPolicy(.accessory)

// Mixed-version callers may still use argv. They take the same lifetime
// lock, so they fail loudly beside a live broker instead of stealing presses.
var legacyNotifierLock: Int32?
if !coldLaunch && !brokerLaunch && !askFixture {
    guard let fd = acquireBrokerLock() else {
        FileHandle.standardError.write(Data("Another Dibs notifier owns this Mac's notifications; run dibs doctor.\n".utf8))
        exit(2)
    }
    legacyNotifierLock = fd
}
let centre = UNUserNotificationCenter.current()
var askCaller: DispatchSourceProcess?
if readingDetail != nil && !buttons.isEmpty {
    // stdout is the only answer carrier. A helper whose caller died cannot
    // answer, so do not leave a 24-hour orphan holding a dead button route.
    let parent = getppid()
    guard parent > 1 else { exit(1) }
    let source = DispatchSource.makeProcessSource(identifier: parent, eventMask: .exit,
                                                  queue: .global(qos: .utility))
    source.setEventHandler { exit(1) }
    source.resume()
    askCaller = source
    if getppid() != parent { exit(1) } // parent left during source registration
}
var status: Int32 = 2
var chosen = ""

func finish(_ code: Int32) -> Never {
    status = code
    if !chosen.isEmpty { print(chosen) }
    exit(status)
}

// A delegate is required for the banner to appear while the app is frontmost,
// and to receive the button press. Without it a notification posted by a
// running app is delivered silently to Notification Centre.
final class Handler: NSObject, UNUserNotificationCenterDelegate {
    let finish: (String) -> Void
    var reading = false
    init(finish: @escaping (String) -> Void) { self.finish = finish }

    func userNotificationCenter(_ c: UNUserNotificationCenter,
                                willPresent n: UNNotification,
                                withCompletionHandler h: @escaping (UNNotificationPresentationOptions) -> Void) {
        h([.banner, .sound])
    }

    func userNotificationCenter(_ c: UNUserNotificationCenter,
                                didReceive r: UNNotificationResponse,
                                withCompletionHandler h: @escaping () -> Void) {
        receive(identifier: r.notification.request.identifier, action: r.actionIdentifier,
                content: r.notification.request.content, completion: h)
    }

    func receive(identifier: String, action: String, content: UNNotificationContent? = nil,
                 completion h: @escaping () -> Void) {
        let own = !coldLaunch && identifier == notificationID
        guard own || content != nil else { h(); return }
        if action == UNNotificationDismissActionIdentifier {
            h()
            if own { receipt("dismissed"); finish("") }
            else if coldLaunch { self.finish("") }
            return
        }
        // An original invocation still owns its stdout route. Reopened asks
        // fetch authoritative state and answer through the attested daemon route.
        if own && action != UNNotificationDefaultActionIdentifier {
            h()
            if buttons.contains(action) { finish(action) }
            return
        }
        h() // acknowledge before entering the nested modal run loop
        let saved = content.flatMap { persistedReading($0) }
        if !own, openNativeHumanMessage(id: identifier, profile: saved?.profile, title: saved?.title ?? content?.title ?? "Dibs",
                                       detail: AskReadingDetail(sender: saved?.sender ?? content?.subtitle ?? "Dibs", body: saved?.body ?? content?.body ?? "", kind: saved?.kind)) { return }
        let detail: AskReadingDetail
        let heading: String
        let choices: [String]
        if own, let live = readingDetail {
            detail = live
            heading = title
            choices = buttons.isEmpty ? ["Close"] : buttons
        } else {
            heading = saved?.title ?? content?.title ?? "Dibs"
            let kind = saved?.kind ?? "ask"
            var text = saved?.body ?? content?.body ?? "Read this notification on the Dibs board."
            if kind == "request" || kind == "question" || kind == "ask" {
                text += "\n\nThis window cannot confirm the live answer route; continue on the Dibs board."
            } else if kind == "contact" {
                text += "\n\nThis window cannot confirm the live Open route; continue on the Dibs board."
            }
            detail = AskReadingDetail(sender: saved?.sender ?? content?.subtitle ?? "Dibs", body: text, kind: kind)
            choices = ["Close"]
        }
        DispatchQueue.main.async {
            guard !self.reading else { return }
            self.reading = true
            let choice = readAskOnScreen(heading, detail: detail, buttons: choices,
                                         storedBytes: (content?.userInfo[readingKey] as? Data)?.count ?? 0)
            self.reading = false
            if own {
                if choice.isEmpty || buttons.isEmpty { receipt("dismissed"); self.finish("") }
                else { self.finish(choice) }
            } else if coldLaunch { self.finish("") }
            #if DIBS_READING_FIXTURE
            if askFixture { exit(0) }
            #endif
        }
    }
}

let handler = Handler { choice in
    chosen = choice
    finish(choice.isEmpty ? (askFixture ? 0 : 1) : 0)
}
centre.delegate = handler

// The main queue owns this barrier, including the bounded settings fallback.
// Missing diagnostic evidence must not delay or duplicate the actual posting.
var beganPosting = false
func postNotification(_ settings: [String: Any]?) {
    guard !beganPosting else { return }
    beganPosting = true
    receiptSettings = settings
    let interruption = requestedInterruptionLevel(buttons: buttons, environment: ProcessInfo.processInfo.environment)
    receiptInterruptionLevel = interruption == .timeSensitive ? "timeSensitive" : "active"
    let content = UNMutableNotificationContent()
    content.title = title
    if !subtitle.isEmpty { content.subtitle = subtitle }
    content.body = body
    if let detail = readingDetail { content.userInfo[readingKey] = storedReading(title, detail: detail) }

    if interruption == .timeSensitive {
        // Somebody is BLOCKED on this one, so it asks to break through Focus.
        //
        // Buttons mean a decision is waiting. A passive alert only reaches
        // this branch when its sender explicitly chose high/urgent priority.
        //
        // Measured: a coordinator request posted successfully, macOS accepted
        // it, `personal-time` Focus swallowed the banner, and every layer
        // reported success. The operator saw nothing and the agent waited out
        // half an hour.
        //
        // This is a REQUESTED level, never proof of the capability. The receipt
        // reports the actual timeSensitiveSetting; an unprovisioned build stays
        // honestly not-supported. Nothing requests critical interruption.
        content.interruptionLevel = .timeSensitive
    }
    if !buttons.isEmpty {
        let actions = buttons.map {
            UNNotificationAction(identifier: $0, title: $0, options: [.foreground])
        }
        let category = UNNotificationCategory(identifier: "dibs.ask", actions: actions,
                                              intentIdentifiers: [], options: [.customDismissAction])
        if !askFixture { centre.setNotificationCategories([category]) }
        content.categoryIdentifier = "dibs.ask"
    }

    let keyed = ProcessInfo.processInfo.environment["DIBS_NOTIFY_ID"]
    if let keyed, !validMessageID(keyed) { finish(2) }
    let posted: (Error?) -> Void = { err in
        if err != nil { finish(2) }
        receipt("posted")

        // Nothing to wait for when there is nothing to answer.
        if buttons.isEmpty { finish(0) }
    }
    #if DIBS_READING_FIXTURE
    if askFixture, let path = askEnvironment["DIBS_READING_POSTED"] {
        let saved: [String: Any] = ["title": content.title, "subtitle": content.subtitle,
                                  "body": content.body, "userInfo": content.userInfo]
        try! PropertyListSerialization.data(fromPropertyList: saved, format: .binary, options: 0)
            .write(to: URL(fileURLWithPath: path))
        if let action = askEnvironment["DIBS_READING_ACTION"] {
            guard let installed = centre.delegate as? Handler else { exit(2) }
            installed.receive(identifier: askEnvironment["DIBS_READING_RESPONSE_ID"] ?? notificationID,
                            action: action, content: content, completion: {})
        } else { finish(0) }
        return
    }
    #endif
    if askFixture { posted(nil); return }
    centre.add(UNNotificationRequest(identifier: notificationID,
                                     content: content, trigger: nil), withCompletionHandler: posted)
}

// Apple's notification response contract launches the bundle then calls the
// delegate. Assigning it above, before app.run(), meets its before-launch rule:
// https://developer.apple.com/documentation/usernotifications/unusernotificationcenter/delegate
// https://developer.apple.com/documentation/usernotifications/handling-notifications-and-notification-related-actions
if brokerLaunch {
    guard let broker = NativeNotificationBroker(centre: centre) else {
        let failure: [String: Any] = ["v": 1, "event": "failed", "id": "",
            "error": "Another Dibs notifier owns this Mac's notifications, or its lock cannot be acquired; read the board and run dibs doctor."]
        FileHandle.standardOutput.write(try! JSONSerialization.data(withJSONObject: failure) + Data([10]))
        exit(2)
    }
    centre.delegate = broker
    broker.start()
    withExtendedLifetime(broker) { app.run() }
    exit(0)
}
if coldLaunch {
    #if DIBS_READING_FIXTURE
    if askFixture, let path = askEnvironment["DIBS_READING_POSTED"] {
        let bytes = try! Data(contentsOf: URL(fileURLWithPath: path))
        let saved = try! PropertyListSerialization.propertyList(from: bytes, format: nil) as! [String: Any]
        let content = UNMutableNotificationContent()
        content.title = saved["title"] as! String
        content.subtitle = saved["subtitle"] as! String
        content.body = saved["body"] as! String
        content.userInfo = saved["userInfo"] as! [AnyHashable: Any]
        if askEnvironment["DIBS_READING_LEGACY_PROFILE"] == "1", let data = content.userInfo[fullReadingKey] as? Data,
           var detail = try? JSONDecoder().decode(StoredReadingDetail.self, from: data) {
            detail.profile = nil; content.userInfo[fullReadingKey] = try! JSONEncoder().encode(detail)
        }
        switch askEnvironment["DIBS_READING_CORRUPT"] {
        case "oversize":
            let oversized = StoredReadingDetail(title: "Daemon effect", sender: "Daemon sender",
                                                body: String(repeating: "x", count: 8193) + "PRIVATE END", kind: "request")
            content.userInfo[readingKey] = try! JSONEncoder().encode(oversized)
        case "invalid": content.userInfo[readingKey] = Data("not json".utf8)
        case "missing": content.userInfo = [:]
        default: break
        }
        guard let installed = centre.delegate as? Handler else { exit(2) }
        installed.receive(identifier: askEnvironment["DIBS_READING_NOTIFICATION_ID"] ?? "dibs.msg.reading.7",
                        action: askEnvironment["DIBS_READING_ACTION"] ?? UNNotificationDefaultActionIdentifier,
                        content: content, completion: {})
    }
    #endif
} else if askFixture {
    postNotification(nil)
    #if DIBS_ASK_FIXTURE
    if let action = askEnvironment["DIBS_ASK_FIXTURE_ACTION"] {
        DispatchQueue.main.async {
            handler.receive(identifier: askEnvironment["DIBS_ASK_FIXTURE_ID"] ?? "", action: action, completion: {})
        }
    }
    #endif
} else {
    centre.requestAuthorization(options: [.alert, .sound]) { granted, _ in
        guard granted else { finish(2) }
        centre.getNotificationSettings { s in
            let settings = notificationSettings(s)
            DispatchQueue.main.async { postNotification(settings) }
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) { postNotification(nil) }
    }
}

// New asks remain answerable until their recorded deadline (at most 24 h).
// The caller cancels a live helper on board/relay cleanup. This is the helper's
// own bound, not a daemon timer. Old callers retain their two-minute contract.
let expireAsk = {
    if !askFixture, readingDetail != nil, let id = askEnvironment["DIBS_NOTIFY_ID"], validMessageID(id) {
        centre.removeDeliveredNotifications(withIdentifiers: [id])
    }
    exit(1) // no answer; independent of a reading window's nested modal loop
}
if readingDetail != nil && !buttons.isEmpty {
    DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + lifetime, execute: expireAsk)
} else {
    DispatchQueue.main.asyncAfter(deadline: .now() + (buttons.isEmpty ? 10 : lifetime)) {
        if !handler.reading && nativeHumanWindows.isEmpty { finish(1) }
    }
}
app.run()
