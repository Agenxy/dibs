// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy
import ImageIO

struct NativePromptMedia: @unchecked Sendable {
    var kind: String, mime: String
    var size: Int64
    var expired: Bool, file: Bool
    var image: NSImage?
    var loadLarger: (@Sendable () -> NSImage?)?
    var caption: String {
        if expired { return "expired" }
        if file && size == 0 { return "file reference · open on the board" }
        let bytes = ByteCountFormatter.string(fromByteCount: size, countStyle: .file)
        let label = kind.isEmpty ? "file" : kind
        return image != nil ? "\(label) · \(bytes) · click to enlarge" : "\(label) · \(bytes) · open on the board"
    }
}

// Decode raster bytes into a bounded bitmap, never an NSImage vector/document
// representation. Even a sender labelling SVG or HTML as PNG cannot render it.
@Sendable func nativeRasterThumbnail(_ data: Data, pixels: Int = 1024, allowTIFF: Bool = false) -> NSImage? {
    guard data.count <= (allowTIFF ? 64 : 16) * 1024 * 1024,
          let source = CGImageSourceCreateWithData(data as CFData, nil), let type = CGImageSourceGetType(source) else { return nil }
    let allowed = ["public.png", "public.jpeg", "com.compuserve.gif", "org.webmproject.webp", "public.avif"]
    guard allowed.contains(type as String) || (allowTIFF && type as String == "public.tiff") else { return nil }
    let options: [CFString: Any] = [kCGImageSourceCreateThumbnailFromImageAlways: true,
        kCGImageSourceThumbnailMaxPixelSize: pixels, kCGImageSourceCreateThumbnailWithTransform: true]
    guard let bitmap = CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary) else { return nil }
    return NSImage(cgImage: bitmap, size: NSSize(width: bitmap.width, height: bitmap.height))
}

func nativePromptMedia(profile: String, node: String, serial: UInt64, metadata: [[String: Any]]) -> [NativePromptMedia] {
    metadata.prefix(8).map { item in
        var result = NativePromptMedia(kind: item["kind"] as? String ?? "", mime: item["mime"] as? String ?? "",
            size: (item["size"] as? NSNumber)?.int64Value ?? 0, expired: item["expired"] as? Bool ?? false,
            file: item["file"] as? Bool ?? false)
        if result.kind == "image", !result.expired, !result.file, result.size <= 16 * 1024 * 1024,
           let value = try? nativeHumanRPC(profile: profile, method: "media", payload: ["node": node, "serial": serial, "index": item["index"] ?? 0]) {
            if (value["httpStatus"] as? Int) == 410 { result.expired = true }
            if value["ok"] as? Bool == true, let raw = value["data"] as? String, let data = Data(base64Encoded: raw) {
                result.image = nativeRasterThumbnail(data)
                let index = item["index"] as? Int ?? 0
                result.loadLarger = {
                    guard let value = try? nativeHumanRPC(profile: profile, method: "media", payload: ["node": node, "serial": serial, "index": index]),
                          value["ok"] as? Bool == true, let raw = value["data"] as? String, let data = Data(base64Encoded: raw) else { return nil }
                    return nativeRasterThumbnail(data, pixels: 4096)
                }
            }
        }
        return result
    }
}

final class PromptMediaImageView: NSImageView {
    var open: (() -> Void)?
    override func mouseDown(with event: NSEvent) { open?() }
}

final class PromptMediaRow: NSView {
    let item: NativePromptMedia
    let picture = PromptMediaImageView(), label = NSTextField(labelWithString: "")
    var open: (() -> Void)? { didSet { picture.open = open } }
    init(_ item: NativePromptMedia) {
        self.item = item; super.init(frame: .zero)
        picture.image = item.image; picture.imageScaling = .scaleProportionallyUpOrDown
        picture.setAccessibilityLabel("Attached image; click to enlarge")
        label.stringValue = item.caption; label.font = .systemFont(ofSize: 11); label.textColor = .secondaryLabelColor
        addSubview(picture); addSubview(label)
    }
    required init?(coder: NSCoder) { fatalError("init(coder:) is unavailable") }
    override func mouseDown(with event: NSEvent) { if item.image != nil { open?() } }
    func measuredHeight(_ width: CGFloat) -> CGFloat {
        guard let image = item.image else { return 24 }
        return min(240, width * image.size.height / max(1, image.size.width)) + 26
    }
    func place(width: CGFloat) {
        picture.frame = NSRect(x: 0, y: 26, width: width, height: max(0, bounds.height - 26))
        picture.isHidden = item.image == nil
        label.frame = NSRect(x: 0, y: 2, width: width, height: 20)
    }
}

final class PromptMediaList: NSView {
    var rows = [PromptMediaRow]()
    var preview: NSPanel?
    func set(_ items: [NativePromptMedia]) {
        rows.forEach { $0.removeFromSuperview() }; rows.removeAll()
        for item in items {
            let row = PromptMediaRow(item)
            row.open = { [weak self] in if let image = item.image { self?.enlarge(image, load: item.loadLarger) } }
            addSubview(row); rows.append(row)
        }
    }
    func measuredHeight(_ width: CGFloat) -> CGFloat { rows.reduce(0) { $0 + $1.measuredHeight(width) + 8 } }
    func place(width: CGFloat) {
        var y = bounds.height
        for row in rows {
            let height = row.measuredHeight(width)
            row.frame = NSRect(x: 0, y: y - height, width: width, height: height); row.place(width: width)
            y -= height + 8
        }
    }
    func enlarge(_ image: NSImage, load: (@Sendable () -> NSImage?)?) {
        preview?.close()
        let screen = window?.screen?.visibleFrame.size ?? NSSize(width: 1000, height: 800)
        let size = NSSize(width: min(1000, screen.width * 0.8), height: min(760, screen.height * 0.8))
        let panel = NSPanel(contentRect: NSRect(origin: .zero, size: size), styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
        let view = NSImageView(frame: NSRect(origin: .zero, size: size))
        view.image = image; view.imageScaling = .scaleProportionallyUpOrDown; view.autoresizingMask = [.width, .height]
        panel.title = "Attached image"; panel.contentView = view; panel.isReleasedWhenClosed = false
        preview = panel
        #if DIBS_READING_FIXTURE
        if ProcessInfo.processInfo.environment["DIBS_NATIVE_REOPEN_VIEW"] != nil { return }
        #endif
        panel.center(); panel.makeKeyAndOrderFront(nil)
        if let load {
            DispatchQueue.global(qos: .userInitiated).async { [weak view] in
                guard let larger = load() else { return }
                DispatchQueue.main.async { view?.image = larger }
            }
        }
    }
    var viewInfo: [[String: Any]] {
        rows.map { ["kind": $0.item.kind, "mime": $0.item.mime, "size": $0.item.size,
                    "expired": $0.item.expired, "file": $0.item.file,
                    "rendered": $0.picture.image != nil && $0.picture.frame.height > 0,
                    "width": $0.picture.frame.width, "height": $0.picture.frame.height] }
    }
}
