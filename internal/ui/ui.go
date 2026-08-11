// Package ui is the terminal's half of the design system.
//
// The board and the panel already share one component library so the two
// browser surfaces cannot drift. The terminal is the third surface: the one an
// operator on a server, over ssh, or in a pipeline actually has, and it was
// the only one with no shared vocabulary at all: `doctor` wrote raw ANSI escape
// codes inline, nothing else was styled, and nothing anywhere checked whether
// the output was going to a terminal or into a file.
//
// So the same rule applies here as there: colour MEANS something, and it means
// the same thing on every surface.
//
//	quiet   context: true, but nobody must act on it
//	good    working as intended
//	attn    outstanding: somebody will get to it
//	alarm   nothing will resolve this without a person
//
// DEGRADATION IS THE POINT. A coordination tool gets piped, teed into logs, and
// run in CI. lipgloss resolves the colour profile from the writer: a pipe, a
// dumb terminal, NO_COLOR or CLICOLOR_FORCE all resolve correctly, so styled
// output collapses to exactly the plain text it would have been. `dibs board |
// grep builder` finds builder, and a redirected `dibs doctor` writes readable
// text rather than escape sequences.
package ui

import (
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// r renders against stdout, which is what decides the colour profile: lipgloss
// asks the writer, not the environment, so redirecting is detected rather than
// guessed at.
var r = lipgloss.NewRenderer(os.Stdout)

// The palette is adaptive: the same style is legible on a light terminal and a
// dark one, which is not a nicety when half the fleet's operators run one and
// half the other.
// The palette is DERIVED from the browser one, not chosen alongside it.
//
// The two were picked independently, so "the same green" was two different
// greens and the three surfaces were three products wearing one name. These are
// the nearest xterm-256 entries to the exact hexes in board.css: computed, not
// eyeballed, so an agent that is live is the same green in the terminal, the
// web board and the MCP panel.
//
//	role    board.css dark → 256   board.css light → 256
//	accent  #8FA8D4       110      #3C61A8        61
//	live    #6BBB8E        72      #2C7B50        29
//	warn    #DE7A60       173      #B04527       130
//	fg-2    #99A0A8       247      #545A62       240
//	fg-3    #666D75       242      #838A93       245
//
// alarm has no browser twin: the board carries "needs a person" as a red PILL
// rather than red text, and a terminal has no pills. It stays a deeper red than
// warn so the two remain distinguishable on a 256-colour terminal, where 173 and
// 203 are far enough apart to survive a low-contrast profile.
var (
	dim   = r.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "242"})
	good  = r.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "29", Dark: "72"})
	attn  = r.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "130", Dark: "173"})
	alarm = r.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"})
	acc   = r.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "61", Dark: "110"})
	bold  = r.NewStyle().Bold(true)
)

// Dim is context the reader does not act on.
func Dim(s string) string { return dim.Render(s) }

// Good is working as intended.
func Good(s string) string { return good.Render(s) }

// Attn is outstanding: real, but somebody is expected to get to it.
func Attn(s string) string { return attn.Render(s) }

// Alarm is the one weight that means nothing will resolve this without a
// person. Used sparingly on purpose: a board where everything is red is a
// board people stop reading.
func Alarm(s string) string { return alarm.Render(s) }

// Accent marks structure: identifiers, the thing a line is about.
func Accent(s string) string { return acc.Render(s) }

// Bold is emphasis that survives a terminal with no colour at all.
func Bold(s string) string { return bold.Render(s) }

// Marks carry a SYMBOL as well as a colour, because a reader who is
// colour-blind, redirecting to a file, or on a dumb terminal still has to be
// able to tell them apart.

// OK marks something working as intended.
func OK(s string) string { return "  " + good.Render("✓") + " " + s }

// Bad marks a finding that needs a person.
func Bad(s string) string { return "  " + alarm.Render("✗") + " " + s }

// Warn marks something true and worth knowing that nobody must act on now.
func Warn(s string) string { return "  " + attn.Render("!") + " " + s }

// Note marks plain context.
func Note(s string) string { return "  " + dim.Render("·") + " " + s }

// Fix is the second half of a finding: what to DO about it. Always on its own
// line and always indented under the finding, because a diagnostic that names a
// fault without naming the remedy is half a diagnostic.
func Fix(s string) string { return "      " + dim.Render("→ "+s) }

// Section is a titled break. Underlined with a rule rather than boxed: a box
// around output that is going to be grepped is a box somebody has to strip.
func Section(title string) string {
	return "\n" + bold.Render(title) + "\n" + dim.Render(strings.Repeat("─", lipgloss.Width(title)))
}

// Field renders a label and its value, aligned to width. Uses lipgloss.Width
// rather than len so a name with wide or combining characters still lines up,
// agent names come from whatever the operator typed, and len() counts bytes.
func Field(label string, width int, value string) string {
	pad := width - lipgloss.Width(label)
	if pad < 1 {
		pad = 1
	}
	return dim.Render(label) + strings.Repeat(" ", pad) + value
}

// Pad right-pads to a display width, unicode-aware. The reason this is not
// fmt's %-Ns: %-Ns counts BYTES, so one non-ASCII agent name knocks every
// column out of alignment for the rest of the board.
func Pad(s string, width int) string {
	if n := width - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// Enabled reports whether colour is actually being emitted, for callers that
// want to vary layout rather than just colour.
func Enabled() bool { return r.ColorProfile() != 0 /* termenv.Ascii */ }

// SetOutput repoints the renderer, for tests and for commands that write
// somewhere other than stdout.
func SetOutput(w io.Writer) { r = lipgloss.NewRenderer(w) }

// Path shortens a filesystem path for display without making it ambiguous.
//
// Coordination paths are long: a claim inside a temp checkout ran to 96
// characters and pushed everything after it off the line, which is how a
// column-aligned board stops being aligned. Shortening is done in the order a
// reader would: relative to where they are standing, then relative to home,
// then by dropping the MIDDLE, never the end. The end is the part that says
// which directory this actually is.
func Path(p string) string {
	if p == "" {
		return p
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "/" {
		if rel := strings.TrimPrefix(p, cwd+string(os.PathSeparator)); rel != p {
			return "./" + rel
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel := strings.TrimPrefix(p, home+string(os.PathSeparator)); rel != p {
			p = "~/" + rel
		}
	}
	return Elide(p, 54)
}

// Elide drops the middle of an over-long string, keeping both ends.
//
// Both ends, because the head says where something lives and the tail says what
// it is: truncating either one alone produces a line the reader cannot act on.
func Elide(s string, max int) string {
	if max < 8 || lipgloss.Width(s) <= max {
		return s
	}
	rs := []rune(s)
	keep := max - 1 // for the ellipsis
	head := keep / 2
	tail := keep - head
	return string(rs[:head]) + "…" + string(rs[len(rs)-tail:])
}

// Tally renders the one-line summary a person reads before anything else: how
// much of the fleet is working, and how much needs them.
//
// The browser board has carried this since it existed; the terminal had
// nothing, so an operator over ssh had to count rows. Zero counts are dropped
// rather than shown as "0", because a row of zeroes is noise that hides the one
// number that is not zero.
func Tally(pairs []Count) string {
	var out []string
	for _, c := range pairs {
		if c.N == 0 && !c.Always {
			continue
		}
		style := Dim
		switch {
		case c.Tone == "good" && c.N > 0:
			style = Good
		case c.Tone == "attn" && c.N > 0:
			style = Attn
		case c.Tone == "alarm" && c.N > 0:
			style = Alarm
		}
		out = append(out, style(itoa(c.N))+" "+Dim(c.Label))
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, Dim("  ·  "))
}

// Count is one figure in a Tally.
type Count struct {
	Label  string
	N      int
	Tone   string // "" | good | attn | alarm
	Always bool   // show even at zero (for "0 of 4 live", which is the alarming case)
}

func itoa(n int) string { return strconv.Itoa(n) }
