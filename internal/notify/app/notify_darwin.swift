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
import AppKit
import Foundation
import UserNotifications

let args = Array(CommandLine.arguments.dropFirst())

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

// --ask: the same question as a banner, in a window that Focus cannot silence.
//
// A notification is the right shape and is not always available. Measured on the
// machine this was written on: authorisation granted, alerts enabled, the
// notification delivered and held by macOS, and never seen, because two Focus
// modes were active and `timeSensitiveSetting` reports notSupported. Time
// Sensitive is what breaks through Focus, and Apple gates it behind an
// entitlement Dibs does not carry, signing being left to whoever installs it.
//
// So on that machine a request to the human could not be seen in time, by
// construction, however correctly it was posted. This is the escalation: same
// text, same buttons, as a window that activates. It is used only when the quiet
// path is known to be silenced, never by default, because a service that steals
// focus for every question is one people turn off.
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

if args.first == "--ask" {
    // --out <path> may precede the rest: where to leave the answer.
    var rest = Array(args.dropFirst())
    var outPath: String?
    if rest.first == "--out", rest.count >= 2 {
        outPath = rest[1]
        rest = Array(rest.dropFirst(2))
    }
    guard rest.count >= 3 else {
        FileHandle.standardError.write("usage: dibs-notify --ask <title> <body> <button…>\n".data(using: .utf8)!)
        exit(2)
    }
    let app = NSApplication.shared
    app.setActivationPolicy(.accessory)
    app.activate(ignoringOtherApps: true)

    let alert = NSAlert()
    alert.messageText = rest[0]
    alert.informativeText = rest[1]
    alert.alertStyle = .informational
    // Added in order, and AppKit puts the first button rightmost as the default,
    // so the caller's last-is-default convention is preserved by reversing.
    for title in Array(rest.dropFirst(2)).reversed() {
        alert.addButton(withTitle: title)
    }
    let pressed = alert.runModal()
    let index = pressed.rawValue - NSApplication.ModalResponse.alertFirstButtonReturn.rawValue
    let buttons = Array(rest.dropFirst(2)).reversed().map { $0 }
    guard index >= 0 && index < buttons.count else { exit(1) }
    // Written to a FILE as well as stdout.
    //
    // The daemon launches this through `launchctl asuser`, which is what gives
    // it a GUI session to draw in, and which does not carry our stdout back.
    // The answer therefore has to be left somewhere the daemon can read it.
    if let out = outPath {
        try? buttons[index].write(toFile: out, atomically: true, encoding: .utf8)
    }
    print(buttons[index])
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
            finish("")            // clicked the banner itself: not an answer
        } else if r.actionIdentifier == UNNotificationDismissActionIdentifier {
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

centre.requestAuthorization(options: [.alert, .sound]) { granted, _ in
    guard granted else { finish(2) }

    let content = UNMutableNotificationContent()
    content.title = title
    if !subtitle.isEmpty { content.subtitle = subtitle }
    content.body = body

    if !buttons.isEmpty {
        // Somebody is BLOCKED on this one, so it asks to break through Focus.
        //
        // Buttons are the tell: Dibs only attaches them when an agent is waiting
        // for a decision. An FYI keeps the default level and is correctly
        // silenced by Focus; a question or a request is the case where silence
        // costs somebody their deadline.
        //
        // Measured: a coordinator request posted successfully, macOS accepted
        // it, `personal-time` Focus swallowed the banner, and every layer
        // reported success. The operator saw nothing and the agent waited out
        // half an hour.
        //
        // Apple gates .timeSensitive behind an entitlement, which Dibs does not
        // carry because signing is left to whoever installs it. Setting it is
        // still right: where it is honoured the ask arrives, and where it is not
        // this is exactly what happened anyway. `dibs doctor` reports an active
        // Focus either way, so the failure is never silent again.
        content.interruptionLevel = .timeSensitive
        let actions = buttons.map {
            UNNotificationAction(identifier: $0, title: $0, options: [.foreground])
        }
        let category = UNNotificationCategory(identifier: "dibs.ask", actions: actions,
                                              intentIdentifiers: [], options: [])
        centre.setNotificationCategories([category])
        content.categoryIdentifier = "dibs.ask"
    }

    centre.add(UNNotificationRequest(identifier: UUID().uuidString,
                                     content: content, trigger: nil)) { err in
        if err != nil { finish(2) }

        // Nothing to wait for when there is nothing to answer.
        if buttons.isEmpty { finish(0) }
    }
}

// Bounded. A banner nobody answers must not hold a process open on an
// unattended machine, and the caller treats a timeout as "no answer".
DispatchQueue.main.asyncAfter(deadline: .now() + (buttons.isEmpty ? 10 : 120)) {
    finish(1)
}
app.run()
