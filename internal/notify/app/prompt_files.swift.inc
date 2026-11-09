// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy
import UniformTypeIdentifiers

func nativeFilePasteboard(_ board: NSPasteboard) -> Bool {
    board.availableType(from: [.fileURL, .png, .tiff]) != nil
}

struct NativeFileError: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

// A selected file is a URL until Send, so eight large files do not occupy eight
// byte buffers while the person edits or walks to another message.
struct NativeHumanFile: Sendable {
    let name: String, mime: String
    let size: Int
    let url: URL?
    let pasted: Data?
    static func selected(_ url: URL, limit: Int) throws -> NativeHumanFile {
        let info = try url.resourceValues(forKeys: [.isRegularFileKey, .fileSizeKey])
        guard info.isRegularFile == true, let size = info.fileSize, size <= limit else {
            throw NativeFileError(message: "Choose a regular file within the attachment limit")
        }
        let mime = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType ?? "application/octet-stream"
        return NativeHumanFile(name: url.lastPathComponent, mime: mime, size: size, url: url, pasted: nil)
    }
    func bytes(limit: Int) throws -> Data {
        if let pasted { guard pasted.count <= limit else { throw NativeFileError(message: "Image exceeds the attachment limit") }; return pasted }
        guard let url else { throw NativeFileError(message: "File is unavailable") }
        let handle = try FileHandle(forReadingFrom: url); defer { try? handle.close() }
        var data = Data()
        while data.count <= limit {
            guard let chunk = try handle.read(upToCount: min(1024 * 1024, limit + 1 - data.count)), !chunk.isEmpty else { break }
            data.append(chunk)
        }
        guard data.count <= limit else { throw NativeFileError(message: "File grew beyond the attachment limit; choose a smaller file") }
        return data
    }
}

final class PromptFileChips: NSView {
    var labels = [NSTextField](), buttons = [NSButton]()
    var remove: ((Int) -> Void)?
    func set(_ files: [NativeHumanFile]) {
        subviews.forEach { $0.removeFromSuperview() }; labels.removeAll(); buttons.removeAll()
        for (index, file) in files.enumerated() {
            let label = NSTextField(labelWithString: file.name)
            label.lineBreakMode = .byTruncatingMiddle; label.font = .systemFont(ofSize: 12)
            let button = NSButton(title: "×", target: self, action: #selector(removeFile(_:)))
            button.tag = index; button.setAccessibilityLabel("Remove \(file.name)")
            addSubview(label); addSubview(button); labels.append(label); buttons.append(button)
        }
    }
    var measuredHeight: CGFloat { CGFloat(labels.count) * 30 }
    func place(width: CGFloat) {
        for index in labels.indices {
            let y = bounds.height - CGFloat(index + 1) * 30
            labels[index].frame = NSRect(x: 4, y: y + 5, width: max(0, width - 42), height: 20)
            buttons[index].frame = NSRect(x: width - 32, y: y, width: 30, height: 28)
        }
    }
    @objc func removeFile(_ sender: NSButton) { remove?(sender.tag) }
}

extension HumanPromptWindow {
    func setFiles(_ files: [NativeHumanFile]) {
        self.files = files; fileChips.set(files); updateEnabled(); layout(fit: false)
        draftChanged?()
    }
    func addFiles(_ urls: [URL]) {
        do {
            guard files.count + urls.count <= fileCountLimit else { throw NativeFileError(message: "Attach at most \(fileCountLimit) files") }
            let chosen = try urls.map { try NativeHumanFile.selected($0, limit: fileByteLimit) }
            setFiles(files + chosen)
        } catch { hint.stringValue = error.localizedDescription; layout(fit: false) }
    }
    func pasteImage(_ pasteboard: NSPasteboard = .general) -> Bool {
        guard allowAttachments, !submitting, let data = pasteboard.data(forType: .png) ?? pasteboard.data(forType: .tiff) else { return false }
        guard files.count < fileCountLimit, let image = nativeRasterThumbnail(data, pixels: 4096, allowTIFF: true),
              let bitmap = image.cgImage(forProposedRect: nil, context: nil, hints: nil),
              let png = NSBitmapImageRep(cgImage: bitmap).representation(using: .png, properties: [:]), png.count <= fileByteLimit else {
            hint.stringValue = "Image exceeds the attachment limit or is not a raster image"; return true
        }
        setFiles(files + [NativeHumanFile(name: "Pasted image.png", mime: "image/png", size: png.count, url: nil, pasted: png)])
        return true
    }
    @objc func chooseFiles(_ sender: Any?) {
        guard !submitting else { return }
        let picker = NSOpenPanel(); picker.canChooseFiles = true; picker.canChooseDirectories = false
        picker.allowsMultipleSelection = true
        picker.beginSheetModal(for: panel) { [weak self] response in if response == .OK { self?.addFiles(picker.urls) } }
    }
    func acceptDrop(_ pasteboard: NSPasteboard) -> Bool {
        guard allowAttachments, !submitting else { return false }
        if let urls = pasteboard.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true]) as? [URL], !urls.isEmpty {
            addFiles(urls); return true
        }
        return pasteImage(pasteboard)
    }
}
