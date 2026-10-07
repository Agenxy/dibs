// historyapiproof runs hosted controls against an immutable feature source.
// The original green source is checked first. Compiler/setup failures never
// count as intended RED, and all mutations are restored before the next arm.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	candidate   = "3e80ed7d4422f33e25004638ae8c4f0976989063"
	beforeAPI   = "e771d2f6603cbed768025a1107615c524669c02b"
	beforeBytes = "c241fe34f3b50d0b67634c297741ee3fadadb237"
)

type (
	result struct {
		action string
		output string
	}
	mutation struct {
		name, path, before, after string
		guards                    map[string]string
	}
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if err := verify("source", candidate); err != nil {
		return err
	}
	if err := verify("old", beforeAPI); err != nil {
		return err
	}
	if err := verify("old-seek", beforeBytes); err != nil {
		return err
	}
	for _, dir := range []string{"source", "old", "old-seek"} {
		if _, err := command(dir, "mise", "trust"); err != nil {
			return err
		}
	}
	expected, err := testNames("source", []string{
		"internal/mailhistory/query_native_test.go",
		"internal/mailhistory/query_content_test.go",
		"internal/mcp/mail_history_test.go",
	})
	if err != nil {
		return err
	}
	got, exit, err := test("source", "^TestMailHistory(Native|RealMCP)")
	if err != nil {
		return err
	}
	if exit != nil {
		return fmt.Errorf("candidate baseline failed; no mutation verdict allowed: %w", exit)
	}
	for _, name := range expected {
		if got[name].action != "pass" {
			return fmt.Errorf("candidate baseline did not pass %s", name)
		}
	}
	fmt.Printf("HISTORY_API_BASELINE source=%s tests=%d green=true\n", candidate, len(expected))
	if err := oldProof(); err != nil {
		return err
	}
	if err := oldSeekProof(); err != nil {
		return err
	}
	for _, m := range mutations() {
		if err := mutate(m); err != nil {
			return err
		}
	}
	return verify("source", candidate)
}

func verify(dir, sha string) error {
	raw, err := command(dir, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) != sha {
		return errors.New("immutable source identity mismatch")
	}
	_, err = command(dir, "git", "diff", "--exit-code")
	return err
}

func command(dir, name string, args ...string) ([]byte, error) {
	// #nosec G204 -- fixed proof-plan argv, never a shell or external input.
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return raw, fmt.Errorf("%s %v: %w: %s", name, args, err, raw)
	}
	return raw, nil
}

func test(dir, selector string) (map[string]result, error, error) {
	// #nosec G204 -- fixed mise/go argv; selector names come from the immutable fixture.
	cmd := exec.Command("mise", "exec", "--", "go", "test", "-race", "-count=1", "-timeout=40s", "-json", "-run", selector,
		"./internal/mailhistory", "./internal/mcp")
	cmd.Dir = dir
	raw, exit := cmd.CombinedOutput()
	// JSON records prove named runtime assertions were reached. Merely seeing
	// a nonzero process exit would admit compiler errors and broken fixtures.
	got := map[string]result{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		var ev struct{ Action, Test, Output string }
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Test == "" {
			continue
		}
		name, _, _ := strings.Cut(ev.Test, "/")
		r := got[name]
		r.output += ev.Output
		if ev.Test == name && (ev.Action == "pass" || ev.Action == "fail") {
			r.action = ev.Action
		}
		got[name] = r
	}
	if err := scanner.Err(); err != nil {
		return nil, exit, err
	}
	if _, err := os.Stdout.Write(raw); err != nil {
		return nil, exit, err
	}
	return got, exit, nil
}

func testNames(dir string, files []string) ([]string, error) {
	pattern := regexp.MustCompile(`(?m)^func (TestMailHistory(?:Native|RealMCP)\w+)\(`)
	var out []string
	for _, file := range files {
		// #nosec G304 -- immutable hosted checkout and constant fixture manifest.
		raw, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			return nil, err
		}
		for _, m := range pattern.FindAllSubmatch(raw, -1) {
			out = append(out, string(m[1]))
		}
	}
	if len(out) == 0 {
		return nil, errors.New("fixture test list is empty")
	}
	return out, nil
}

func oldProof() error {
	files := []string{
		"internal/mailhistory/query_export_test.go",
		"internal/mailhistory/query_native_test.go",
		"internal/mcp/mail_history_test.go",
	}
	for _, file := range files {
		// #nosec G304 -- immutable source checkout and constant fixture manifest.
		raw, err := os.ReadFile(filepath.Join("source", file))
		if err != nil {
			return err
		}
		// #nosec G703 -- old checkout receives only identical constant fixture paths.
		if err := os.WriteFile(filepath.Join("old", file), raw, 0o600); err != nil {
			return err
		}
	}
	names, err := testNames("old", files[1:])
	if err != nil {
		return err
	}
	for _, name := range names {
		got, exit, err := test("old", "^"+name+"$")
		if err != nil {
			return err
		}
		marker := "history real MCP door failed"
		if strings.HasPrefix(name, "TestMailHistoryNative") {
			marker = "history public query is missing after real native setup"
		}
		if err := intended(got, exit, name, marker); err != nil {
			return err
		}
		fmt.Printf("HISTORY_API_OLD source=%s test=%s intended_red=missing_public_api\n", beforeAPI, name)
	}
	// This is API absence proof, not the conditional race discriminator. Those
	// separately remove the exact runtime safeguard from the green feature.
	return nil
}

func oldSeekProof() error {
	for _, file := range []string{
		"internal/mailhistory/query_export_test.go",
		"internal/mailhistory/query_native_test.go",
		"internal/mailhistory/query_content_test.go",
	} {
		// #nosec G304 -- immutable source checkout and constant fixture manifest.
		raw, err := os.ReadFile(filepath.Join("source", file))
		if err != nil {
			return err
		}
		// #nosec G703 -- old checkout receives only identical constant fixture paths.
		if err := os.WriteFile(filepath.Join("old-seek", file), raw, 0o600); err != nil {
			return err
		}
	}
	name := "TestMailHistoryNativeSmallBodySurvivesLargeValidSeekInterval"
	got, exit, err := test("old-seek", "^"+name+"$")
	if err != nil {
		return err
	}
	marker := "small authorized body remained unavailable in a large valid interval"
	if err := intended(got, exit, name, marker); err != nil {
		return err
	}
	fmt.Printf("HISTORY_API_OLD_SEEK source=%s test=%s intended_red=valid_interval_unavailable\n", beforeBytes, name)
	return nil
}

func intended(got map[string]result, exit error, name, marker string) error {
	r := got[name]
	if exit == nil || r.action != "fail" || !strings.Contains(r.output, marker) ||
		strings.Contains(r.output, "setup:") || strings.Contains(r.output, "panic:") {
		return errors.Join(exit, fmt.Errorf("%s did not reach intended runtime RED %q: action=%q output=%s",
			name, marker, r.action, r.output))
	}
	return nil
}

func mutate(m mutation) (failure error) {
	path := filepath.Join("source", m.path)
	// #nosec G304 -- mutation target from the fixed, checked source manifest.
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.Count(string(original), m.before) != 1 {
		return fmt.Errorf("mutation %s must match exactly once", m.name)
	}
	changed := strings.Replace(string(original), m.before, m.after, 1)
	// #nosec G703 -- disposable source fixture, fixed mutation target.
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		return err
	}
	// Restore the same verified source bytes, including failure exits.
	defer func() {
		// #nosec G703 -- same fixed mutation target, restore-only cleanup.
		failure = errors.Join(failure, os.WriteFile(path, original, 0o600))
	}()
	for name, marker := range m.guards {
		got, exit, err := test("source", "^"+name+"$")
		if err != nil {
			return err
		}
		if err := intended(got, exit, name, marker); err != nil {
			return err
		}
		fmt.Printf("HISTORY_API_MUTATION source=%s safeguard=%s test=%s intended_red=true\n", candidate, m.name, name)
	}
	return nil
}

func mutations() []mutation {
	return []mutation{
		{
			"final-reauthorization", "internal/engine/mail_history.go",
			"\tchecked, err = e.historyReauthorize(ctx, token, reader, index, page, upper, fence)\n" +
				"\tif err != nil || checked[\"error\"] != nil {\n" +
				"\t\treturn checked, err\n" +
				"\t}\n", "",
			map[string]string{
				"TestMailHistoryNativeOwnershipChangeAfterContentReadRefusesWholePage": "ownership move durin" +
					"g actual I/O must refuse whole page",
				// #nosec G101 -- a test name and failure assertion, not a credential.
				"TestMailHistoryNativeTokenRevokedAfterContentReadRefusesWholePage": "final token check disclosed a page",
			},
		},
		{
			"catch-up-ownership-fence", "internal/mailhistory/page.go",
			"\t\t\tif s.Drained.Serial < req.OwnershipChange {\n\t\t\t\treturn s, ErrSettling\n\t\t\t}\n", "",
			map[string]string{
				"TestMailHistoryNativeOwnershipFenceBeforeRemovedLiveHeader": "held consumer blocked query past budget",
			},
		},
		{
			"suffix-authentication", "internal/ledger/mail_history_content.go",
			"consumed != length || previous != seek.Hash || found == nil", "consumed != length || found == nil",
			map[string]string{
				"TestMailHistoryNativeContentAuthenticatesTheSuffixAfterRequestedRecord": "unauthenticated nati" +
					"ve suffix disclosed content",
			},
		},
		{
			"cursor-generation", "internal/mailhistory/page.go",
			"c.Generation != status.Generation || ", "",
			map[string]string{
				"TestMailHistoryRealMCPForeignAndForgedCursorsRefuseWithoutRows": "forged cursor accepted or leaked rows",
			},
		},
		{
			"cursor-party-membership", "internal/mailhistory/page.go",
			"\t\tif !i.partyReference(partyKey{req.Reader.ID, req.Reader.CreatedSerial}, c.Reference) {\n" +
				"\t\t\treturn 0, 0, ErrCursor\n" +
				"\t\t}\n", "",
			map[string]string{
				"TestMailHistoryRealMCPForeignAndForgedCursorsRefuseWithoutRows": "foreign-party cursor disclosed a row",
			},
		},
		{
			"encoded-wire-bound", "internal/engine/mail_history.go",
			"const historyPageBytes = 60 << 10", "const historyPageBytes = 512 << 10",
			map[string]string{"TestMailHistoryRealMCPWireBoundAndArguments": "history exceeded actual MCP wire bound"},
		},
		{
			"observation-semantics", "internal/engine/mail_history.go",
			"e.authObserve(token, time.Now())", "e.authRead(token, time.Now())",
			map[string]string{
				"TestMailHistoryNativeObservationDoesNotWakeOrAdvanceDurableState": "history observation " +
					"woke a sleeping row or advanced durable/read state",
			},
		},
	}
}
