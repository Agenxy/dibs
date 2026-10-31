// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy
// Embedded at build time by tools/appbundle and native fixtures. It is AppKit
// only: the window owns transient editing state; the broker owns answer routes.
import CoreText
import QuartzCore

enum PromptFonts {
    static let urls = Bundle.main.urls(forResourcesWithExtension: "ttf", subdirectory: "Fonts") ?? []
    static let registered: Void = {
        for url in urls { CTFontManagerRegisterFontsForURL(url as CFURL, .process, nil) }
    }()
    static var families: [String] {
        _ = registered
        return NSFontManager.shared.availableFontFamilies.filter { $0 == "Atkinson Hyperlegible Next" || $0 == "Atkinson Hyperlegible Mono" }
    }
}

struct HumanPromptReply {
    var action = ""
    var choice = ""
    var text = ""
}

final class PromptSurface: NSView {
    var appearanceChanged: (() -> Void)?
    override func viewDidChangeEffectiveAppearance() { super.viewDidChangeEffectiveAppearance(); appearanceChanged?() }
}

final class PromptActionButton: NSButton {
    var fill = NSColor.clear
    var ink = NSColor.labelColor
    var hovered = false
    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        for area in trackingAreas { removeTrackingArea(area) }
        addTrackingArea(NSTrackingArea(rect: bounds, options: [.mouseEnteredAndExited, .activeAlways, .inVisibleRect], owner: self))
    }
    override func mouseEntered(with event: NSEvent) { hovered = true; needsDisplay = true }
    override func mouseExited(with event: NSEvent) { hovered = false; needsDisplay = true }
    override func draw(_ dirtyRect: NSRect) {
        (isEnabled ? (isHighlighted ? fill.blended(withFraction: 0.1, of: .black) ?? fill : hovered ? fill.blended(withFraction: 0.06, of: .white) ?? fill : fill) : fill.withAlphaComponent(0.35)).setFill()
        NSBezierPath(roundedRect: bounds.insetBy(dx: 1, dy: 1), xRadius: 8, yRadius: 8).fill()
        let attributes: [NSAttributedString.Key: Any] = [.font: font ?? NSFont.systemFont(ofSize: 13), .foregroundColor: isEnabled ? ink : NSColor.disabledControlTextColor]
        let size = (title as NSString).size(withAttributes: attributes)
        (title as NSString).draw(at: NSPoint(x: (bounds.width - size.width) / 2, y: (bounds.height - size.height) / 2), withAttributes: attributes)
    }
    override func drawFocusRingMask() { NSBezierPath(roundedRect: bounds, xRadius: 8, yRadius: 8).fill() }
    override var focusRingMaskBounds: NSRect { bounds }
}

final class PromptChoiceButton: NSButton {
    var accent = NSColor.controlAccentColor
    var ground = NSColor.clear
    var ink = NSColor.labelColor
    var muted = NSColor.secondaryLabelColor
    var textScale: CGFloat = 1
    var focused: (() -> Void)?
    var tabStop = false
    override var acceptsFirstResponder: Bool { tabStop }
    override func becomeFirstResponder() -> Bool { let accepted = super.becomeFirstResponder(); if accepted { focused?() }; return accepted }
    override var isFlipped: Bool { true }
    override func draw(_ dirtyRect: NSRect) {
        let selected = state == .on
        if selected {
            ground.setFill(); let path = NSBezierPath(roundedRect: bounds.insetBy(dx: 1, dy: 1), xRadius: 6, yRadius: 6); path.fill()
            accent.setStroke(); path.lineWidth = 1.5; path.stroke()
        }
        let ring = NSRect(x: 8, y: 12 * textScale, width: 16 * textScale, height: 16 * textScale)
        (selected ? accent : muted).setStroke(); let path = NSBezierPath(ovalIn: ring); path.lineWidth = 1.5; path.stroke()
        if selected { accent.setFill(); NSBezierPath(ovalIn: ring.insetBy(dx: 4, dy: 4)).fill() }
        let paragraph = NSMutableParagraphStyle(); paragraph.lineBreakMode = .byWordWrapping
        let attributes: [NSAttributedString.Key: Any] = [.font: font ?? NSFont.systemFont(ofSize: 14), .foregroundColor: ink, .paragraphStyle: paragraph]
        (title as NSString).draw(with: NSRect(x: 34 * textScale, y: 8 * textScale, width: bounds.width - 40 * textScale, height: bounds.height - 8 * textScale),
                                  options: [.usesLineFragmentOrigin, .usesFontLeading], attributes: attributes)
    }
    override func drawFocusRingMask() { NSBezierPath(roundedRect: bounds, xRadius: 6, yRadius: 6).fill() }
    override var focusRingMaskBounds: NSRect { bounds }
}

final class PromptTextView: NSTextView {
    var returnPressed: (() -> Void)?
    var placeholder = "Reply (optional)"
    var placeholderColor = NSColor.placeholderTextColor
    var focused: (() -> Void)?
    override func becomeFirstResponder() -> Bool { let accepted = super.becomeFirstResponder(); if accepted { focused?() }; return accepted }
    override func draw(_ dirtyRect: NSRect) {
        super.draw(dirtyRect)
        if string.isEmpty {
            (placeholder as NSString).draw(at: NSPoint(x: textContainerInset.width + 4, y: textContainerInset.height),
                withAttributes: [.font: font ?? NSFont.systemFont(ofSize: 14), .foregroundColor: placeholderColor])
        }
    }
    override func keyDown(with event: NSEvent) {
        if event.keyCode == 36 || event.keyCode == 76 {
            if event.modifierFlags.intersection([.shift, .option]).isEmpty {
                returnPressed?(); return
            }
        }
        super.keyDown(with: event)
    }
}

final class HumanPromptPanel: NSPanel {
    var shortcut: ((NSEvent) -> Bool)?
    override var canBecomeKey: Bool { true }
    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        if shortcut?(event) == true { return true }
        return super.performKeyEquivalent(with: event)
    }
}

// One native window for live replies, direct prompts, and read-only cold mail.
// Full text is selectable; the layout never substitutes an ellipsis for data.
final class HumanPromptWindow: NSObject, NSWindowDelegate, NSTextViewDelegate {
    let panel: HumanPromptPanel
    let heading: String
    let detail: AskReadingDetail
    let replyKind: String
    let choices: [String]
    let legacyButtons: [String]
    let picked: (HumanPromptReply) -> Void
    let root = PromptSurface()
    let mark = NSImageView()
    let name = NSTextField(wrappingLabelWithString: "")
    let context = NSTextField(wrappingLabelWithString: "")
    let kind = NSTextField(labelWithString: "")
    let message = NSTextView()
    let composer = PromptTextView()
    let messageScroll = NSScrollView()
    let replyScroll = NSScrollView()
    let overflowScroll = NSScrollView()
    let document = NSView()
    let choiceGroup = NSView()
    let separator = NSBox()
    let hint = NSTextField(labelWithString: "")
    let more = NSTextField(labelWithString: "More below ↓")
    let moreFade = NSView()
    let moreGradient = CAGradientLayer()
    var radios = [NSButton]()
    var actionButtons = [NSButton]()
    var selected: Int?
    var scale: CGFloat = 1
    var preferredWidth: CGFloat = 560
    var overflow = false
    var layingOut = false
    var finished = false
    var sent = false
    var shown = false
    static var cascade = 0
    var observers = [NSObjectProtocol]()
    #if DIBS_BROKER_FIXTURE
    var fixtureScale: CGFloat?
    var fixtureScreen: NSSize?
    #endif

    init(title: String, detail: AskReadingDetail, buttons: [String], replyKind: String = "",
         choices: [String] = [], picked: @escaping (HumanPromptReply) -> Void) {
        self.heading = title; self.detail = detail; self.legacyButtons = buttons
        self.replyKind = replyKind; self.choices = choices; self.picked = picked
        panel = HumanPromptPanel(contentRect: NSRect(x: 0, y: 0, width: 560, height: 420),
                                 styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
        super.init()
        _ = PromptFonts.registered
        panel.delegate = self; panel.isReleasedWhenClosed = false
        panel.title = title; panel.titleVisibility = .hidden; panel.titlebarAppearsTransparent = true
        panel.contentView = root
        panel.setAccessibilityLabel("Dibs \(detail.kind ?? "message") from \(detail.sender)")
        name.stringValue = title.hasPrefix("Dibs · ") ? String(title.dropFirst(7)) : title
        name.maximumNumberOfLines = 0; context.maximumNumberOfLines = 0
        name.setAccessibilityRole(NSAccessibility.Role(rawValue: "AXHeading"))
        context.stringValue = detail.sender
        kind.stringValue = replyKind == "request" ? "APPROVAL" : (detail.kind ?? "message").uppercased()
        separator.boxType = .separator
        if let url = Bundle.main.url(forResource: "Dibs", withExtension: "icns") ?? Bundle.main.url(forResource: "Dibs", withExtension: "png"), let icon = NSImage(contentsOf: url) {
            icon.isTemplate = false; mark.image = icon
        }
        mark.setAccessibilityLabel("Dibs")
        for view in [mark, name, context, kind, separator, hint, messageScroll, replyScroll, overflowScroll] { root.addSubview(view) }
        moreFade.wantsLayer = true; moreFade.layer?.addSublayer(moreGradient)
        moreGradient.startPoint = CGPoint(x: 0.5,y: 1); moreGradient.endPoint = CGPoint(x: 0.5,y: 0)
        moreFade.addSubview(more); root.addSubview(moreFade)
        more.setAccessibilityLabel(choices.isEmpty ? "More content below: a reply field" : "More content below: choices and a reply field")
        message.isEditable = false; message.isSelectable = true
        message.isVerticallyResizable = true; message.isHorizontallyResizable = false
        message.maxSize = NSSize(width: CGFloat.greatestFiniteMagnitude, height: CGFloat.greatestFiniteMagnitude)
        message.textContainer?.widthTracksTextView = true
        message.textContainer?.heightTracksTextView = false
        message.textContainerInset = NSSize(width: 14, height: 12)
        message.setAccessibilityLabel("Full message")
        composer.isRichText = false; composer.isVerticallyResizable = true
        composer.textContainer?.widthTracksTextView = true
        composer.textContainerInset = NSSize(width: 10, height: 8)
        composer.delegate = self; composer.setAccessibilityLabel("Reply")
        composer.returnPressed = { [weak self] in if self?.replyKind == "question" { self?.send() } }
        configureScroll(messageScroll, text: message)
        configureScroll(replyScroll, text: composer)
        overflowScroll.hasVerticalScroller = true; overflowScroll.autohidesScrollers = false
        overflowScroll.scrollerStyle = .legacy
        overflowScroll.drawsBackground = false; overflowScroll.contentView.drawsBackground = false
        // Scrolled choices must remain inside this viewport, below the pinned
        // sender/context header, including layer-backed offscreen rendering.
        overflowScroll.wantsLayer = true; overflowScroll.layer?.masksToBounds = true
        overflowScroll.contentView.postsBoundsChangedNotifications = true
        overflowScroll.documentView = document; overflowScroll.isHidden = true
        choiceGroup.setAccessibilityElement(true); choiceGroup.setAccessibilityRole(.radioGroup); choiceGroup.setAccessibilityLabel("Choices")
        root.addSubview(choiceGroup)
        for (index, choice) in choices.enumerated() {
            let button = PromptChoiceButton(title: "\(index + 1)  \(choice)", target: self, action: #selector(selectChoice(_:)))
            button.setButtonType(.radio)
            button.tag = index; button.state = .off
            button.tabStop = index == 0
            button.cell?.wraps = true; button.cell?.lineBreakMode = .byWordWrapping
            button.focusRingType = .exterior
            button.setAccessibilityLabel("\(index + 1) of \(choices.count), \(choice)")
            button.focused = { [weak button] in if let button { button.scrollToVisible(button.bounds) } }
            choiceGroup.addSubview(button); radios.append(button)
        }
        let labels = replyKind == "request" ? ["Reply without deciding", "Deny", "Approve"] :
                     replyKind == "question" ? ["Cancel", "Send"] : buttons
        for label in labels {
            let button = PromptActionButton(title: label, target: self, action: #selector(action(_:)))
            button.bezelStyle = .rounded; button.focusRingType = .exterior
            button.setAccessibilityLabel(label)
            if label == "Cancel" || label == "Close" { button.keyEquivalent = "\u{1b}" }
            if replyKind == "question" && label == "Send" { button.keyEquivalent = "\r" }
            root.addSubview(button); actionButtons.append(button)
        }
        // An approval has no default button. Return cannot manufacture a grant.
        if replyKind == "request" { panel.defaultButtonCell = nil }
        panel.shortcut = { [weak self] event in self?.key(event) ?? false }
        root.appearanceChanged = { [weak self] in
            guard let self else { return }
            self.panel.effectiveAppearance.performAsCurrentDrawingAppearance { self.layout(fit: false) }
        }
        composer.placeholder = choices.isEmpty && replyKind == "question" ? "Reply" : "Reply (optional)"
        composer.focused = { [weak self] in if let self, self.overflow { self.replyScroll.scrollToVisible(self.replyScroll.bounds) } }
        let center = NotificationCenter.default
        observers.append(center.addObserver(forName: NSView.boundsDidChangeNotification, object: overflowScroll.contentView, queue: .main) { [weak self] _ in self?.updateMore() })
        observers.append(center.addObserver(forName: NSApplication.didChangeScreenParametersNotification, object: nil, queue: .main) { [weak self] _ in self?.layout(fit: true) })
        observers.append(NSWorkspace.shared.notificationCenter.addObserver(forName: NSWorkspace.accessibilityDisplayOptionsDidChangeNotification, object: nil, queue: .main) { [weak self] _ in self?.layout(fit: true) })
        layout(fit: true)
    }
    deinit {
        for observer in observers { NotificationCenter.default.removeObserver(observer); NSWorkspace.shared.notificationCenter.removeObserver(observer) }
    }
    func configureScroll(_ scroll: NSScrollView, text: NSTextView) {
        scroll.hasVerticalScroller = true; scroll.autohidesScrollers = true
        scroll.drawsBackground = true; scroll.borderType = .noBorder
        scroll.wantsLayer = true; scroll.layer?.cornerRadius = 6
        scroll.documentView = text
    }
    func color(_ light: UInt32, _ dark: UInt32) -> NSColor {
        NSColor(name: nil) { appearance in
            let value = appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua ? dark : light
            return NSColor(srgbRed: CGFloat((value >> 16) & 255) / 255,
                           green: CGFloat((value >> 8) & 255) / 255, blue: CGFloat(value & 255) / 255, alpha: 1)
        }
    }
    func font(_ size: CGFloat, weight: NSFont.Weight = .regular, mono: Bool = false) -> NSFont {
        let face = mono ? "Atkinson Hyperlegible Mono" : "Atkinson Hyperlegible Next"
        if NSFontManager.shared.availableFontFamilies.contains(face),
           let available = NSFont(descriptor: NSFontDescriptor(fontAttributes: [.family:face]), size: size * scale) {
            let dark = panel.effectiveAppearance.bestMatch(from: [.darkAqua,.aqua]) == .darkAqua
            let value = weight == .semibold ? (dark ? 660 : 640) : weight == .medium ? (dark ? 576 : 560) : (dark ? 412 : 400)
            let descriptor = available.fontDescriptor.addingAttributes([.variation:[NSNumber(value:0x77676874):NSNumber(value:value)]])
            return NSFont(descriptor: descriptor, size: size * scale) ?? available
        }
        return mono ? .monospacedSystemFont(ofSize: size * scale, weight: weight) : .systemFont(ofSize: size * scale, weight: weight)
    }
    func height(_ text: String, width: CGFloat, font: NSFont, line: CGFloat = 1.5) -> CGFloat {
        let paragraph = NSMutableParagraphStyle(); paragraph.lineSpacing = max(0, font.pointSize * line - font.ascender + font.descender)
        return ceil((text as NSString).boundingRect(with: NSSize(width: max(1, width), height: .greatestFiniteMagnitude),
            options: [.usesLineFragmentOrigin, .usesFontLeading], attributes: [.font: font, .paragraphStyle: paragraph]).height) + 4
    }
    func applyStyle() {
        let ink = color(0x21272F, 0xDCDBD6), sunk = color(0xE0DCD6, 0x141412)
        let contrast = NSWorkspace.shared.accessibilityDisplayShouldIncreaseContrast
        root.wantsLayer = true; root.layer?.backgroundColor = color(0xE6E3DF, 0x1C1C1A).cgColor
        panel.backgroundColor = color(0xE6E3DF, 0x1C1C1A)
        name.font = font(16, weight: .semibold); name.textColor = color(0x21272F, 0xEDE7DB)
        context.font = font(12, mono: true); context.textColor = contrast ? ink : color(0x40464D, 0xA7A6A2)
        kind.font = font(11, weight: .medium, mono: true); kind.textColor = color(0x8A3F17, 0xDC7B40)
        hint.font = font(11, mono: true); hint.textColor = contrast ? ink : color(0x4B5056, 0x989793)
        more.font = font(11, weight: .medium, mono: true); more.textColor = contrast ? ink : color(0x4B5056,0x989793)
        let paper = color(0xE6E3DF,0x1C1C1A)
        moreGradient.colors = [paper.withAlphaComponent(0).cgColor,paper.cgColor]
        hint.stringValue = replyKind == "request" ? "" : "⏎ send · ⎋ cancel"
        let paragraph = NSMutableParagraphStyle(); paragraph.lineSpacing = 7 * scale; paragraph.paragraphSpacing = 7 * scale
        let plain = detail.body.replacingOccurrences(of: "\n[\t ]*\n+", with: "\n", options: .regularExpression)
        let attributed = NSMutableAttributedString(string: plain, attributes: [.font: font(14), .foregroundColor: ink, .paragraphStyle: paragraph])
        if let code = try? NSRegularExpression(pattern: "`[^`\n]+`") {
            for match in code.matches(in: plain, range: NSRange(location: 0, length: (plain as NSString).length)).reversed() {
                attributed.deleteCharacters(in: NSRange(location: match.range.location + match.range.length - 1, length: 1))
                attributed.deleteCharacters(in: NSRange(location: match.range.location, length: 1))
                attributed.addAttribute(.font, value: font(13, mono: true), range: NSRange(location: match.range.location, length: match.range.length - 2))
            }
        }
        if let links = try? NSRegularExpression(pattern: "https?://[^\\s<>()]+") {
            let text = attributed.string as NSString
            for match in links.matches(in: attributed.string, range: NSRange(location: 0, length: text.length)) {
                if let url = URL(string: text.substring(with: match.range)) { attributed.addAttribute(.link, value: url, range: match.range) }
            }
        }
        message.linkTextAttributes = [.foregroundColor: color(0x1C5284,0x5A9FD4)]
        message.textStorage?.setAttributedString(attributed)
        message.backgroundColor = sunk; message.insertionPointColor = ink
        messageScroll.backgroundColor = sunk; replyScroll.backgroundColor = sunk
        composer.font = font(14); composer.textColor = ink; composer.backgroundColor = sunk; composer.insertionPointColor = ink
        composer.placeholderColor = contrast ? ink : color(0x4B5056,0x989793)
        for (index, radio) in radios.enumerated() {
            radio.font = font(14, weight: index == selected ? .medium : .regular)
            radio.contentTintColor = ink
            if let choice = radio as? PromptChoiceButton {
                choice.ink = ink; choice.muted = color(0x40464D,0xA7A6A2); choice.accent = color(0x8A3F17,0xDC7B40)
                choice.ground = sunk; choice.textScale = scale; choice.needsDisplay = true
            }
        }
        for button in actionButtons {
            button.font = font(13, weight: .medium)
            if let action = button as? PromptActionButton { action.fill = sunk; action.ink = ink }
            if button.title == "Send" || button.title == "Approve" {
                button.bezelColor = color(0x8A3F17, 0xDC7B40); button.contentTintColor = color(0xFFFFFF, 0x1C1C1A)
                if let action = button as? PromptActionButton { action.fill = color(0x8A3F17,0xDC7B40); action.ink = color(0xFFFFFF,0x1C1C1A) }
            }
            if button.title == "Reply without deciding", let action = button as? PromptActionButton { action.fill = .clear; action.ink = color(0x1C5284,0x5A9FD4) }
        }
        mark.wantsLayer = true; mark.layer?.cornerRadius = 6.5
        let dark = panel.effectiveAppearance.bestMatch(from: [.darkAqua,.aqua]) == .darkAqua
        mark.layer?.borderWidth = dark ? 1 / (panel.backingScaleFactor) : 0
        mark.layer?.borderColor = color(0x21272F,0xE8E4DF).withAlphaComponent(dark ? 0.14 : 0.12).cgColor
        updateEnabled()
    }
    func layout(fit: Bool) {
        guard !layingOut else { return }; layingOut = true; defer { layingOut = false }
        scale = min(1.6, max(0.9, NSFont.preferredFont(forTextStyle: .body).pointSize / 13))
        #if DIBS_BROKER_FIXTURE
        if let raw = ProcessInfo.processInfo.environment["DIBS_PROMPT_SCALE"], let value = Double(raw) { scale = min(1.6, max(0.9, value)) }
        if let fixtureScale { scale = min(1.6, max(0.9, fixtureScale)) }
        #endif
        var screen = panel.screen?.visibleFrame ?? NSScreen.main?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1280, height: 800)
        #if DIBS_BROKER_FIXTURE
        if let fixtureScreen { screen.size = fixtureScreen }
        #endif
        preferredWidth = min(scale > 1.15 ? min(720, 560 * scale) : 560, screen.width * 0.9)
        let width = preferredWidth, inset: CGFloat = 20, inner = width - 2 * inset
        applyStyle()
        let kindWidth = ceil((kind.stringValue as NSString).size(withAttributes: [.font: font(11,weight:.medium,mono:true)]).width) + 6
        let nameNatural = (name.stringValue as NSString).size(withAttributes: [.font:font(16,weight:.semibold)]).width
        let stackedKind = nameNatural + kindWidth + 12 > inner - 40
        let nameWidth = stackedKind ? inner - 40 : inner - 40 - kindWidth - 12
        let contextWidth = stackedKind ? inner - 40 - kindWidth - 12 : inner - 40
        let nameHeight = max(24, height(name.stringValue, width: nameWidth, font: font(16, weight: .semibold)))
        let contextHeight = max(stackedKind ? 20 * scale : 0,height(detail.sender, width: contextWidth, font: font(12, mono: true)))
        let headerHeight = nameHeight + contextHeight + 20
        let footerHeight: CGFloat = 32 * scale + 20
        let replyHeight = replyKind.isEmpty ? 0 : min(6 * 20 * scale, max(3 * 20 * scale, height(composer.string, width: inner - 28, font: font(14)))) + 16
        let rowHeights = choices.enumerated().map { index,choice in
            max(40 * scale, height("\(index + 1)  \(choice)", width: inner - 16 - 40 * scale,
                                   font: font(14,weight:index == selected ? .medium : .regular), line: 1.35) + 16 * scale)
        }
        let choicesHeight = rowHeights.reduce(0, +) + CGFloat(max(0,radios.count - 1)) * 4
        let contentExtra = (radios.isEmpty ? 0 : choicesHeight + 16) + (replyHeight > 0 ? replyHeight + 16 : 0)
        let fullMessageHeight = bodyHeight(width: inner - 30)
        let chrome = headerHeight + contentExtra + footerHeight + 60
        let cap = min(680 * scale, screen.height * 0.85)
        let desired = min(cap, max(320, chrome + min(fullMessageHeight, 320 * scale)))
        if fit { panel.setContentSize(NSSize(width: width, height: desired)) }
        let total = root.bounds.height
        let wasOverflow = overflow
        overflow = chrome + 120 * scale > total
        mark.frame = NSRect(x: inset, y: total - inset - 28, width: 28, height: 28)
        name.frame = NSRect(x: inset + 40, y: total - inset - nameHeight, width: nameWidth, height: nameHeight)
        kind.frame = NSRect(x: width - inset - kindWidth,
                            y: stackedKind ? total - inset - nameHeight - 4 - 20 * scale : total - inset - 20 * scale,
                            width: kindWidth, height: 20 * scale)
        context.frame = NSRect(x: inset + 40, y: total - inset - nameHeight - 4 - contextHeight, width: contextWidth, height: contextHeight)
        separator.frame = NSRect(x: inset, y: total - headerHeight - inset, width: inner, height: 1)
        let bottom = footerHeight + inset, top = total - headerHeight - 36
        let available = max(40, top - bottom)
        if overflow {
            overflowScroll.isHidden = false
            overflowScroll.frame = NSRect(x: inset, y: bottom, width: inner, height: available)
            overflowScroll.tile()
            let viewportWidth = overflowScroll.contentSize.width
            let docHeight = fullMessageHeight + contentExtra
            document.frame = NSRect(x: 0, y: 0, width: viewportWidth, height: docHeight)
            for view in [messageScroll, replyScroll, choiceGroup] { if view.superview !== document { document.addSubview(view) } }
            placeContent(parentWidth: viewportWidth, top: docHeight, messageHeight: fullMessageHeight, rowHeights: rowHeights, replyHeight: replyHeight)
            if !wasOverflow || fit { overflowScroll.contentView.scroll(to: NSPoint(x: 0, y: max(0, docHeight - available))) }
        } else {
            overflowScroll.isHidden = true
            for view in [messageScroll, replyScroll, choiceGroup] { if view.superview !== root { root.addSubview(view) } }
            placeContent(parentWidth: inner, top: available, messageHeight: max(40, available - contentExtra), rowHeights: rowHeights, replyHeight: replyHeight)
            for view in [messageScroll, replyScroll, choiceGroup] { view.frame.origin.x += inset; view.frame.origin.y += bottom }
        }
        messageScroll.isHidden = false; replyScroll.isHidden = replyKind.isEmpty
        moreFade.frame = NSRect(x: inset,y:bottom,width:inner,height:32 * scale)
        moreGradient.frame = moreFade.bounds
        let moreWidth = ceil((more.stringValue as NSString).size(withAttributes:[.font:font(11,weight:.medium,mono:true)]).width) + 8
        more.frame = NSRect(x: inner - moreWidth - 20,y:4,width:moreWidth,height:20 * scale)
        updateMore()
        choiceGroup.isHidden = radios.isEmpty
        var right = width - inset
        for button in actionButtons.reversed() {
            let buttonWidth = max(96, (button.title as NSString).size(withAttributes: [.font:font(13,weight:.medium)]).width + 24)
            button.frame = NSRect(x: right - buttonWidth, y: inset, width: buttonWidth, height: 32 * scale)
            right -= buttonWidth + 8
        }
        hint.frame = NSRect(x: inset, y: inset + 7 * scale, width: max(0,right - inset), height: 18 * scale)
        message.nextKeyView = radios.first ?? (replyKind.isEmpty ? actionButtons.first : composer)
        for radio in radios { radio.nextKeyView = composer }
        composer.nextKeyView = actionButtons.first
        for i in actionButtons.indices { actionButtons[i].nextKeyView = i + 1 < actionButtons.count ? actionButtons[i + 1] : message }
        panel.contentMinSize = NSSize(width: width, height: min(320, screen.height * 0.85))
        panel.contentMaxSize = NSSize(width: width, height: screen.height - 32)
    }
    func placeContent(parentWidth: CGFloat, top: CGFloat, messageHeight: CGFloat, rowHeights: [CGFloat], replyHeight: CGFloat) {
        var y = top
        messageScroll.frame = NSRect(x: 0, y: y - messageHeight, width: parentWidth, height: messageHeight); y -= messageHeight
        message.frame = NSRect(x: 0, y: 0, width: parentWidth - 16, height: max(messageHeight, bodyHeight(width: parentWidth - 44)))
        message.textContainer?.containerSize = NSSize(width: parentWidth - 44, height: .greatestFiniteMagnitude)
        if !radios.isEmpty { y -= 16 }
        let rowsHeight = rowHeights.reduce(0,+) + CGFloat(max(0,radios.count - 1)) * 4
        choiceGroup.frame = NSRect(x: 0, y: y - rowsHeight, width: parentWidth, height: rowsHeight)
        var rowTop = rowsHeight
        for (index, radio) in radios.enumerated() { radio.frame = NSRect(x: 0, y: rowTop - rowHeights[index], width: parentWidth, height: rowHeights[index]); rowTop -= rowHeights[index] + 4 }
        y -= rowsHeight
        if replyHeight > 0 { y -= 16 }
        replyScroll.frame = NSRect(x: 0, y: y - replyHeight, width: parentWidth, height: replyHeight)
        composer.frame = NSRect(x: 0, y: 0, width: parentWidth - 16, height: max(replyHeight, height(composer.string, width: parentWidth - 36, font: font(14)) + 16))
        composer.textContainer?.containerSize = NSSize(width: parentWidth - 36, height: .greatestFiniteMagnitude)
    }
    func bodyHeight(width: CGFloat) -> CGFloat {
        // usedRect on the live text view describes the portion already laid
        // out for its viewport. Measure the entire attributed document before
        // sizing that viewport, otherwise the first page becomes its ceiling.
        let rect = message.attributedString().boundingRect(with: NSSize(width: max(1,width), height: CGFloat.greatestFiniteMagnitude),
                                                           options: [.usesLineFragmentOrigin, .usesFontLeading])
        return max(42 * scale, ceil(rect.height) + 24)
    }
    func updateEnabled() {
        let text = !composer.string.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        let choice = selected.map { choices[$0] } ?? ""
        let bytes = composer.string.utf8.count + choice.utf8.count + (text && !choice.isEmpty ? 2 : 0)
        let bounded = bytes <= (replyKind == "request" ? 32640 : 32768)
        for button in actionButtons {
            if button.title == "Send" { button.isEnabled = bounded && (selected != nil || text); button.setAccessibilityHelp(!bounded ? "Shorten the reply before sending" : button.isEnabled ? "Send reply" : "Choose an option or type a reply first") }
            if button.title == "Reply without deciding" { button.isEnabled = text && bounded }
            if button.title == "Approve" || button.title == "Deny" { button.isEnabled = bounded }
        }
    }
    func updateMore() { moreFade.isHidden = !overflow || overflowScroll.contentView.bounds.minY <= 1 }
    @objc func selectChoice(_ sender: NSButton) {
        selected = sender.tag
        for (index, radio) in radios.enumerated() { radio.state = index == selected ? .on : .off }
        for (index, radio) in radios.enumerated() { (radio as? PromptChoiceButton)?.tabStop = index == selected }
        applyStyle()
        if overflow { sender.scrollToVisible(sender.bounds) }
    }
    @objc func action(_ sender: NSButton) {
        guard sender.isEnabled else { return }
        switch sender.title {
        case "Send": send()
        case "Cancel", "Close": complete(HumanPromptReply())
        case "Approve" where replyKind == "request": complete(HumanPromptReply(action: "approve", text: composer.string))
        case "Deny" where replyKind == "request": complete(HumanPromptReply(action: "deny", text: composer.string))
        case "Reply without deciding": if sender.isEnabled { complete(HumanPromptReply(action: "comment", text: composer.string)) }
        default: complete(HumanPromptReply(action: sender.title))
        }
    }
    func send() {
        guard replyKind == "question", actionButtons.first(where: { $0.title == "Send" })?.isEnabled == true else { return }
        complete(HumanPromptReply(action: "answer", choice: selected.map { choices[$0] } ?? "", text: composer.string))
    }
    func key(_ event: NSEvent) -> Bool {
        if event.keyCode == 53 || (event.modifierFlags.contains(.command) && event.charactersIgnoringModifiers == ".") { complete(HumanPromptReply()); return true }
        if panel.firstResponder === composer, (event.keyCode == 36 || event.keyCode == 76),
           !event.modifierFlags.intersection([.shift,.option]).isEmpty {
            composer.insertNewline(nil); return true
        }
        if event.modifierFlags.contains(.command), event.keyCode == 36,
           let button = panel.firstResponder as? NSButton, actionButtons.contains(button), button.isEnabled { button.performClick(nil); return true }
        if replyKind == "request", (event.keyCode == 36 || event.keyCode == 76),
           event.modifierFlags.intersection([.shift,.option]).isEmpty { return true }
        if panel.firstResponder !== composer && !radios.isEmpty {
            if let value = Int(event.charactersIgnoringModifiers ?? ""), value > 0, value <= radios.count { radios[value - 1].performClick(nil); return true }
            if event.keyCode == 125 || event.keyCode == 126 {
                let index = max(0, min(radios.count - 1, (selected ?? (event.keyCode == 125 ? -1 : 1)) + (event.keyCode == 125 ? 1 : -1)))
                radios[index].performClick(nil); panel.makeFirstResponder(radios[index]); return true
            }
        }
        return false
    }
    func complete(_ reply: HumanPromptReply) { guard !finished else { return }; finished = true; sent = !reply.action.isEmpty; picked(reply) }
    func textDidChange(_ notification: Notification) {
        layout(fit: false); composer.needsDisplay = true
        if overflow { replyScroll.scrollToVisible(replyScroll.bounds) }
    }
    func windowDidResize(_ notification: Notification) {
        if !layingOut && panel.isVisible { UserDefaults.standard.set(root.bounds.height, forKey: "DibsPromptHeight") }
        layout(fit: false)
    }
    func windowDidChangeEffectiveAppearance(_ notification: Notification) { layout(fit: false) }
    func windowShouldClose(_ sender: NSWindow) -> Bool { complete(HumanPromptReply()); return false }
    func show() {
        // Every caller of show is a native banner/default-action callback or
        // an explicit --prompt/--pick gesture, never mail arrival.
        let screen = panel.screen?.visibleFrame ?? NSScreen.main?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1280, height: 800)
        if !shown {
            let saved = UserDefaults.standard.double(forKey: "DibsPromptHeight")
            if saved >= 320 { panel.setContentSize(NSSize(width: preferredWidth, height: min(screen.height - 48, saved))); layout(fit: false) }
        }
        let offset = CGFloat(Self.cascade % 6) * 24
        let origin = NSPoint(x: min(screen.maxX - panel.frame.width - 16, screen.midX - panel.frame.width / 2 + offset),
                             y: max(screen.minY + 16, screen.maxY - screen.height * 0.22 - panel.frame.height - offset))
        panel.setFrameOrigin(origin)
        panel.level = .floating
        let animate = !shown && !NSWorkspace.shared.accessibilityDisplayShouldReduceMotion
        if animate { panel.alphaValue = 0; panel.setFrameOrigin(NSPoint(x: origin.x, y: origin.y - 6)) }
        NSApp.activate(ignoringOtherApps: true); panel.makeKeyAndOrderFront(nil)
        panel.makeFirstResponder(radios.first ?? (replyKind.isEmpty ? actionButtons.first : composer))
        if animate { NSAnimationContext.runAnimationGroup { context in
            context.duration = 0.18; context.timingFunction = CAMediaTimingFunction(controlPoints: 0.32, 0.72, 0, 1)
            panel.animator().alphaValue = 1; panel.animator().setFrameOrigin(origin)
        } }
        if !shown { Self.cascade = (Self.cascade + 1) % 6 }; shown = true
    }
    func close() {
        panel.delegate = nil
        if panel.isVisible && sent && NSApp.modalWindow == nil && !NSWorkspace.shared.accessibilityDisplayShouldReduceMotion {
            NSAnimationContext.runAnimationGroup({ context in context.duration = 0.12; panel.animator().alphaValue = 0 }, completionHandler: { [panel] in panel.orderOut(nil); panel.close() })
        } else { panel.orderOut(nil); panel.close() }
    }
    func messageEndRect() -> NSRect {
        guard let container = message.textContainer, let manager = message.layoutManager else { return .zero }
        let text = message.string as NSString
        let glyphs = manager.glyphRange(forCharacterRange: NSRange(location: max(0,text.length - 14), length: min(14,text.length)), actualCharacterRange: nil)
        return manager.boundingRect(forGlyphRange: glyphs, in: container).offsetBy(dx: message.textContainerOrigin.x, dy: message.textContainerOrigin.y)
    }
    func revealMessageEnd() { message.scrollToVisible(messageEndRect().insetBy(dx: 0,dy: -12)) }
    var viewInfo: [String: Any] {
        ["title": heading, "sender": detail.sender, "body": detail.body, "renderedBody": message.string,
         "buttons": actionButtons.map { $0.title }, "choices": choices,
         "reply": !replyKind.isEmpty, "overflow": overflow, "width": panel.contentView!.bounds.width,
         "endVisible": message.visibleRect.intersects(messageEndRect()), "selected": selected as Any? ?? NSNull(),
         "replyVisibleHeight": replyScroll.visibleRect.height, "replyHeight": replyScroll.bounds.height,
         "replyText": composer.string,
         "choiceGroupRole": choiceGroup.accessibilityRole()?.rawValue ?? "", "headerRole": name.accessibilityRole()?.rawValue ?? "",
         "actionsFit": actionButtons.allSatisfy { root.bounds.contains($0.frame) },
         "kindFits": root.bounds.contains(kind.frame) && kind.frame.width >= (kind.stringValue as NSString).size(withAttributes:[.font:kind.font!]).width,
         "choiceLabelsFit": radios.allSatisfy { radio in
             let required = (radio.title as NSString).boundingRect(with: NSSize(width:radio.bounds.width - 40 * scale,height:CGFloat.greatestFiniteMagnitude),
                 options:[.usesLineFragmentOrigin,.usesFontLeading],attributes:[.font:radio.font!]).height
             return required <= radio.bounds.height - 16 * scale
         },
         "endBottomInset": message.visibleRect.maxY - messageEndRect().maxY,
         "moreBelow": !moreFade.isHidden, "firstChoiceVisible": radios.first.map { $0.visibleRect.height >= $0.bounds.height - 1 } ?? true,
         "overflowLaneX": overflowScroll.frame.maxX - 2, "overflowLaneY": root.bounds.height - overflowScroll.frame.midY,
         "fontFamily": font(14).familyName ?? "unknown",
         "height": panel.contentView!.bounds.height, "bodyHeight": message.frame.height, "documentHeight": document.frame.height, "renderedLength": message.string.utf8.count, "defaultApproval": replyKind == "request" && panel.defaultButtonCell != nil]
    }
}
