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
    app.activate(ignoringOtherApps: true)

    let alert = NSAlert()
    alert.messageText = heading
    alert.informativeText = detail
    alert.alertStyle = .informational

    var field: NSTextField?
    var menu: NSPopUpButton?
    if choices.isEmpty {
        let f = NSTextField(frame: NSRect(x: 0, y: 0, width: 320, height: 24))
        f.placeholderString = "Your answer"
        alert.accessoryView = f
        field = f
    } else {
        // A pop-up rather than N buttons: an alert caps at three, and the whole
        // reason this path exists is a list that did not fit on the banner.
        let m = NSPopUpButton(frame: NSRect(x: 0, y: 0, width: 320, height: 26), pullsDown: false)
        m.addItems(withTitles: choices)
        alert.accessoryView = m
        menu = m
    }
    alert.addButton(withTitle: "Send")
    alert.addButton(withTitle: "Cancel")
    // Focus in the field, so they can type immediately rather than click first.
    alert.window.initialFirstResponder = alert.accessoryView

    guard alert.runModal() == .alertFirstButtonReturn else { return 1 }
    let answer = field.map { $0.stringValue } ?? menu?.titleOfSelectedItem ?? ""
    let trimmed = answer.trimmingCharacters(in: .whitespacesAndNewlines)
    if trimmed.isEmpty { return 1 } // sending nothing is not an answer
    print(trimmed)
    return 0
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

guard args.count >= 3 else {
    FileHandle.standardError.write("usage: dibs-notify <title> <subtitle> <body> [button…]\n".data(using: .utf8)!)
    exit(2)
}
let title = args[0], subtitle = args[1], body = args[2]
let buttons = Array(args.dropFirst(3))

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

let centre = UNUserNotificationCenter.current()
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
    init(finish: @escaping (String) -> Void) { self.finish = finish }

    func userNotificationCenter(_ c: UNUserNotificationCenter,
                                willPresent n: UNNotification,
                                withCompletionHandler h: @escaping (UNNotificationPresentationOptions) -> Void) {
        h([.banner, .sound])
    }

    func userNotificationCenter(_ c: UNUserNotificationCenter,
                                didReceive r: UNNotificationResponse,
                                withCompletionHandler h: @escaping () -> Void) {
        // The identifier is the button's own title, so the caller reads back
        // exactly what it offered rather than an index it has to map.
        if r.actionIdentifier == UNNotificationDefaultActionIdentifier {
            receipt("dismissed")
            finish("")            // clicked the banner itself: not an answer
        } else if r.actionIdentifier == UNNotificationDismissActionIdentifier {
            receipt("dismissed")
            finish("")
        } else {
            finish(r.actionIdentifier)
        }
        h()
    }
}

let handler = Handler { choice in
    chosen = choice
    finish(choice.isEmpty ? 1 : 0)
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
        centre.setNotificationCategories([category])
        content.categoryIdentifier = "dibs.ask"
    }

    let keyed = ProcessInfo.processInfo.environment["DIBS_NOTIFY_ID"]
    if let keyed, !validMessageID(keyed) { finish(2) }
    centre.add(UNNotificationRequest(identifier: keyed ?? UUID().uuidString,
                                     content: content, trigger: nil)) { err in
        if err != nil { finish(2) }
        receipt("posted")

        // Nothing to wait for when there is nothing to answer.
        if buttons.isEmpty { finish(0) }
    }
}

centre.requestAuthorization(options: [.alert, .sound]) { granted, _ in
    guard granted else { finish(2) }
    centre.getNotificationSettings { s in
        let settings = notificationSettings(s)
        DispatchQueue.main.async { postNotification(settings) }
    }
    DispatchQueue.main.asyncAfter(deadline: .now() + 2) { postNotification(nil) }
}

// Bounded. A banner nobody answers must not hold a process open on an
// unattended machine, and the caller treats a timeout as "no answer".
DispatchQueue.main.asyncAfter(deadline: .now() + (buttons.isEmpty ? 10 : 120)) {
    finish(1)
}
app.run()
