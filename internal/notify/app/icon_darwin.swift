// dibs-icon: renders the product mark to PNG, at whatever size is asked for.
//
// The mark is defined once, in internal/assets/icon.svg, and drawn here rather
// than parsed from it. That is a deliberate duplication of two primitives: a
// rounded tile and three circles. Parsing SVG would mean shipping a parser or
// a build dependency on librsvg to produce an icon, and the whole geometry is
// nine numbers.
//
// `TestTheAppIconMatchesTheProductMark` keeps the two honest by comparing the
// numbers in this file with the ones in the SVG, so a mark that changes in one
// place fails rather than diverging quietly. The colours are the board's own
// tokens, converted from OKLCH, and are the same three the SVG carries.
import AppKit
import ImageIO
import UniformTypeIdentifiers

let out = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : ""
let px = CommandLine.arguments.count > 2 ? Int(CommandLine.arguments[2]) ?? 1024 : 1024
guard !out.isEmpty else {
    FileHandle.standardError.write("usage: dibs-icon <out.png> <pixels>\n".data(using: .utf8)!)
    exit(2)
}

let side = CGFloat(px)
let scale = side / 32.0   // the mark is authored in a 32-unit box

guard let ctx = CGContext(data: nil, width: px, height: px, bitsPerComponent: 8,
                          bytesPerRow: 0, space: CGColorSpaceCreateDeviceRGB(),
                          bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else {
    exit(1)
}
// Flip to the SVG's coordinate system: y down from the top left.
ctx.translateBy(x: 0, y: side)
ctx.scaleBy(x: scale, y: -scale)

func rgb(_ hex: UInt32) -> CGColor {
    CGColor(red: CGFloat((hex >> 16) & 0xFF) / 255, green: CGFloat((hex >> 8) & 0xFF) / 255,
            blue: CGFloat(hex & 0xFF) / 255, alpha: 1)
}

// <rect width="32" height="32" rx="7.5" fill="#131518"/>
ctx.setFillColor(rgb(0x131518))
ctx.addPath(CGPath(roundedRect: CGRect(x: 0, y: 0, width: 32, height: 32),
                   cornerWidth: 7.5, cornerHeight: 7.5, transform: nil))
ctx.fillPath()

func dot(_ cx: CGFloat, _ cy: CGFloat, _ r: CGFloat, _ hex: UInt32) {
    ctx.setFillColor(rgb(hex))
    ctx.fillEllipse(in: CGRect(x: cx - r, y: cy - r, width: r * 2, height: r * 2))
}

// Three agents, one of them holding: the accent is the holder, the two
// peers are equal to each other because neither is subordinate.
// <circle cx="16" cy="9" r="4.3" fill="#83a7ea"/>
dot(16, 9, 4.3, 0x83A7EA)
// <circle cx="9.6" cy="20.2" r="4.3" fill="#9aa0a8"/>
dot(9.6, 20.2, 4.3, 0x9AA0A8)
// <circle cx="22.4" cy="20.2" r="4.3" fill="#9aa0a8"/>
dot(22.4, 20.2, 4.3, 0x9AA0A8)

guard let image = ctx.makeImage(),
      let dest = CGImageDestinationCreateWithURL(
          URL(fileURLWithPath: out) as CFURL, "public.png" as CFString, 1, nil) else {
    exit(1)
}
CGImageDestinationAddImage(dest, image, nil)
exit(CGImageDestinationFinalize(dest) ? 0 : 1)
