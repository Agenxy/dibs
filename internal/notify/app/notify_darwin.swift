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
import AppKit
import Foundation
import UserNotifications

let args = Array(CommandLine.arguments.dropFirst())
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
