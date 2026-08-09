package plugins

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"testing"
)

// The embedded copy must match the canonical plugin in the repository root.
//
// Embedding cannot reach above its own package, so the files a connecting agent
// receives are a COPY of plugins/ rather than the thing itself. That copy is a
// liability the moment it drifts: an agent would be handed a plugin that no
// longer matches the one this project ships and tests, and the failure would
// appear as hooks that quietly do not fire — indistinguishable from not having
// installed it. skills.md has the same arrangement for the same reason.
func TestEmbeddedPluginsMatchTheRepository(t *testing.T) {
	// Walk the CANONICAL tree, rather than checking a hand-written list of pairs.
	//
	// The list could only ever verify the files somebody remembered to add to it,
	// so it was blind to the failure that actually happened: a bare `go:embed
	// data` skipped every dotfile, and .claude-plugin/plugin.json and .mcp.json
	// vanished from a payload documented as the whole plugin while this test
	// stayed green. A missing file is the defect this exists to catch, so the
	// source of truth has to be the directory, not the test.
	for _, dir := range []string{"claude-code", "codex"} {
		root := filepath.Join("..", "..", "plugins", dir)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return rerr
			}
			want, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			embedded := path.Join("data", dir, filepath.ToSlash(rel))
			got, gerr := files.ReadFile(embedded)
			if gerr != nil {
				// Reported, not returned: a missing file is the defect being looked
				// for, and aborting the walk on the first one would hide the rest.
				// The whole point is to list everything the payload is short of.
				t.Errorf("plugins/%s/%s is not embedded — an agent installing from "+
					"lanes://plugin would write an incomplete plugin. Copy it to "+
					"internal/plugins/%s (and remember go:embed needs all: for dotfiles)",
					dir, rel, embedded)
				return nil //nolint:nilerr // continue the walk; every gap should be named
			}
			if string(got) != string(want) {
				t.Errorf("plugins/%s/%s has drifted from its embedded copy", dir, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	marketplace, err := files.ReadFile("data/marketplace.json")
	if err != nil {
		t.Fatalf("embedded marketplace.json missing: %v", err)
	}
	canonical, err := os.ReadFile(filepath.Join("..", "..", ".claude-plugin", "marketplace.json"))
	if err != nil {
		t.Fatalf("canonical marketplace.json missing: %v", err)
	}
	if string(marketplace) != string(canonical) {
		t.Error("the embedded marketplace descriptor has drifted from .claude-plugin/marketplace.json")
	}
}

// The Claude Code payload must carry the files that make it a plugin at all.
//
// Named explicitly, on top of the walk above, because these two are the ones
// whose absence is invisible: hooks.json and SKILL.md were present, so the
// payload looked plausible, while the manifest and the MCP server definition
// were missing and an offline install could not have worked.
func TestTheClaudeCodePluginCarriesItsManifestAndServer(t *testing.T) {
	p, ok := For("claude-code")
	if !ok {
		t.Fatal("no claude-code plugin")
	}
	for _, needed := range []string{
		".claude-plugin/plugin.json",
		".mcp.json",
		"hooks/hooks.json",
		"skills/lanes/SKILL.md",
	} {
		if _, has := p.Files[needed]; !has {
			t.Errorf("claude-code payload is missing %s — %v", needed, keysOf(p.Files))
		}
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestEveryPluginIsActionable(t *testing.T) {
	for _, p := range All() {
		if len(p.Files) == 0 {
			t.Errorf("%s has no files — an agent told to install it has nothing to write", p.Harness)
		}
		if p.Verify == "" {
			t.Errorf("%s has no end-to-end verification", p.Harness)
		}
		if len(p.Setup) == 0 {
			t.Errorf("%s has no setup steps", p.Harness)
		}
		for i, s := range p.Setup {
			if s.Do == "" || s.Check == "" {
				t.Errorf("%s step %d is missing do or check — a step whose effect cannot "+
					"be confirmed lets a broken setup look finished", p.Harness, i)
			}
		}
	}
}

// The harness name agents actually report must resolve.
//
// register_lane takes `harness` as free text and agents spell it differently.
// Answering "no plugin exists" to a spelling variant would reproduce exactly the
// gap this package closes, so the variants seen in the wild are pinned.
func TestHarnessNamesResolveHowAgentsSpellThem(t *testing.T) {
	for _, name := range []string{
		"claude-code", "claude_code", "Claude Code", "claude", "CLAUDECODE",
		"codex", "chatgpt-desktop", "gpt",
	} {
		if _, ok := For(name); !ok {
			t.Errorf("For(%q) found nothing — an agent reporting this harness would be "+
				"told there is no plugin for it", name)
		}
	}
	if _, ok := For("emacs"); ok {
		t.Error("For(\"emacs\") matched something; unknown harnesses must not be given " +
			"a plugin that does not fit them")
	}
}
