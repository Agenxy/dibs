// Command appbundle builds Dibs.app: the process that speaks to the person.
//
// A notification has an identity. Whoever posts it lends it their name and
// their icon, so a daemon shelling out to osascript borrows Script Editor's,
// and every message from an agent arrived branded "osascript" with osascript's
// icon. There is no flag that changes that: the poster's bundle IS the identity.
//
// The bundle buys the other half too. UNUserNotificationCenter needs a bundle
// identifier and crashes without one, and it is the only API that puts ACTION
// BUTTONS on the banner itself. "Make it look like Dibs" and "let the human
// answer without opening anything" are the same change.
//
// A Go tool rather than a Taskfile of shell: assembling an iconset is a loop
// over ten sizes with a naming convention, and this project does not put logic
// in shell (CONTRIBUTING.md).
//
// macOS only, and it says so rather than failing obscurely: there is no
// equivalent on Linux, where the daemon falls back to no notifications at all.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

// iconSizes are the ten an .icns carries. The names are iconutil's convention
// and are not negotiable: it reads the set by filename.
var iconSizes = []struct {
	px   int
	name string
}{
	{16, "icon_16x16.png"},
	{32, "icon_16x16@2x.png"},
	{32, "icon_32x32.png"},
	{64, "icon_32x32@2x.png"},
	{128, "icon_128x128.png"},
	{256, "icon_128x128@2x.png"},
	{256, "icon_256x256.png"},
	{512, "icon_256x256@2x.png"},
	{512, "icon_512x512.png"},
	{1024, "icon_512x512@2x.png"},
}

// plist is the smallest Info.plist that makes this a real application.
//
// LSUIElement is the one that matters for a coordination service: without it
// posting a notification bounces an icon into the Dock and steals focus, which
// is precisely the interruption the notification exists to avoid.
const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Dibs</string>
  <key>CFBundleDisplayName</key><string>Dibs</string>
  <key>CFBundleIdentifier</key><string>org.agenxy.dibs</string>
  <key>CFBundleExecutable</key><string>dibs-notify</string>
  <key>CFBundleIconFile</key><string>Dibs</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>%s</string>
  <key>CFBundleVersion</key><string>%s</string>
  <key>NSHumanReadableCopyright</key><string>Agenxy. Apache-2.0.</string>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
  <key>LSUIElement</key><true/>
  <!-- Correct metadata, and NOT a Focus bypass. An app cannot declare itself
       into a Focus mode: the only things that break through are the Time
       Sensitive entitlement, which Apple gates behind a paid developer
       account, and the user adding Dibs to that mode's allowed apps. Dibs
       escalates to a window instead, which Focus does not silence. -->
  <key>LSApplicationCategoryType</key><string>public.app-category.productivity</string>
</dict>
</plist>
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "appbundle:", err)
		os.Exit(1)
	}
}

func run() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("the app bundle is macOS only; nothing to build on %s", runtime.GOOS)
	}
	out := flagValue("-o", "bin/Dibs.app")
	version := flagValue("-version", "0.0.0")
	src := flagValue("-src", "internal/notify/app")

	work, err := os.MkdirTemp("", "dibs-app")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	// The icon renderer is a build-time tool: it draws the mark and exits, and
	// nothing ships it.
	iconBin := filepath.Join(work, "dibs-icon")
	if err := swiftc(iconBin, filepath.Join(src, "icon_darwin.swift")); err != nil {
		return err
	}
	iconset := filepath.Join(work, "Dibs.iconset")
	if err := os.MkdirAll(iconset, 0o750); err != nil {
		return err
	}
	for _, s := range iconSizes {
		// #nosec G204 -- iconBin is a path this function just built; the size is
		// from the table above.
		if err := exec.Command(iconBin, filepath.Join(iconset, s.name), strconv.Itoa(s.px)).Run(); err != nil {
			return fmt.Errorf("rendering %s: %w", s.name, err)
		}
	}
	icns := filepath.Join(work, "Dibs.icns")
	// #nosec G204 -- both paths are inside this tool's own temp directory.
	if o, err := exec.Command("iconutil", "-c", "icns", iconset, "-o", icns).CombinedOutput(); err != nil {
		return fmt.Errorf("iconutil: %w: %s", err, o)
	}

	macos := filepath.Join(out, "Contents", "MacOS")
	res := filepath.Join(out, "Contents", "Resources")
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	for _, d := range []string{macos, res} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}
	if err := swiftc(filepath.Join(macos, "dibs-notify"),
		filepath.Join(src, "notify_darwin.swift")); err != nil {
		return err
	}
	if err := copyFile(icns, filepath.Join(res, "Dibs.icns")); err != nil {
		return err
	}
	body := fmt.Sprintf(plist, version, version)
	if err := os.WriteFile(filepath.Join(out, "Contents", "Info.plist"), []byte(body), 0o600); err != nil {
		return err
	}
	// Signed with whatever identity the caller selected, falling back to ad-hoc
	// so the bundle is still a valid application macOS will run.
	//
	// The fallback used to be the only path in practice, because nothing set
	// the variable: `task app` now passes tools/signid's answer, the same one
	// `task install` signs the binaries with. That matters more here than
	// anywhere else, since notification authorisation is remembered against the
	// signature and this bundle exists to raise notifications: an ad-hoc
	// rebuild is a different app to macOS, and the grant is asked for again.
	// #nosec G204 -- identity() is DIBS_CODESIGN_IDENTITY or "-", and `out` is
	// this tool's own -o flag. A build tool run by the maintainer.
	if o, err := exec.Command("codesign", "--force", "--sign", identity(), out).CombinedOutput(); err != nil {
		return fmt.Errorf("codesign: %w: %s", err, o)
	}
	fmt.Println("built", out)
	return nil
}

func identity() string {
	if id := os.Getenv("DIBS_CODESIGN_IDENTITY"); id != "" {
		return id
	}
	return "-"
}

// swiftc builds the notifier for the one Mac Dibs ships to.
//
// AN EXPLICIT TARGET, not the build host's default. Built without one this was
// whatever the release runner happened to be, and the release copies a single
// bundle into every archive: `dibs-notify` was arm64-only inside the Intel
// tarball, where the passive path returns the exec error rather than falling
// back to osascript, so the notifier was simply absent on that platform while
// every check was green.
//
// The answer is not a fat binary. Dibs does not ship a Mac Intel build at all
// (see the `ignore` in .goreleaser.yml), so there is one target and stating it
// is what keeps the artifact a property of this file rather than of the machine
// that ran it.
//
// macos12, where the presence helper uses macos11: this source calls
// interruptionLevel and timeSensitiveSetting unconditionally, which are macOS
// 12 APIs, so 12 is what it already required at runtime. The host default hid
// that too.
func swiftc(out, src string) error {
	// #nosec G204 -- a fixed target triple, and paths built from this tool's own
	// flags and the fixed source directory.
	if o, err := exec.Command("swiftc", "-O", "-target", "arm64-apple-macos12",
		"-o", out, src).CombinedOutput(); err != nil {
		return fmt.Errorf("swiftc %s: %w: %s", src, err, o)
	}
	return nil
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from) // #nosec G304 -- a file this tool just produced
	if err != nil {
		return err
	}
	// #nosec G703 -- both paths are this tool's own temp dir and -o flag.
	return os.WriteFile(to, b, 0o600)
}

// flagValue is a two-line flag reader, because this tool takes three options
// and importing flag would mean fighting it over the -o convention.
func flagValue(name, fallback string) string {
	for i, a := range os.Args {
		if a == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return fallback
}
