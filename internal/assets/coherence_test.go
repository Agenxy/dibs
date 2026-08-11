package assets

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// read pulls a surface off disk. The surfaces are not embedded here: they
// belong to internal/mcp and internal/web, and this test is about whether they
// honour THIS package's design system, so it reaches for them by path rather
// than inverting the dependency to suit a test.
func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// The three surfaces must stay ONE product, and that cannot be maintained by
// looking at them.
//
// Drift here is never a decision: nobody sets out to make the panel a
// different shade of green. It happens one hurried rule at a time: a hex typed
// inline because the token name was not to hand, a font-size in px because the
// scale was two files away. Each is invisible alone and the sum is two products
// wearing one name. This is the check that a person would have to do by eye,
// done mechanically instead.
//
// It guards the SHARED sheet's authority, not the surfaces' freedom: a surface
// may lay itself out however it likes, and may state what is genuinely
// different about it (the panel's control density, its busy spinner). What it
// may not do is re-decide colour or type.
func TestSurfacesDoNotDriftFromTheDesignSystem(t *testing.T) {
	style := regexp.MustCompile(`(?s)<style[^>]*>(.*?)</style>`)
	fontPx := regexp.MustCompile(`font-size:\s*[0-9.]+px`)
	// Not just hex. The palette moved to OKLCH, so a surface can now drift by
	// writing a colour function inline just as easily as by typing a hex: and
	// the check that only knew about `#` would have waved it through.
	rawColour := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|\b(?:oklch|oklab|lch|lab|rgba?|hsla?|color)\(`)
	// A surface redefining a palette token is the drift that matters most: it
	// looks right on that surface and silently disagrees with the other two.
	redefine := regexp.MustCompile(`--(?:bg|fg|accent|live|warn|line)[\w-]*\s*:`)

	for _, s := range []struct{ name, src string }{
		{"the MCP App panel", read(t, "../mcp/board_app.html")},
		{"the web board", read(t, "../web/templates/index.html")},
	} {
		for _, block := range style.FindAllStringSubmatch(s.src, -1) {
			css := block[1]
			if m := fontPx.FindAllString(css, -1); len(m) > 0 {
				t.Errorf("%s sets type in px, bypassing the scale: %v\n"+
					"  use --t-micro/meta/fine/sm/body/lg/title: a px size also ignores\n"+
					"  the reader who raised their browser font because they need it",
					s.name, m)
			}
			if m := rawColour.FindAllString(css, -1); len(m) > 0 {
				t.Errorf("%s hardcodes colour: %v\n"+
					"  every hue lives in board.css; a literal here is a colour the other\n"+
					"  two surfaces will never learn about", s.name, m)
			}
			if m := redefine.FindAllString(css, -1); len(m) > 0 {
				t.Errorf("%s redefines a palette token: %v\n"+
					"  that is how the same green becomes two greens", s.name, m)
			}
		}
	}
}

// Every colour the system renders must clear WCAG AA for BODY text.
//
// The palette carried --fg-3 at 3.66:1 (dark) and 3.25:1 (light). Both clear
// 3.0, which is the LARGE-text allowance, 18pt or 14pt bold, while fg-3 is
// what the system puts on 9px and 10px type: pills, badges, ages, counts,
// bands. The lowest contrast in the system on the smallest type in it.
//
// Computed rather than eyeballed, because "looks fine to me" is exactly the
// judgement this replaces.
func TestEveryPaletteColourClearsAABody(t *testing.T) {
	const aaBody = 4.5
	pal := palette(t)
	for _, theme := range []string{"dark", "light"} {
		for _, tok := range []string{"fg", "fg-2", "fg-3", "accent", "live", "warn"} {
			if r := contrast(pal[theme][tok], pal[theme]["bg"]); r < aaBody {
				t.Errorf("%s --%s is %.2f:1 on the page background, below AA body %.1f\n"+
					"  3.0 is the LARGE-text allowance and this colour is used on 9-10px type",
					theme, tok, r, aaBody)
			}
		}
	}
}

// rgb is a colour after conversion, in the 0..1 sRGB the WCAG formula expects.
type rgb struct{ r, g, b float64 }

// palette reads the shipped values out of board.css.
//
// The previous version of this test carried its own copy of the eighteen
// hexes and checked `strings.Contains(boardCSS, hex)` to catch drift between
// them. That is a weaker thing than it looks: it proves the sheet contains the
// string, not that the string is what the sheet USES, and it left the test as
// a second place where the palette was written down. Parsing removes the copy,
// so the audit cannot disagree with the thing it audits.
func palette(t *testing.T) map[string]map[string]rgb {
	t.Helper()
	decl := regexp.MustCompile(`--([a-z0-9-]+):\s*light-dark\(\s*(oklch\((?:[^()]|\([^()]*\))*\))\s*,\s*(oklch\((?:[^()]|\([^()]*\))*\))\s*\)`)
	hues := map[string]float64{}
	for _, m := range regexp.MustCompile(`--(h-[a-z]+):\s*([0-9.]+)`).FindAllStringSubmatch(boardCSS, -1) {
		h, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			t.Fatalf("hue --%s is not a number: %q", m[1], m[2])
		}
		hues[m[1]] = h
	}
	out := map[string]map[string]rgb{"light": {}, "dark": {}}
	for _, m := range decl.FindAllStringSubmatch(boardCSS, -1) {
		out["light"][m[1]] = oklch(t, m[2], hues)
		out["dark"][m[1]] = oklch(t, m[3], hues)
	}
	for _, theme := range []string{"dark", "light"} {
		for _, need := range []string{"bg", "bg-1", "bg-2", "fg", "fg-2", "fg-3", "accent", "live", "warn"} {
			if _, ok := out[theme][need]; !ok {
				t.Fatalf("board.css declares no --%s for the %s theme: either the palette "+
					"lost a token or it stopped using light-dark(), and this audit went blind "+
					"rather than failing", need, theme)
			}
		}
	}
	return out
}

// oklch converts one `oklch(L% C H)` to sRGB, resolving a var() hue.
//
// This is Ottosson's transform, and it is here rather than imported because it
// is the one piece of arithmetic that decides whether the assertions below mean
// anything. Out-of-gamut components are clipped exactly as a browser clips
// them, so what is measured is what is painted.
func oklch(t *testing.T, fn string, hues map[string]float64) rgb {
	t.Helper()
	f := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(fn, "oklch("), ")"))
	if len(f) != 3 {
		t.Fatalf("cannot read %q as oklch(L C H)", fn)
	}
	num := func(s string) float64 {
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err != nil {
			t.Fatalf("%q in %q is not a number", s, fn)
		}
		return v
	}
	l := num(f[0]) / 100
	c := num(f[1])
	var h float64
	if strings.HasPrefix(f[2], "var(") {
		name := strings.TrimSuffix(strings.TrimPrefix(f[2], "var(--"), ")")
		v, ok := hues[name]
		if !ok {
			t.Fatalf("%q refers to --%s, which board.css does not declare", fn, name)
		}
		h = v
	} else {
		h = num(f[2])
	}
	a, b := c*math.Cos(h*math.Pi/180), c*math.Sin(h*math.Pi/180)
	cube := func(v float64) float64 { return v * v * v }
	lc := cube(l + 0.3963377774*a + 0.2158037573*b)
	mc := cube(l - 0.1055613458*a - 0.0638541728*b)
	sc := cube(l - 0.0894841775*a - 1.2914855480*b)
	enc := func(v float64) float64 {
		v = math.Max(0, math.Min(1, v))
		if v <= 0.0031308 {
			return v * 12.92
		}
		return 1.055*math.Pow(v, 1/2.4) - 0.055
	}
	return rgb{
		enc(4.0767416621*lc - 3.3077115913*mc + 0.2309699292*sc),
		enc(-1.2684380046*lc + 2.6097574011*mc - 0.3413193965*sc),
		enc(-0.0041960863*lc - 0.7034186147*mc + 1.7076147010*sc),
	}
}

// contrast is the WCAG 2.1 relative-luminance ratio.
func contrast(a, b rgb) float64 {
	lum := func(c rgb) float64 {
		ch := func(v float64) float64 {
			if v <= 0.03928 {
				return v / 12.92
			}
			return math.Pow((v+0.055)/1.055, 2.4)
		}
		return 0.2126*ch(c.r) + 0.7152*ch(c.g) + 0.0722*ch(c.b)
	}
	x, y := lum(a), lum(b)
	if x < y {
		x, y = y, x
	}
	return (x + 0.05) / (y + 0.05)
}

// An emphasis surface has to be VISIBLE against the page it sits on.
//
// --bg-2 is the selected tab, the hovered control, the raised card. Dark moved
// it 4.9 lightness away from the page; light moved it 1.1, because the page
// was already at 97.6 and there is nothing above it, so in the light theme
// the board simply did not say which tab you were on. Both themes passed every
// contrast check in this file, because contrast checks measure TEXT against
// its background and this is a background against a background.
//
// Found by opening the board in light and noticing the absence of something.
func TestTheEmphasisSurfaceIsVisibleAgainstThePage(t *testing.T) {
	// Measured, not guessed: the dark theme reads correctly at 1.17, so the
	// floor is set just below it and the light theme was at 1.02.
	const leastPerceptible = 1.06
	pal := palette(t)
	for _, theme := range []string{"dark", "light"} {
		if r := contrast(pal[theme]["bg-2"], pal[theme]["bg"]); r < leastPerceptible {
			t.Errorf("%s --bg-2 is %.3f:1 against --bg: the selected tab, the hovered\n"+
				"  control and the raised card are all invisible at that step. Light marks\n"+
				"  emphasis by going DOWN in lightness; there is no room above the page.",
				theme, r)
		}
	}
}

// Colours are used in PAIRS, and the pairing is what a reader sees.
//
// The audit above measured every colour against the page background, which is
// where most text sits and is not where the important marks sit. The alarm pill
// is text on --warn; the live badge is text on --live-dim. A palette can pass
// colour-by-colour and still put 2.98:1 on the one mark that means somebody has
// to do something, which is exactly what happened here, and what the
// colour-by-colour check did not see. Found by codex.
//
// The pairs are named by TOKEN now, not by hex. Spelling them as hexes meant
// this test kept its own copy of the palette and would go on passing against
// colours the product had stopped using.
func TestFilledMarksClearAABodyAgainstTheirOwnBackground(t *testing.T) {
	const aaBody = 4.5
	pal := palette(t)
	for _, c := range []struct{ what, fg, bg string }{
		// The abandoned pill: "nothing will resolve this without a person". The
		// loudest thing on the board, and so the most legible.
		{"abandoned pill", "bg", "warn"},
		// Selected tab: text on the raised surface it sits in.
		{"selected tab", "fg", "bg-2"},
		// Inputs, where somebody types.
		{"input text", "fg", "bg-1"},
	} {
		for _, theme := range []string{"dark", "light"} {
			if r := contrast(pal[theme][c.fg], pal[theme][c.bg]); r < aaBody {
				t.Errorf("%s (%s): --%s on --%s is %.2f:1, below AA body %.1f\n"+
					"  a palette can pass colour-by-colour and still fail where the colours meet",
					c.what, theme, c.fg, c.bg, r, aaBody)
			}
		}
	}
}

// A literal colour in the SHARED sheet is drift too.
//
// The surface check caught hexes in the two HTML surfaces and never looked at
// board.css itself: where a mangled edit of mine left `.pill.abandoned {
// color: #fff }` in global scope, overriding the dark theme and putting white
// on amber at 2.98:1 on the one mark meaning "nothing resolves this without a
// person". A drift test with a blind spot over the design system is not a
// drift test.
func TestOnlyThePaletteDeclaresLiteralColour(t *testing.T) {
	// Hexes are legitimate where the palette is DEFINED: inside :root blocks
	// and the prefers-color-scheme override. Everywhere else is a decision that
	// escaped the system.
	body := boardCSS
	for _, marker := range []string{":root {", `:root[data-theme="light"] {`, "@media (prefers-color-scheme: light) {"} {
		i := strings.Index(body, marker)
		if i < 0 {
			continue
		}
		// Blank out the palette block so only rules are scanned.
		end := strings.Index(body[i:], "\n}")
		if end > 0 {
			body = body[:i] + strings.Repeat(" ", end+2) + body[i+end+2:]
		}
	}
	// Comments are prose, and the prose here explains the very bug this catches,
	// stripping them is not leniency, it is scanning the stylesheet rather than
	// its documentation.
	body = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(body, "")
	if m := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`).FindAllString(body, -1); len(m) > 0 {
		t.Errorf("board.css declares literal colour outside the palette: %v\n"+
			"  every hue belongs to a token; a literal here silently outranks a theme", m)
	}
}

// And the shared sheet itself must hold the line it asks the surfaces to hold.
func TestTheDesignSystemUsesItsOwnScale(t *testing.T) {
	if m := regexp.MustCompile(`font-size:\s*[0-9.]+px`).FindAllString(boardCSS, -1); len(m) > 0 {
		t.Errorf("board.css sets type in px: %v: the scale exists to end exactly this", m)
	}
	for _, tok := range []string{"--t-micro", "--t-meta", "--t-fine", "--t-sm", "--t-body", "--t-lg", "--t-title"} {
		if !strings.Contains(boardCSS, tok+":") {
			t.Errorf("the type scale is missing %s; surfaces are told to use it", tok)
		}
	}
	// One focus ring, reachable by every interactive element. The old code
	// styled focus four times in three ways and removed it once.
	if !strings.Contains(boardCSS, ":focus-visible") ||
		!strings.Contains(boardCSS, "forced-colors") {
		t.Error("the shared focus ring must exist and must survive forced-colours mode")
	}
	// Both themes reachable without an explicit attribute. The mechanism is now
	// `color-scheme: light dark` plus light-dark() rather than a media query
	// carrying a second copy of the palette, so this asserts the guarantee and
	// names both halves, since either alone silently gives you one theme.
	if !strings.Contains(boardCSS, "color-scheme: light dark") ||
		!strings.Contains(boardCSS, "light-dark(") {
		t.Error("the board must follow the system theme; it was dark-only for a long time")
	}
}

// Motion drifts exactly the way colour does.
//
// The panel had view transitions, a reduced-motion guard and a feature check.
// The web board, same components, same design system, same product, replaced
// its HTML with no continuity at all. Nobody decided that; one surface simply
// got the work. Two surfaces wearing one name, which is the whole thing this
// file exists to prevent, and it is as true of how the product MOVES as of
// what colour it is.
//
// So the runner is shared and a surface may not start its own. A second copy
// is how the reduced-motion guard ends up on one surface and not the other.
func TestNeitherSurfaceRunsItsOwnMotion(t *testing.T) {
	for _, s := range []struct{ name, src string }{
		{"the MCP App panel", read(t, "../mcp/board_app.html")},
		{"the web board", read(t, "../web/templates/index.html")},
	} {
		if strings.Contains(s.src, "document.startViewTransition") {
			t.Errorf("%s starts its own view transition\n"+
				"  use Board.transition(kind, update): a second copy is how the\n"+
				"  reduced-motion guard ends up on one surface and not the other", s.name)
		}
	}
	// And the shared runner has to actually carry the guard it is trusted with.
	for _, need := range []string{"prefers-reduced-motion", "startViewTransition", "data.transition"} {
		if !strings.Contains(boardJS, strings.ReplaceAll(need, "data.transition", "dataset.transition")) {
			t.Errorf("the shared runner is missing %q: every surface now depends on it", need)
		}
	}
}

// An explanation nobody can reach is not an explanation.
//
// Every mark that means something the reader cannot derive carries a `why`,
// and for a long time that `why` was a `title` attribute: mouse only, hover
// only, after a delay, absent on touch, and announced inconsistently. The text
// was written with care and most people could never get at it: the same shape
// as a documented feature nothing ever calls.
//
// It now goes through Board.explained/data-why into one anchored popover. This
// exists because ONE site hand-rolled its own `title` rather than using the
// helper, and so stayed behind on the mark that SPEC-CHANNELS §10.3 requires
// be explainable. A helper that can be bypassed silently will be.
func TestNoMarkExplainsItselfOnlyToAMouse(t *testing.T) {
	for _, s := range []struct{ name, src string }{
		{"the shared component library", boardJS},
		{"the MCP App panel", read(t, "../mcp/board_app.html")},
		{"the web board", read(t, "../web/templates/index.html")},
	} {
		if m := regexp.MustCompile("title=[\"`']").FindAllString(s.src, -1); len(m) > 0 {
			t.Errorf("%s delivers %d explanation(s) by title attribute\n"+
				"  title reaches only a reader with a mouse who waits: no touch, no\n"+
				"  keyboard, no reliable announcement. Use explained(cls, label, why),\n"+
				"  or data-why + tabindex for a mark built by hand", s.name, len(m))
		}
	}
	// And the mechanism it moved TO must actually be installed, on both
	// surfaces. data-why with nothing reading it is a silent regression to no
	// explanation at all, which is worse than the title it replaced.
	for _, s := range []struct{ name, src string }{
		{"the MCP App panel", read(t, "../mcp/board_app.html")},
		{"the web board", read(t, "../web/templates/index.html")},
	} {
		if !strings.Contains(s.src, "Board.explainer()") {
			t.Errorf("%s renders data-why marks but never installs Board.explainer()", s.name)
		}
	}
}

// The first screen has to say what to do next.
//
// A person who has just started the daemon does not need to be told the board is
// empty: they can see that. They need the next command. This was "No lanes", a
// sentence, and a row of four zeros: all true, none of it a way forward, on the
// one screen where somebody decides whether the thing is real.
//
// Checked here rather than in the browser because it is a CONTENT promise, and
// the browser suite already proves the page renders.
func TestTheFirstScreenGivesTheNextCommand(t *testing.T) {
	js := read(t, "board.js")
	if !strings.Contains(js, "firstRunHTML") {
		t.Fatal("no first-run state; an empty board is where a new user is lost")
	}
	start := strings.Index(js, "function firstRunHTML")
	end := strings.Index(js[start:], "\n  }") + start
	body := js[start:end]

	// The command itself, not a description of it.
	if !strings.Contains(body, "lanes mcp-config") {
		t.Error("the first screen must name the command that connects an agent")
	}
	// And the honest caveat: matching is off until it is configured, which is
	// the single thing most likely to make somebody think the product is broken.
	if !strings.Contains(body, "lanes calibrate") {
		t.Error("the first screen must say matching needs calibrating. " +
			"a board that silently never matches reads as a broken product")
	}
	// Nothing here is a problem, so nothing here is coloured.
	if strings.Contains(body, "warn") || strings.Contains(body, "alarm") {
		t.Error("an empty board is not an error state and must not be dressed as one")
	}

	// The web board must actually distinguish "never registered" from "all gone".
	web := read(t, "../web/templates/index.html")
	if !strings.Contains(web, "firstRunHTML") {
		t.Error("the board never shows the first-run state")
	}
	if !strings.Contains(web, "No agents right now") {
		t.Error("a board that has emptied out is a different fact from one that " +
			"never filled, and only one of them is answered by a command")
	}
}

// Text colour may not be thinned with transparency.
//
// The palette's dimmest foreground, --fg-3, measures about 4.79:1 on the page
// background: deliberately just above the 4.5:1 AA floor, because it is the
// quietest thing the system is willing to say. Mixing ANY of it toward
// transparent therefore lands below AA by construction, and does so invisibly:
// the declaration still names a palette token, so every existing guard here
// passes while the rendered text drops under the line.
//
// This is not hypothetical. A rule added to make a zero reading recede,
// `color: color-mix(in oklch, var(--fg-3) 72%, transparent)`: measured 3.03:1
// in dark and 2.83:1 in light and shipped through a full green gate, because the
// palette check only ever sees the TOKEN and never the mix. It was dimmest on
// exactly the readings a low-vision reader has to work hardest to find.
//
// De-emphasis is still allowed; it just has to come from somewhere other than
// the contrast budget. Step down to a quieter token, drop a glow, reduce weight,
// or lower the SIZE: all of which recede without making the glyphs harder to
// separate from the background.
//
// Backgrounds, borders and shadows are untouched by this: they are not text, and
// mixing them toward transparent is how the whole system builds depth.
func TestNoTextColourIsThinnedWithTransparency(t *testing.T) {
	style := regexp.MustCompile(`(?s)<style[^>]*>(.*?)</style>`)
	// `color:` only, not background-color, border-color, text-decoration-color,
	// or any of the box properties. The property boundary is what keeps this
	// from banning the technique everywhere it is correct.
	// Any color-mix in a TEXT colour, not merely one naming `transparent`.
	//
	// Matching the literal word was evadable in one line: assigning transparent
	// to a custom property and mixing with that produced the identical below-AA
	// glyph and passed. Since no text colour in this system legitimately uses
	// color-mix at all (verified: zero occurrences across all four surfaces),
	// the honest rule is the total one. It cannot be reformulated around,
	// because it does not depend on how the second operand is spelled.
	thinned := regexp.MustCompile(`(?:^|[;{\s])color\s*:\s*color-mix\(`)

	sources := map[string]string{
		"board.css":         read(t, "board.css"),
		"signal.css":        read(t, "signal.css"),
		"the MCP App panel": read(t, "../mcp/board_app.html"),
		"the web board":     read(t, "../web/templates/index.html"),
	}
	for name, src := range sources {
		css := src
		if blocks := style.FindAllStringSubmatch(src, -1); len(blocks) > 0 {
			css = ""
			for _, b := range blocks {
				css += b[1]
			}
		}
		if m := thinned.FindAllString(css, -1); len(m) > 0 {
			t.Errorf("%s builds a TEXT colour with color-mix: %v\n"+
				"  --fg-3 already sits at ~4.79:1, just above the 4.5:1 AA floor, so any\n"+
				"  mix that dilutes it renders below AA while still naming a palette\n"+
				"  token, which is why every other check here passes. The rule covers\n"+
				"  ALL color-mix on text, not just the ones spelling out `transparent`,\n"+
				"  because hiding that word behind a custom property evaded the earlier\n"+
				"  version in a single line.\n"+
				"  To recede, use a quieter token, less weight, a smaller size, or drop a\n"+
				"  glow. Not the contrast budget.", name, m)
		}
	}
}
