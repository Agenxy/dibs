module github.com/agenxy/dibs

go 1.26

toolchain go1.26.6

// v0.0.0 was published, cached by the Go module proxy, and then the tag was
// moved to a later commit. The proxy and sum.golang.org are append-only, so the
// checksum they recorded can never agree with the tag again: `go install
// ...@v0.0.0` with GOPROXY=direct fails with a SECURITY ERROR, and through the
// default proxy it silently serves the older tree. Neither is acceptable, and
// neither is fixable: a moved tag is permanent. Use v0.0.1 or later.
//
// v0.0.1 is NOT merely v0.0.0 plus this line, which an earlier version of this
// comment claimed: it also carries `dibs stop`, service-unit generation, a
// path-canonicalisation fix and documentation corrections. Saying otherwise
// invited somebody to conclude the retraction was procedural and keep using a
// version that fails to install.
retract v0.0.0

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/charmbracelet/lipgloss v1.1.0
	golang.org/x/tools v0.49.0
)

require (
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/charmbracelet/colorprofile v0.2.3-0.20250311203215-f60798e515dc // indirect
	github.com/charmbracelet/x/ansi v0.8.0 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.13-0.20250311204145-2c3ea96c31dd // indirect
	github.com/charmbracelet/x/term v0.2.1 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	golang.org/x/sys v0.47.0 // indirect
)
