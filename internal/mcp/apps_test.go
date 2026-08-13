package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The board detail must reach the UI and NOT the model. Base MCP's content and
// structuredContent are model-facing; tool-result _meta is the MCP App's private
// backchannel, so the whole point of this tool is that `content` stays one cheap
// line and the board travels only in metadata.
func TestShowBoardSplitsModelAndUIPayloads(t *testing.T) {
	res := core.Result{
		"board": core.Result{
			"node": "n1", "serial": 42,
			"agents": []core.Result{
				{"id": "a", "status": "active"},
				{"id": "b", "status": "dormant"},
			},
		},
		"inbox":    core.Result{"messages": []core.Result{{"serial": 1}, {"serial": 2}}},
		"agent_id": "a",
	}
	out := showBoardResult(res, false, false)

	content := out["content"].([]map[string]any)
	text := content[0]["text"].(string)
	for _, want := range []string{"2 agent(s)", "1 active", "2 unread"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary %q missing %q", text, want)
		}
	}
	// A summary that silently reports zero is the failure mode we actually hit:
	// asserting []any against a typed slice yields nothing without erroring.
	if strings.Contains(text, "0 agent(s)") {
		t.Fatal("summary counted 0 agents while the payload had 2")
	}

	// structuredContent may carry the panel's BOOTSTRAP: the view, the agent id
	// and the caller's own token, so a host that drops _meta can still let the
	// panel fetch the board over its own bridge. It must never carry the board
	// itself: that is the cost this tool exists to avoid.
	if boot, ok := out["structuredContent"].(core.Result); ok {
		for _, banned := range []string{"board", "inbox", "events"} {
			if _, leaked := boot[banned]; leaked {
				t.Errorf("board detail leaked into model-facing structuredContent via %q", banned)
			}
		}
		// A host that shows structuredContent INSTEAD of content must still leave
		// the agent knowing what the board looks like.
		if boot["summary"] != text {
			t.Errorf("bootstrap summary = %v, want the same line content carries (%q)",
				boot["summary"], text)
		}
	}
	meta := out["_meta"].(map[string]any)
	sc, ok := meta[panelDataMetaKey].(core.Result)
	if !ok {
		t.Fatal("panel metadata missing: the UI would render nothing")
	}
	b := asMap(sc["board"])
	if got := len(asMaps(b["agents"])); got != 2 {
		t.Errorf("panel metadata agents = %d, want 2", got)
	}

	ui := meta["ui"].(map[string]any)
	if ui["resourceUri"] != uiBoardURI {
		t.Errorf("resourceUri = %v, want %v", ui["resourceUri"], uiBoardURI)
	}

	// The model line must stay far smaller than the UI payload, or the tool has
	// stopped paying for itself.
	full, _ := json.Marshal(sc)
	if len(text) >= len(full) {
		t.Errorf("model text (%d B) not smaller than UI payload (%d B)", len(text), len(full))
	}
}

// Hosts prefetch and cache the template by URI, so it must be static and carry
// the exact MIME type the spec reserves.
func TestUIBoardTemplateIsStaticAndWellFormed(t *testing.T) {
	requireFullPanel(t)
	a := readUIBoard()["contents"].([]map[string]any)[0]
	if a["mimeType"] != uiMime {
		t.Errorf("mimeType = %v, want %v", a["mimeType"], uiMime)
	}
	if a["uri"] != uiBoardURI {
		t.Errorf("uri = %v, want %v", a["uri"], uiBoardURI)
	}
	html, _ := a["text"].(string)
	if !strings.Contains(html, "ui/initialize") || !strings.Contains(html, "ui/notifications/tool-result") {
		t.Error("template does not implement the MCP Apps postMessage handshake")
	}
	// The panel must state the full route: "from X" alone reads as half a fact
	// when the reader is trying to follow agent-to-agent traffic.
	if !strings.Contains(html, `class="route"`) {
		t.Error("message header does not render a from → to route")
	}
	// A human reads this panel; the agent is one of the names on screen. Second
	// person asks the reader to be the agent, which is how "→ you" shipped once.
	// Match rendered output only: prose in a comment explaining the rule is fine.
	for _, bad := range []string{`>you<`, `> you<`, "→ you", "You</"} {
		if strings.Contains(html, bad) {
			t.Errorf("panel addresses the reader as the agent (%q); name the agent instead", bad)
		}
	}
	// First paint must not wait on the host. An earlier version called draw()
	// only after ui/initialize settled, so a silent host left the panel blank for
	// the full timeout, and a host that sizes its container to content showed the
	// human an empty box. The draw() call must precede the handshake IIFE.
	firstDraw := strings.Index(html, "\ndraw()")
	handshake := strings.Index(html, `call("ui/initialize"`)
	if firstDraw < 0 || handshake < 0 || firstDraw > handshake {
		t.Error("panel does not paint before the ui/initialize handshake")
	}
	// The handshake shape is validated by the host against a zod schema. Sending
	// base MCP's clientInfo/capabilities instead of appInfo/appCapabilities is
	// rejected, and rejection is silent: the panel simply never comes up.
	if !strings.Contains(html, "appInfo:") || !strings.Contains(html, "appCapabilities:") {
		t.Error("ui/initialize must send appInfo + appCapabilities, not clientInfo/capabilities")
	}
	if strings.Contains(html, `clientInfo: { name: "agents-board"`) {
		t.Error("ui/initialize still sends the base-MCP clientInfo shape")
	}
	// The host sizes the iframe from what the app reports. Send nothing and the
	// container has zero height: a widget that "rendered" showing nothing. This
	// is what made the panel look blank for three rounds of testing.
	if !strings.Contains(html, "ui/notifications/size-changed") {
		t.Error("app never reports its size; the host cannot give it any height")
	}
	if !strings.Contains(html, "ui/notifications/initialized") {
		t.Error("app never confirms initialization; the host may not treat it as live")
	}
	// The panel is set as a document, not a dashboard: an agent is a ledger
	// ENTRY, its status a printed mark. Asserting the structure rather than a
	// particular decoration: the previous version of this pinned `class="rail"`
	// and so failed the moment the design was reworked, which is a test
	// measuring the wrong thing.
	for _, part := range []string{`class="entry`, `class="pip"`, `class="band`} {
		if !strings.Contains(html, part) {
			t.Errorf("agent entry structure missing %s", part)
		}
	}
	// Figures are read in columns, so they must be tabular or the columns lie.
	if !strings.Contains(html, "tabular-nums") {
		t.Error("figures must be set with tabular numerals")
	}
	// prompt() is refused outright by a sandboxed iframe with no allow-modals,
	// which would make Answer silently do nothing. Matching the CALL form, not
	// the bare word: the template explains in prose why it does not use one, and
	// a naive substring check fails on its own documentation.
	if strings.Contains(html, `prompt("`) || strings.Contains(html, "prompt('") {
		t.Error("a sandboxed panel cannot use prompt(); reply must be inline")
	}
	if !strings.Contains(html, "<textarea") {
		t.Error("no inline composer; answering a question would have nowhere to type")
	}
	// Self-hosted type: an external font URL would be blocked by our own CSP.
	if !strings.Contains(html, "@font-face") || strings.Contains(html, "https://fonts.") {
		t.Error("fonts must be inlined, never fetched from an external origin")
	}
	// Live board data must arrive via notifications, never be baked into a
	// cached template.
	if strings.Contains(html, "9c879068") || strings.Contains(html, "\"agents\":[") {
		t.Error("template appears to embed board data; it must stay static")
	}
	csp := a["_meta"].(map[string]any)["ui"].(map[string]any)["csp"].(map[string]any)
	if len(csp["connectDomains"].([]string)) != 0 {
		t.Error("template should declare no external connect domains")
	}
}

func TestInboxCountHandlesBothShapes(t *testing.T) {
	if got := inboxCount(map[string]any{"messages": []any{1, 2, 3}}); got != 3 {
		t.Errorf("wrapped = %d, want 3", got)
	}
	if got := inboxCount([]any{1, 2}); got != 2 {
		t.Errorf("bare = %d, want 2", got)
	}
	if got := inboxCount(nil); got != 0 {
		t.Errorf("nil = %d, want 0", got)
	}
}

// A panel fix has to be able to REACH a host, and for one release it could not.
//
// Hosts prefetch the template by URI and keep it: this server marks it public
// and hints an hour, and a real host held one across a daemon restart and every
// rebuild in a session, serving pre-fix markup while the daemon served the fix
// to anyone who asked. Nothing asked. A permanently stable URI makes a shipped
// panel bug permanent for everyone who already has it.
//
// So identity follows the bytes: same template, same URI, still cached; changed
// template, a URI nothing has ever held. Asserting the RELATIONSHIP rather than
// any particular hash, since the digest is an implementation detail and pinning
// it would make every panel edit a test edit.
func TestThePanelURIChangesWhenThePanelDoes(t *testing.T) {
	requireFullPanel(t)
	uri := uiBoardURI
	if !strings.HasPrefix(uri, uiBoardBase+"/") {
		t.Fatalf("panel URI %q left its family name behind; hosts and the resource "+
			"dispatch both key off %q", uri, uiBoardBase)
	}
	if uri == uiBoardBase {
		t.Fatal("the panel URI is still the bare constant: a cached panel can never be replaced")
	}
	// Derived from the served template, so an edit anywhere in it moves the URI.
	// Recomputed here the long way rather than compared to the package value,
	// which would pass even if the package computed it from something else.
	// Hashed BEFORE the build id is substituted in, or the template would have
	// to contain its own digest.
	sum := sha256.Sum256([]byte(boardAppTemplate()))
	if want := uiBoardBase + "/" + hex.EncodeToString(sum[:])[:12]; uri != want {
		t.Errorf("panel URI = %q, want %q: it is not derived from the template served", uri, want)
	}
	// Two different templates must not share a URI, which is the property the
	// whole mechanism rests on.
	other := sha256.Sum256([]byte(boardAppTemplate() + "<!-- one more byte -->"))
	if hex.EncodeToString(other[:])[:12] == hex.EncodeToString(sum[:])[:12] {
		t.Error("a changed template produced the same URI; hosts would keep the old panel")
	}
	// And the descriptor and the tool result must both point at the SAME URI, or
	// a host reads one and renders the other.
	if got := uiResourceDescriptor()["uri"]; got != uri {
		t.Errorf("resources/list advertises %v, tool results carry %q", got, uri)
	}
	meta := panelMeta(core.Result{})["ui"].(map[string]any)
	if got := meta["resourceUri"]; got != uri {
		t.Errorf("tool-result _meta points at %v, not %q", got, uri)
	}
	if got := readUIBoard()["contents"].([]map[string]any)[0]["uri"]; got != uri {
		t.Errorf("resources/read answers under %v, not %q", got, uri)
	}

	// And the panel says which build it is, in its own markup. This is the only
	// thing that makes a screenshot diagnostic: a host serving a cached panel
	// looks identical to a server that never shipped the fix, and for one round
	// of this the difference was guessed at rather than read.
	html, _ := readUIBoard()["contents"].([]map[string]any)[0]["text"].(string)
	if !strings.Contains(html, "panel "+panelBuild) {
		t.Error("the panel does not print its build id; a screenshot cannot say which " +
			"template is on screen")
	}
	if strings.Contains(html, "__PANELBUILD__") {
		t.Error("the build placeholder was served unsubstituted")
	}
}

// board on a host that declares a renderer must actually carry a board.
//
// This is the failure a screenshot finally showed, after the tool result had
// been measured every other way and looked correct every time. The panel said it
// itself: "no board from this host". Every carrier was empty. _meta dropped by
// the host, `content` one summary line with no board in it, structuredContent
// holding three fields of plumbing, and the fetch impossible because that host
// does not let an app call tools back. board's whole purpose is to put the
// board in front of a human, and it was displaying nothing at all.
//
// So a client that DECLARES it renders MCP Apps gets the board in
// structuredContent as well, and pays for it. A client that declares nothing
// still gets _meta and nothing extra: that half is asserted too, because gating
// the payload itself (rather than this duplicate) is the older mistake that
// silently starved the reference host.
func TestShowBoardCarriesTheBoardToAHostThatSaysItRenders(t *testing.T) {
	res := core.Result{
		"board": core.Result{
			"node": "n1", "serial": 7,
			"agents": []core.Result{{"id": "a", "status": "active"}},
		},
		"agent_id": "a", "act_token": "tok",
	}

	// Declared: the board must be reachable without _meta and without a fetch.
	declared, ok := showBoardResult(res, false, true)["structuredContent"].(core.Result)
	if !ok {
		t.Fatal("no structuredContent for a host that declared a renderer")
	}
	board := asMap(declared["board"])
	if got := len(asMaps(board["agents"])); got != 1 {
		t.Errorf("structuredContent board has %d agents, want 1: the panel would draw "+
			"\"no board from this host\"", got)
	}
	// The summary must survive alongside it: this host shows the model
	// structuredContent INSTEAD of content, so dropping it blinds the agent.
	if declared["summary"] == nil {
		t.Error("structuredContent lost the summary the agent reads")
	}
	if declared["act_token"] != "tok" {
		t.Error("structuredContent lost the token the panel acts with")
	}

	// Undeclared: bootstrap only, and _meta unchanged. The reference host lives
	// here and renders from _meta alone.
	plain := showBoardResult(res, false, false)
	boot, ok := plain["structuredContent"].(core.Result)
	if !ok {
		t.Fatal("no bootstrap for an undeclared host")
	}
	if _, leaked := boot["board"]; leaked {
		t.Error("board duplicated into structuredContent for a host that never asked; " +
			"that is the context cost board exists to avoid")
	}
	meta := plain["_meta"].(map[string]any)
	if _, has := meta[panelDataMetaKey]; !has {
		t.Fatal("the _meta payload is gated on the declaration: that starves every " +
			"host that renders without announcing")
	}
}

// No tool may advertise the unhashed panel URI.
//
// Hosts learn an MCP App's template from `_meta.ui.resourceUri` on the tool in
// tools/list, and they resolve it once, at connect time. A URI that does not
// change when the panel changes is therefore cached for the life of the
// connection: the server ships a fix, the host keeps drawing the old panel, and
// nothing in either log says so. That is precisely what happened: the result
// and resources/list carried the content hash while all four declarations
// carried the base, so the one path hosts actually use was the one path the
// versioning never reached.
//
// The inspector could not have caught this. It re-reads the template on every
// run, so it never has a cache to serve stale bytes from: a green instrument
// and a stale screen, at the same time, for the same build.
func TestNoToolDeclaresTheUnhashedPanelURI(t *testing.T) {
	for _, tool := range toolDefs {
		meta, ok := tool["_meta"].(map[string]any)
		if !ok {
			continue
		}
		ui, ok := meta["ui"].(map[string]any)
		if !ok {
			continue
		}
		uri, _ := ui["resourceUri"].(string)
		// An absent or empty resourceUri is not a pass.
		//
		// This test skipped it, so deleting the field from all four declarations
		// left the whole suite green while every host lost the panel entirely,
		// the failure is total rather than stale, and it was the less-guarded of
		// the two. A tool that declares `ui` at all is claiming to have one.
		if uri == "" {
			t.Errorf("tool %v declares _meta.ui with no resourceUri: a host has nothing "+
				"to resolve and renders no panel at all", tool["name"])
			continue
		}
		if uri == uiBoardBase {
			t.Errorf("tool %v declares the unhashed panel URI %q: hosts cache the "+
				"template under this URI at connect time and will never see a changed "+
				"panel; use uiBoardURI", tool["name"], uri)
		}
		if uri != "" && uri != uiBoardURI {
			t.Errorf("tool %v declares panel URI %q, want the current %q",
				tool["name"], uri, uiBoardURI)
		}
	}
}

// The declaration, the resource listing and the tool result must agree.
//
// Three surfaces name the same template, and a host may consult any of them. If
// they disagree the host can fetch a URI the server does not serve, or cache one
// the server has moved on from: both of which present as a blank or stale
// panel with no error anywhere.
func TestEverySurfaceNamesTheSamePanelURI(t *testing.T) {
	// The declarations are the surface hosts actually resolve from, so they are
	// counted here rather than only pattern-checked above: a test named "every
	// surface" that never looked at a tool declaration was describing more than
	// it did.
	// The exact set, not a count.
	//
	// Requiring "at least one" let three of the four declarations be deleted
	// while the suite stayed green: check_in, inbox and board would all
	// have stopped opening the panel, which is most of the ways a human ever
	// sees it, and only await_events kept the guard satisfied. These four are
	// named because each is a moment the human is meant to get a board: the
	// activation checkpoint, reading mail, asking for the board, and waking.
	mustDeclare := map[string]bool{
		"check_in": false, "inbox": false, "board": false, "await_events": false,
	}
	for _, tool := range toolDefs {
		name, _ := tool["name"].(string)
		if _, wanted := mustDeclare[name]; !wanted {
			continue
		}
		meta, _ := tool["_meta"].(map[string]any)
		ui, _ := meta["ui"].(map[string]any)
		if uri, _ := ui["resourceUri"].(string); uri == uiBoardURI {
			mustDeclare[name] = true
		}
	}
	for name, ok := range mustDeclare {
		if !ok {
			t.Errorf("tool %q no longer declares the panel URI: that surface stops "+
				"opening the board for the human, and a count-based check would not "+
				"have noticed while any other tool still declared it", name)
		}
	}
	listed, _ := uiResourceDescriptor()["uri"].(string)
	if listed != uiBoardURI {
		t.Errorf("resources/list advertises %q, tools declare %q", listed, uiBoardURI)
	}
	if !strings.HasPrefix(uiBoardURI, uiBoardBase+"/") {
		t.Errorf("uiBoardURI %q is not a versioned form of %q", uiBoardURI, uiBoardBase)
	}
}

// The URI must name the bytes actually served, under every switch that changes
// them.
//
// The hash covered the full template unconditionally while boardApp() could
// return the minimal panel instead, so a daemon with DIBS_PANEL_MINIMAL set
// advertised the same URI for 483 bytes that another advertised for 264594. A
// host that saw both across a restart would hold whichever it cached first and
// never refetch: a content-addressed identity that was not addressing the
// content.
func TestThePanelBuildNamesTheBytesActuallyServed(t *testing.T) {
	sum := sha256.Sum256([]byte(boardApp()))
	served := hex.EncodeToString(sum[:])[:12]
	// boardApp() substitutes the build id into the full template, so the served
	// bytes cannot equal the hashed ones there: what must hold is that the id
	// is derived from the same SOURCE the switch selects.
	source := boardAppTemplate()
	if panelMinimal {
		source = minimalPanelHTML
		if served != panelBuild {
			t.Errorf("minimal panel serves %s but the URI names %s: a host would cache "+
				"one under the other's identity", served, panelBuild)
		}
	}
	sum2 := sha256.Sum256([]byte(source))
	if want := hex.EncodeToString(sum2[:])[:12]; want != panelBuild {
		t.Errorf("panelBuild = %s, want %s: the id does not name the template this "+
			"daemon would serve", panelBuild, want)
	}
}

// requireFullPanel skips a test that is about the SHIPPING panel when the daemon
// is configured to serve the stripped diagnostic one.
//
// DIBS_PANEL_MINIMAL exists to answer "is the panel blank because of the panel,
// or because of the host" by serving 483 bytes that cannot possibly be at fault.
// Tests asserting the real template's structure, its script, or the way its hash
// tracks its content are meaningless against that stub: they were simply failing,
// so the whole package was red in a mode the daemon genuinely supports and nobody
// could run the suite to check anything else about it.
//
// Skipping is right here and reporting is not: the assertions do not apply, as
// opposed to applying and being unmet. TestTheMinimalPanelIsServableAndIdentified
// covers what SHOULD hold in that mode, so the skip does not leave it untested.
func requireFullPanel(t *testing.T) {
	t.Helper()
	if panelMinimal {
		t.Skip("DIBS_PANEL_MINIMAL=1: this asserts the shipping panel, which is not " +
			"what this daemon serves; see TestTheMinimalPanelIsServableAndIdentified")
	}
}

// The minimal panel is a real serving mode and has to hold its own contract.
//
// It is a diagnostic: something a person switches on when the board panel is
// blank, to find out whether the panel or the host is at fault. That only works
// if it is unmistakably itself: served, tiny, identified by its own hash, and
// not quietly the full template with the flag ignored.
func TestTheMinimalPanelIsServableAndIdentified(t *testing.T) {
	if !panelMinimal {
		t.Skip("DIBS_PANEL_MINIMAL is not set; task test:minimal runs this")
	}
	body := boardApp()
	if body != minimalPanelHTML {
		t.Fatal("the flag is set but boardApp served something else: the diagnostic " +
			"cannot rule the panel out if it is still serving the panel")
	}
	if len(body) > 4096 {
		t.Errorf("the minimal panel is %d bytes; it exists to be too small to be the "+
			"cause of anything", len(body))
	}
	sum := sha256.Sum256([]byte(minimalPanelHTML))
	if want := hex.EncodeToString(sum[:])[:12]; panelBuild != want {
		t.Errorf("panelBuild = %s, want %s: in this mode the URI must name the stub, "+
			"or a host caches one panel under the other's identity", panelBuild, want)
	}
	if !strings.HasPrefix(uiBoardURI, uiBoardBase+"/") {
		t.Errorf("uiBoardURI %q is not versioned", uiBoardURI)
	}
	// And it must still be a UI resource a host can actually mount.
	res := readUIBoard()
	contents, _ := res["contents"].([]map[string]any)
	if len(contents) == 0 || contents[0]["mimeType"] != uiMime {
		t.Errorf("the minimal panel is not served as an MCP App template: %v", res)
	}
}
