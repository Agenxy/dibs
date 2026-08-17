package mcp

// MCP Apps (SEP-1865, extension spec 2026-01-26): an interactive board the
// human can look at, rendered by the host in a sandboxed iframe.
//
// The split that makes this worth doing: `content` and `structuredContent` are
// model-facing base-MCP fields, while a tool result's `_meta` is the component's
// private backchannel. So the model pays for one compact line while the human
// gets the whole board. That is the efficiency pillar, applied to pixels.

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agenxy/dibs/internal/assets"
	"github.com/agenxy/dibs/internal/core"
)

// skillsDoc is the agent-facing playbook, served as dibs://skills.
//
// Embedded rather than read from disk because the binary has to answer this
// without the repository: an agent that installed a release has no SKILLS.md
// to open. A copy, because go:embed cannot reach above its own package; the
// root file is canonical and skills_embed_test.go fails if the two drift.
//
//go:embed skills.md
var skillsDoc string

// staffDoc is what a role-holder needs and an ordinary agent does not, served
// as dibs://staff.
//
// Separate from skills.md deliberately. Most agents never hold a role, and
// putting the remediation powers in the document everybody reads on first
// connection would spend the attention of the many on the concerns of the few.
// It is referenced from the grant notice instead, which is the moment an agent
// becomes able to use any of it.
//
//go:embed staff.md
var staffDoc string

//go:embed board_app.html
var boardAppHTML string

// boardApp returns the template with the shared design system and component
// library inlined. Deterministic: the same bytes every read, so it stays
// host-cacheable (hosts key their cache on the ui:// URI).
func boardApp() string {
	if panelMinimal {
		return minimalPanelHTML
	}
	// Two substitutions, both inlined rather than linked: the panel's CSP
	// declares no external origins, so a stylesheet or script fetched over the
	// network would fail closed and silently.
	return strings.Replace(boardAppTemplate(), "/*__PANELBUILD__*/", panelBuild, 1)
}

// boardAppTemplate is the panel with its styles and script inlined but its build
// id still a placeholder: the form panelBuild hashes.
//
// Hashing before substitution keeps the identity non-circular: the id names the
// template's content, and the template then carries the id. Hashing after would
// require the file to contain its own digest.
func boardAppTemplate() string {
	out := strings.Replace(boardAppHTML, "/*__STYLES__*/", assets.Styles(), 1)
	return strings.Replace(out, "/*__BOARDJS__*/", assets.BoardJS(), 1)
}

// panelBuild identifies the template's content. It names the URI hosts cache by
// AND is printed in the panel's own footer, so a screenshot answers "which build
// is on screen": the question that could not be answered while a host served a
// cached pre-fix panel and the only view of it was a photograph.
// It hashes whichever template will actually be SERVED, minimal included. It
// used to hash the full one unconditionally, so two daemons: one with
// DIBS_PANEL_MINIMAL set, one without: advertised the identical URI for a
// 264594-byte panel and a 483-byte one. Flipping that variable across a restart
// then left a host holding completely different content under a URI it was told
// was content-derived, which is the exact caching failure the hash exists to
// prevent, reintroduced by the hash not covering the switch.
var panelBuild = func() string {
	source := boardAppTemplate()
	if panelMinimal {
		source = minimalPanelHTML
	}
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])[:12]
}()

// uiBoardBase is the panel template's family name; uiBoardURI adds the hash of
// the template actually being served.
//
// Stable identity is what makes the template cacheable, and a PERMANENTLY stable
// one makes it unfixable. Hosts prefetch by URI and keep the result: this
// server marks it public and hints an hour, and one host held it across a daemon
// restart and every rebuild in a session. So a panel bug shipped to a running
// host stayed shipped: the daemon served the corrected template to anyone who
// asked, and nothing asked. Measured, not guessed: with DIBS_LOG_RPC=1 the
// daemon recorded two tool calls and zero resources/read while the panel drew
// pre-fix markup.
//
// Versioning by CONTENT rather than by release means the URI changes exactly
// when the bytes do: an unchanged panel keeps its identity and its cache, and a
// changed one is a URI nothing has ever cached, so the host must fetch it. The
// tool result carries the current URI in _meta.ui.resourceUri on every call, so
// a host picks the new one up without re-listing resources.
//
// EVERY emission of this URI must be the hashed form, and the tool DECLARATION
// matters most. Hosts resolve an app's template from the `_meta.ui` on the tool
// in tools/list, at connect time, not from the tool result, and not from
// resources/list. For a long stretch the declarations hardcoded the base URI
// while the result and the resource listing carried the hash, so a host cached
// the panel once per connection under a URI that never changed and served that
// copy forever. The panel was fixed on the server and stale on the screen, and
// the two are indistinguishable from the outside, which is the failure mode
// the content hash exists to prevent, arriving through the one door it was not
// applied to. TestNoToolDeclaresTheUnhashedPanelURI holds this.
const uiBoardBase = "ui://dibs/board"

var uiBoardURI = uiBoardBase + "/" + panelBuild

// panelDataMetaKey is intentionally namespaced. Tool-result _meta is delivered
// to the MCP App and withheld from model context; putting the board in ordinary
// structuredContent is the exact context-cost defect board exists to avoid.
const panelDataMetaKey = "com.dibs/panel"

// uiMime is the MIME type the MCP Apps spec reserves for HTML app templates.
const uiMime = "text/html;profile=mcp-app"

// uiResourceDescriptor is the resources/list entry for the board app.
func uiResourceDescriptor() map[string]any {
	return map[string]any{
		"uri":         uiBoardURI,
		"name":        "board-app",
		"description": "Interactive Dibs board (MCP Apps UI template)",
		"mimeType":    uiMime,
	}
}

// readUIBoard serves the template. It is deliberately data-free: hosts cache it
// across invocations, so baking in a board snapshot would serve stale state.
func readUIBoard() map[string]any {
	return map[string]any{"contents": []map[string]any{{
		"uri":      uiBoardURI,
		"mimeType": uiMime,
		"text":     boardApp(),
		"_meta": map[string]any{
			"ui": map[string]any{
				// No external origins: the app renders only what the host hands
				// it and talks solely to the host over postMessage. A restrictive
				// CSP is the correct default and we have nothing to relax it for.
				"csp": map[string]any{
					"connectDomains":  []string{},
					"resourceDomains": []string{},
				},
				"prefersBorder": true,
			},
		},
	}}}
}

// showBoard gathers what the UI renders, for an authenticated caller only.
func (s *Server) showBoard(ctx context.Context, token, view string) (core.Result, error) {
	// Authenticate FIRST, like every other tool.
	//
	// An earlier version served the board to any caller and justified it in a
	// comment reading "the board is public and still worth showing". It is not
	// public: check_in, inbox and the rest all require an agent token, and the
	// board carries agent descriptions, working directories, hostnames and branch
	// names. This tool accepted a token that inbox had rejected seconds earlier
	// and rendered all of it to a human anyway. Reaching the daemon proves you
	// are on this machine; it does not make you a participant.
	// SubscribeInfo is NOT an authenticator, despite the shape. It short-circuits
	// on an empty token to serve the token-less board subscription used by
	// subscriptions/listen, so calling it with "" succeeds, which is exactly how
	// an unauthenticated board slipped through after the bogus-token hole
	// was closed. Reject the empty token here, explicitly.
	if strings.TrimSpace(token) == "" {
		return nil, core.ErrBadToken
	}
	agentID, _, err := s.eng.SubscribeInfo(ctx, token)
	if err != nil {
		return nil, err
	}
	board, err := s.eng.Board(ctx)
	if err != nil {
		return nil, err
	}
	out := core.Result{"board": board, "agent_id": agentID}
	if view == "mail" || view == "board" || view == "activity" {
		out["view"] = view
	}
	if box, err := s.eng.InboxFor(ctx, token); err == nil {
		out["inbox"] = box
	}
	return out, nil
}

// boardSummary is the model-facing line. The UI already shows the detail, so
// spending model context on it twice would be waste.
//
// It reads the JSON-normalised payload, not the engine's typed structs: asserting
// `[]any` against a typed slice silently yields zero, which is how an earlier
// version of this reported "0 agents" while handing the UI seven.
func boardSummary(sc map[string]any, declaredUI bool) string {
	b, _ := sc["board"].(map[string]any)
	agents, _ := b["agents"].([]any)
	var live int
	for _, l := range agents {
		if m, ok := l.(map[string]any); ok {
			if st, _ := m["status"].(string); st == "active" {
				live++
			}
		}
	}
	msg := fmt.Sprintf("Dibs board: %d agent(s), %d active", len(agents), live)
	if n := inboxCount(sc["inbox"]); n > 0 {
		msg += fmt.Sprintf("; %d unread message(s)", n)
	}
	if declaredUI {
		return msg + ". Shown to the human in the board panel."
	}
	// This host did not declare that it renders panels, so whether anything
	// reached the human is not known here, and saying it did is the one thing
	// this must not do.
	//
	// It said it anyway, unconditionally, with no parameter for the answer. A
	// human watching this exact tool call see nothing was told by their agent
	// that the board had been put in front of them, because the tool told the
	// agent so. An agent cannot correct for a result that lies to it, and it
	// will repeat the claim in its own words, which is worse than silence.
	//
	// Not a denial either: the reference host declares nothing and renders from
	// _meta regardless, so "not shown" would be its own false claim. What is
	// true is that it was SENT, and that the agent has a way to see it itself.
	return msg + ". Sent to their panel; this host declares no panel support, so " +
		"they may not see it."
}

// inboxCount counts messages in either shape a mailbox arrives in, through a
// JSON round-trip so named map types and typed slices both resolve. Comparing
// core.Result against map[string]any directly does not match.
func inboxCount(box any) int {
	if box == nil {
		return 0
	}
	b, err := json.Marshal(box)
	if err != nil {
		return 0
	}
	var asObj struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(b, &asObj) == nil && asObj.Messages != nil {
		return len(asObj.Messages)
	}
	var asArr []json.RawMessage
	if json.Unmarshal(b, &asArr) == nil {
		return len(asArr)
	}
	return 0
}

// panelTools are the calls that already carry board or mailbox state, so the
// panel can ride along on work the agent was doing anyway. The human should not
// need the agent to make a second, ceremonial "now show it" call: reading the
// board IS the moment to show the board.
var panelTools = map[string]string{
	"check_in":     "board",    // orientation: who else is here, what are they on
	"inbox":        "mail",     // reading mail shows the mail, not the roster
	"await_events": "activity", // it returned BECAUSE something changed: show that
	"send":         "mail",     // you just wrote to someone; show the thread
	"respond":      "mail",
	"board":        "", // explicit request; honours its own view argument
}

// panelWorthShowing decides whether this particular result has anything the
// human has not already seen.
//
// The panel riding along on every coordination call was the point, but giving
// every call the SAME board turned it into three identical dashboards stacked in
// one conversation. A view that repeats is noise, and noise is what makes people
// stop looking at a panel that matters. So: draw when there is something to say.
func panelWorthShowing(tool string, res core.Result) bool {
	switch tool {
	case "inbox":
		// The inbox tool returns {messages,…} at the TOP level: there is no
		// "inbox" key to look under. Reading the wrong shape here silently
		// suppressed the panel for a mailbox that had mail in it.
		if n := inboxCount(res); n > 0 {
			return true
		}
		// An empty mailbox does not need a panel to announce itself; the summary
		// line already says so in five words.
		return false
	case "await_events":
		// It may have returned on timeout with nothing at all.
		if evs, ok := res["events"].([]any); ok {
			return len(evs) > 0
		}
		return res["events"] != nil
	}
	return true // check_in / board / sends are always deliberate
}

// showBoardResult shapes the tools/call reply per the MCP Apps contract.
//
// declaredUI is the client's own statement that it renders MCP Apps, and it
// decides whether the board also travels in structuredContent. See the comment
// at that assignment: it is the difference between this tool working and this
// tool doing nothing at all on a real host.
func showBoardResult(res core.Result, detail, declaredUI bool) map[string]any {
	full, _ := json.Marshal(res)
	var fullMap map[string]any
	_ = json.Unmarshal(full, &fullMap)
	modelText := boardSummary(fullMap, declaredUI)
	if detail {
		modelText = string(full)
	}
	payload := panelPayload(res)
	out := map[string]any{
		"content": []map[string]any{{"type": "text", "text": modelText}},
		"_meta":   panelMeta(payload),
	}
	if boot := panelBootstrap(payload); boot != nil {
		// The summary travels with the bootstrap, not just in `content`.
		//
		// Hosts choose a carrier. This one shows the model structuredContent
		// INSTEAD of content, so a bootstrap that carried only plumbing replaced
		// the one sentence board owes the agent: it read its own token back
		// and learned nothing about the board.
		boot["summary"] = boardSummary(fullMap, declaredUI)

		// And so does the board itself when the agent asked for it.
		//
		// detail=true was honoured only in `content`, which is precisely the
		// carrier this host drops. So the one documented way for an agent to
		// read the board on purpose returned a summary and its own token back,
		// on the host most likely to be running. Found by an agent that wanted
		// the board, passed detail=true, got a sentence, and went back to
		// talking to the daemon over plain HTTP instead: the tool taught it not
		// to use the tool.
		//
		// Ungated, unlike the panel duplicate below: detail is a request from
		// the MODEL, and what the host declares about rendering panels for the
		// human has no bearing on whether the agent gets what it asked for.
		if detail {
			boot["board"] = fullMap
		}

		// And on a host that says it renders panels, the BOARD goes with it.
		//
		// This tool exists for one purpose: put the board in front of the human.
		// On this host it was achieving nothing at all, and the panel said so in
		// its own words. "no board from this host". Reading that screen backwards
		// gives the whole picture: _meta is dropped, `content` is one summary line
		// with no board in it, structuredContent held only plumbing, and the panel
		// could not fetch because this host does not let an app call tools back.
		// Four carriers, none of them carrying anything. A tool that cannot do the
		// only thing it is for is not cheap, it is broken.
		//
		// So the trimmed payload rides here too, and yes, a host that shows the
		// model structuredContent pays for the board it displays. That is the
		// honest price: board is called BECAUSE a human asked to look, it is
		// trimmed to what the template draws, and detail=true remains the way an
		// agent asks for the board on purpose. A summary line that buys a blank
		// panel is not a saving.
		//
		// Gated on the client's OWN declaration, and only this extra copy is
		// gated. _meta still goes to everyone. That is what keeps the earlier
		// mistake from returning: the reference host declares nothing and renders
		// from _meta, so gating the payload itself starved it silently. Gating a
		// duplicate cannot starve anyone.
		if declaredUI {
			for k, v := range payload {
				if _, taken := boot[k]; !taken {
					boot[k] = v
				}
			}
		}
		out["structuredContent"] = boot
	}
	return out
}

// panelBootstrap is the handful of fields the panel needs to go and FETCH the
// board itself, and nothing else.
//
// _meta is the correct carrier for panel data: it reaches the MCP App and is
// withheld from model context, which is the entire promise board makes.
// Hosts do not all honour it yet. This one declares the app capability, renders
// the template, and hands the component nothing from _meta, so the panel sat on
// "awaiting board · No agents yet" while the daemon held three agents, and the
// 72-character summary in `content` was correct the whole time. That is why no
// measurement of the tool result caught it; only looking at the panel did.
//
// The obvious repair (copy the payload into structuredContent as well) is the
// one thing we must not do. structuredContent is model-facing, so it would hand
// every agent the full board on every call and delete the saving the tool exists
// for, and on check_in it would put a TRIMMED checkpoint where a host may show
// the model a complete one. Both are guarded by tests, and the tests are right.
//
// So what crosses is the bootstrap, not the board: the view, the agent id, and
// the caller's own token, which the model already holds, because it passed the
// token into this very call. Some tens of bytes of things the model already
// knows. With that the panel calls board(detail:true) over its own bridge,
// where the result cannot enter model context at all, and draws from the reply.
// See board_app.html's fetchBoard.
func panelBootstrap(payload core.Result) core.Result {
	out := core.Result{}
	for _, k := range []string{"view", "agent_id", "act_token"} {
		if v, ok := payload[k]; ok {
			out[k] = v
		}
	}
	// No token means no bridge call is possible, so a bootstrap would be an
	// empty object charged to the model for nothing.
	if out["act_token"] == nil {
		return nil
	}
	return out
}

func panelMeta(payload core.Result) map[string]any {
	return map[string]any{
		"ui":             map[string]any{"resourceUri": uiBoardURI},
		panelDataMetaKey: withoutBodies(payload),
	}
}

// withoutBodies strips message text from the panel payload.
//
// The panel had been sharing every unread body with the host through
// ui/update-model-context, which one host renders straight into the operator's
// own composer. That was fixed in the panel, and the fix did not reach anybody:
// MCP Apps templates are CACHED by the host against their ui:// URI, so a
// session that loaded the panel yesterday keeps running yesterday's JavaScript
// and keeps leaking. There is no way to invalidate that from here.
//
// So the bodies stop leaving the daemon. A cached panel cannot share what it
// was never given, which makes this the only version of the fix that is true
// today rather than after every client restarts.
//
// The panel does not lose the mail. It holds the caller's own token and calls
// back over its own bridge, where results cannot enter model context at all;
// that is the same route it already uses for board(detail:true). What changes
// is that the body travels on the private path instead of the one the host is
// entitled to redistribute.
//
// Serials, senders, types and states are kept: everything needed to render a
// mailbox, decide what to open, and fetch it.
func withoutBodies(payload core.Result) core.Result {
	out, _ := redactBodies(payload).(core.Result)
	if out == nil {
		return core.Result{}
	}
	return out
}

// redactBodies walks the whole value and blanks every message body it finds,
// whatever it is wrapped in.
//
// The first version matched two shapes, `inbox` and `messages` as a flat
// []*core.Message, and the real payload is neither: check_in nests the mailbox
// as inbox.messages, so the body went through untouched. An independent
// reviewer found that by constructing the payload the server actually builds
// rather than the one the test imagined.
//
// That is the fifth time this leak has been fixed and the second time the FIX
// was shaped to the wrong data. So this stops matching shapes: it recurses, and
// anything that is a Message gets blanked no matter where it sits. A new field
// that happens to carry a mailbox is covered on the day it is added, which is
// the only version of this guarantee that survives somebody restructuring a
// result.
func redactBodies(v any) any {
	switch t := v.(type) {
	case *core.Message:
		if t == nil {
			return t
		}
		// A copy: the original is the AGENT's own result, delivered over the
		// connection it authenticated on, and it keeps its text.
		c := *t
		c.Body, c.Response = "", ""
		return &c
	case core.Message:
		t.Body, t.Response = "", ""
		return t
	case []*core.Message:
		out := make([]*core.Message, 0, len(t))
		for _, m := range t {
			out = append(out, redactBodies(m).(*core.Message))
		}
		return out
	case core.Result:
		out := core.Result{}
		for k, val := range t {
			out[k] = redactBodies(val)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, val := range t {
			out[k] = redactBodies(val)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, val := range t {
			out = append(out, redactBodies(val))
		}
		return out
	default:
		return v
	}
}

// panelResult attaches the board panel to a tool that already returns board or
// mailbox state.
//
// The agent's own result is NEVER replaced. check_in is the awareness gate,
// the model reads the board out of it to learn what its peers are doing, and an
// earlier version of this swapped that JSON for a prose summary, quietly breaking
// the thing Dibs exists to do. `content` stays exactly what it was; the panel is
// additive.
//
// The panel copy goes to everyone, and that is a deliberate reversal.
//
// It was previously gated on the client declaring io.modelcontextprotocol/ui at
// initialize. The reference host: the actual AppBridge implementation other
// hosts build on: declares `"capabilities":{}` and renders the panel anyway,
// off the tool's _meta.ui.resourceUri. So the declaration is optional in
// practice, and gating on it silently starves every host that renders without
// announcing. That failure is invisible from the server side: the panel draws,
// empty, and looks exactly like a host bug.
//
// The cost of being wrong the other way is bounded and measured: the payload is
// trimmed to the fields the template draws, so a host that cannot render pays
// ~1.5 KB: still less than the 2.1 KB the same call cost before this feature
// existed. Bounded waste beats a feature that silently does not work.
func (s *Server) panelResult(
	ctx context.Context, res core.Result, view, token string, wantsUI, panelFetches bool,
) map[string]any {
	payload := panelPayload(s.panelState(ctx, res, view, token))
	// The bootstrap rides in CONTENT here, never structuredContent, and the
	// difference is the whole recovery checkpoint.
	//
	// board can afford a bootstrap in structuredContent because its content
	// is one summary line. check_in cannot: its content IS the checkpoint: the
	// board, the mailbox, what the agent still owes, and this host displays
	// structuredContent to the model INSTEAD of content. A bootstrap there
	// therefore replaced the answer with three fields of plumbing, and the agent
	// read its own token back and learned nothing about the fleet. Caught by
	// calling check_in as an ordinary agent, not by any test: every assertion
	// about the tool result still passed, because the checkpoint was present the
	// whole time in a field this host does not show.
	//
	// So nothing model-facing is replaced, and the panel still gets what it needs
	// on a host that drops _meta: content already carries the board and mailbox
	// for the agent, and now carries the token to act with: a token the model
	// supplied in this very call, so it is not new information reaching it.
	withBoot := res
	if boot := panelBootstrap(payload); boot != nil {
		withBoot = core.Result{}
		for k, v := range res {
			withBoot[k] = v
		}
		for k, v := range boot {
			if _, taken := withBoot[k]; !taken {
				withBoot[k] = v
			}
		}
	}
	plain, _ := json.Marshal(withBoot)
	_ = wantsUI // retained for the log line; no longer gates the payload

	out := map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(plain)}},
		"_meta":   panelMeta(payload),
	}
	// The duplicate is dropped once a panel has PROVED it can reach us.
	//
	// check_in is called every activation, and the board dominates its size, so
	// sending it in both content and structuredContent charges the model two
	// copies of the fleet per turn: on a large board, most of what the tool
	// costs. The duplication existed for one host shape: drops _meta, forbids an
	// app from calling tools, and shows the model structuredContent INSTEAD of
	// content. There, structuredContent is the panel's only carrier, and a slim
	// one starves the agent, so both had to be whole.
	//
	// Those are the same host. A panel that has called a tool through the bridge
	// has demonstrated the host permits app calls, so it is not that host, so
	// content is what the model reads, and the panel can fetch the board itself
	// rather than being handed it. The proof is observed rather than declared:
	// serverTools is negotiated between host and app, invisible from here, and no
	// published matrix breaks it down per client. The panel marks its own calls
	// and the session remembers.
	//
	// Unproved means duplicate, always. The first ack of a session pays for both
	// copies and every one after it does not; a host whose panel never calls keeps
	// the duplicate forever, which is exactly right, because there it is the only
	// thing that works. Being wrong in this direction costs bytes. Being wrong in
	// the other costs the agent its checkpoint.
	if !panelFetches {
		// EQUAL, never smaller, when it is sent at all. A shape beside content that
		// answers LESS is how check_in once returned a token and nothing about the
		// fleet, on a host that shows structuredContent instead of content. So the
		// rule was never "structuredContent is allowed" but "it is this exact
		// object", and dropping it entirely is safe in a way that shrinking it is
		// not, because a conformant host with no structuredContent falls back to
		// content, while one handed a smaller object believes it.
		out["structuredContent"] = withBoot
	}
	return out
}

// panelState fills in whatever the panel needs that the call did not return.
func (s *Server) panelState(ctx context.Context, res core.Result, view, token string) core.Result {
	out := core.Result{}
	for k, v := range res {
		out[k] = v
	}
	if _, ok := out["board"]; !ok {
		if b, err := s.eng.Board(ctx); err == nil {
			out["board"] = b // the panel needs the board to draw agents at all
		}
	}
	// Normalise the two shapes a mailbox arrives in: `inbox` returns its
	// messages at the top level, `check_in` nests them under "inbox".
	if _, ok := out["inbox"]; !ok {
		if _, has := out["messages"]; has {
			out["inbox"] = core.Result{"messages": out["messages"]}
		}
	}
	// The panel needs the caller's token to act on their behalf: answering a
	// question, approving a request, acknowledging an FYI. Without it the panel
	// can only ever be a picture, and the human has to go back to typing
	// instructions at an agent to do something they can already see.
	//
	// It is the caller's OWN token, travelling to the caller's OWN sandboxed UI,
	// on the same machine, over the connection that token already authenticated.
	// It is not a widening of trust: the agent holding this token handed it to
	// us in this very call.
	if token != "" {
		out["act_token"] = token
	}
	if _, ok := out["agent_id"]; !ok && token != "" {
		if id, _, err := s.eng.SubscribeInfo(ctx, token); err == nil {
			out["agent_id"] = id
		}
	}
	if view != "" {
		out["view"] = view
	}
	return out
}
