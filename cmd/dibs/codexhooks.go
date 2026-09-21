package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/sibling"
)

// dibs codex-hooks: what Codex makes of the Dibs hooks, and `--trust` to let
// them run.
//
// Since 0.153 Codex treats a hook from the user's own configuration as
// UNTRUSTED until a person reviews it, and drops an untrusted hook at
// discovery without telling the session. That leaves the whole delivery path
// looking installed and delivering nothing: measured 2026-09-19 on
// 0.155.0-alpha.9.2, zero `hook_poll` calls per session with the plugin in
// place, two with the review bypassed. The review lives in the TUI (`/hooks`,
// and a prompt at startup), which the ChatGPT app and `codex exec` never show.
//
// This command does what the TUI's trust button does, over the same protocol
// the TUI uses: `codex app-server` on stdio, `hooks/list` for each hook's key
// and current hash as Codex computes them, then `config/batchWrite` of
// `hooks.state.<key>.trusted_hash`. Nothing here guesses a hash or edits
// config.toml by hand, and only hooks whose source is Dibs are touched. It is
// an admin verb: a person runs it, and it prints exactly what it trusted.
const codexHooksHelp = `dibs codex-hooks [--trust]

Lists the Dibs hooks as Codex sees them: event, source, and whether Codex has
trusted them, which since Codex 0.153 decides whether they run at all.

  --trust    record trust for the Dibs hooks, the way Codex's own /hooks does,
             so mail is delivered at Codex's lifecycle boundaries

Reads and writes ~/.codex/config.toml through Codex's app-server protocol; the
binary is found on PATH, in the usual install directories, or inside the
ChatGPT app. Trusts nothing that is not a Dibs hook.
`

// chatGPTCodex is where the desktop app keeps its own Codex, which is often
// newer than the one on PATH and is the one its sessions run.
const chatGPTCodex = "/Applications/ChatGPT.app/Contents/Resources/codex"

func codexHooksCmd(args []string) error {
	trust := false
	for _, a := range args {
		switch a {
		case "--trust":
			trust = true
		case "--help", "-h":
			fmt.Print(codexHooksHelp)
			return nil
		default:
			return fmt.Errorf("dibs codex-hooks: unknown argument %q\n\n%s", a, codexHooksHelp)
		}
	}
	return adminOnly("codex-hooks", func() error { return codexHooks(os.Stdout, trust) })
}

func codexHooks(out io.Writer, trust bool) error {
	bin := codexBinary()
	if bin == "" {
		return errors.New("no codex binary found on PATH, in ~/.local/bin, or inside the ChatGPT app")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	app, err := startAppServer(ctx, bin)
	if err != nil {
		return err
	}
	defer app.close()
	hooks, err := app.dibsHooks()
	if err != nil {
		return err
	}
	if len(hooks) == 0 {
		_, _ = fmt.Fprintln(out, "Codex reports no Dibs hooks: install the plugin (`codex plugin marketplace add "+
			"<path to the dibs checkout>` then `codex plugin add dibs@dibs`), or put plugins/codex/hooks.json "+
			"at ~/.codex/hooks.json")
		return nil
	}
	_, _ = fmt.Fprintf(out, "codex: %s\n", bin)
	listHooks(out, hooks)
	pending, disabled := sortHooks(hooks)
	if len(pending) == 0 {
		reportTrusted(out, hooks, disabled)
		return nil
	}
	if !trust {
		_, _ = fmt.Fprintf(out, "%d of %d not trusted, so Codex will not run them. Re-run with --trust, or trust them "+
			"in the Codex TUI with /hooks\n", len(pending), len(hooks))
		return nil
	}
	if err := app.trust(pending); err != nil {
		return err
	}
	after, err := app.dibsHooks()
	if err != nil {
		return err
	}
	still := 0
	for _, h := range after {
		if h.TrustStatus != "trusted" {
			still++
			_, _ = fmt.Fprintf(out, "  still %s: %s\n", h.TrustStatus, h.Key)
		}
	}
	if still > 0 {
		return fmt.Errorf("%d hook(s) are still not trusted after the write: Codex did not accept it", still)
	}
	_, _ = fmt.Fprintf(out, "trusted %d hook(s); Codex sessions started from now deliver Dibs mail\n", len(pending))
	return nil
}

// codexBinary prefers the ChatGPT app's Codex when it exists: that is the one
// the desktop app's sessions run, and the one on PATH is often an older shim.
func codexBinary() string {
	if st, err := os.Stat(chatGPTCodex); err == nil && !st.IsDir() {
		return chatGPTCodex
	}
	return sibling.Find("codex")
}

// listHooks prints each hook with the two things that decide whether it
// runs: Codex's trust, and Codex's own on/off switch.
func listHooks(out io.Writer, hooks []codexHook) {
	for _, h := range hooks {
		state := h.TrustStatus
		if h.off() {
			state += ", off"
		}
		_, _ = fmt.Fprintf(out, "  %-14s %-14s %s\n", h.EventName, state, h.Key)
	}
}

// sortHooks splits the set into the ones still to trust and the ones
// trusted but switched off, which need different sentences and different
// remedies: `--trust` fixes the first and cannot touch the second.
func sortHooks(hooks []codexHook) (pending, disabled []codexHook) {
	for _, h := range hooks {
		switch {
		case h.TrustStatus != "trusted":
			pending = append(pending, h)
		case h.off():
			disabled = append(disabled, h)
		}
	}
	return pending, disabled
}

// reportTrusted says what a fully trusted set of hooks will actually do,
// which is not the same question as whether it is trusted: Codex has a
// separate switch per hook, and `--trust` cannot touch it.
func reportTrusted(out io.Writer, hooks, disabled []codexHook) {
	if len(disabled) == 0 {
		_, _ = fmt.Fprintln(out, "all trusted: Codex runs them, and mail reaches its agents at their turn boundaries")
		return
	}
	names := make([]string, 0, len(disabled))
	for _, h := range disabled {
		names = append(names, h.EventName)
	}
	_, _ = fmt.Fprintf(out, "all trusted, but Codex has %d of %d switched OFF: %s. "+
		"A hook that is off does not run, whatever its trust says; turn them back on "+
		"in the Codex TUI with /hooks\n", len(disabled), len(hooks), strings.Join(names, ", "))
}

type codexHook struct {
	Key         string `json:"key"`
	EventName   string `json:"eventName"`
	TrustStatus string `json:"trustStatus"`
	PluginID    string `json:"pluginId"`
	SourcePath  string `json:"sourcePath"`
	CurrentHash string `json:"currentHash"`
	// The handler, flattened into the entry by Codex (HookHandlerMetadata):
	// "mcpTool" with a server and a tool, or "command" with a command.
	HandlerType string `json:"handlerType"`
	Server      string `json:"server"`
	Tool        string `json:"tool"`
	// Enabled is Codex's own per-hook switch (`[hooks.state.<key>]
	// enabled` in its config, toggled in the TUI's hook browser), and it
	// is separate from trust: `hooks/list` returns disabled hooks too.
	// Reading trust alone let `dibs codex-hooks` and `dibs doctor` report
	// "mail is delivered at its lifecycle boundaries" about a hook a
	// person had switched off. Whether the executor skips a disabled hook
	// was not measured here; what is certain is that we cannot claim
	// delivery for one, so the report says what it can see. Round
	// forty-six of the pre-release review.
	Enabled *bool `json:"enabled"`
}

// off reports a hook Codex lists as disabled. A payload with no field at
// all (an older Codex) is not disabled: absence is not a no, which is the
// same rule the rest of this codebase applies to an absent fact.
func (h codexHook) off() bool { return h.Enabled != nil && !*h.Enabled }

// live reports a hook that will actually run: trusted, and not switched off.
func (h codexHook) live() bool { return h.TrustStatus == "trusted" && !h.off() }

// isDibsHook is the one shape `--trust` will vouch for: an MCP tool hook
// calling hook_poll on the dibs server. That is what the plugin ships and
// what `dibs mcp-config` prints, and nothing else.
//
// It used to be "from the dibs plugin, or any loose hook whose JSON mentions
// hook_poll", and the second half matched the STRING anywhere in the entry:
// a command hook with `statusMessage: "hook_poll"` qualified whatever its
// command ran, and so did an MCP hook on another server. `--trust` then
// recorded that hook's hash, which is Codex's authorisation to run it: a
// trust command for Dibs's hooks vouching for a stranger's. The plugin id
// alone is not enough either, since a plugin is named by its marketplace and
// `dibs@` is a prefix anyone can publish under. Found by the pre-release
// review, round four.
func isDibsHook(h codexHook) bool {
	return h.HandlerType == "mcpTool" && h.Server == "dibs" && h.Tool == "hook_poll"
}

// appServer is one `codex app-server` process spoken to over newline-framed
// JSON-RPC on its stdio, the transport the TUI and the desktop app use.
type appServer struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	nextID int
}

func startAppServer(ctx context.Context, bin string) (*appServer, error) {
	cmd := exec.CommandContext(ctx, bin, "app-server") // #nosec G204 -- the operator's own codex
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s app-server: %w", bin, err)
	}
	a := &appServer{cmd: cmd, in: in, out: bufio.NewReaderSize(outPipe, 1<<20)}
	var init json.RawMessage
	if err := a.call("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "dibs", "version": version},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &init); err != nil {
		a.close()
		return nil, fmt.Errorf("codex app-server did not initialize: %w", err)
	}
	if err := a.notify("initialized"); err != nil {
		a.close()
		return nil, err
	}
	return a, nil
}

func (a *appServer) close() {
	_ = a.in.Close()
	_ = a.cmd.Process.Kill()
	_ = a.cmd.Wait()
}

func (a *appServer) notify(method string) error {
	return a.send(map[string]any{"jsonrpc": "2.0", "method": method})
}

func (a *appServer) send(msg map[string]any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = a.in.Write(append(b, '\n'))
	return err
}

// call sends one request and reads until its reply, skipping the server's
// notifications, which arrive interleaved and are not ours to act on.
func (a *appServer) call(method string, params any, result any) error {
	a.nextID++
	id := a.nextID
	if err := a.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		line, err := a.out.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("codex app-server closed during %s: %w", method, err)
		}
		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &msg) != nil || msg.ID == nil || *msg.ID != id {
			continue
		}
		if msg.Error != nil {
			return fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(msg.Result, result)
	}
}

// dibsHooks lists the hooks Codex discovered for the current directory and
// keeps the ones that are Dibs's, by the handler they run (isDibsHook).
func (a *appServer) dibsHooks() ([]codexHook, error) {
	cwd, _ := os.Getwd()
	var res struct {
		Data []struct {
			Hooks []json.RawMessage `json:"hooks"`
		} `json:"data"`
	}
	if err := a.call("hooks/list", map[string]any{"cwds": []string{cwd}}, &res); err != nil {
		return nil, err
	}
	var hooks []codexHook
	for _, entry := range res.Data {
		for _, raw := range entry.Hooks {
			var h codexHook
			if json.Unmarshal(raw, &h) != nil {
				continue
			}
			if isDibsHook(h) {
				hooks = append(hooks, h)
			}
		}
	}
	return hooks, nil
}

// trust records each hook's current hash, which is what the TUI writes when a
// person presses trust: `hooks.state.<key> = {trusted_hash}` upserted through
// config/batchWrite, with the user config reloaded so the change is live.
func (a *appServer) trust(hooks []codexHook) error {
	value := map[string]any{}
	for _, h := range hooks {
		value[h.Key] = map[string]any{"trusted_hash": h.CurrentHash}
	}
	return a.call("config/batchWrite", map[string]any{
		"edits": []map[string]any{{
			"keyPath":       "hooks.state",
			"value":         value,
			"mergeStrategy": "upsert",
		}},
		"reloadUserConfig": true,
	}, nil)
}
