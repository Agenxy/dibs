// historyallocationproof measures approved allocation stages on one hosted
// runner and identical encrypted bytes. Original resource verdicts stay intact.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type stage struct{ name, dir, sha string }

var stages = []stage{
	{"before", "allocation-before", "359ba2fe4df6d1c9d4a8ed555c55e1040a05cc0f"},
	{"segments", "allocation-segments", "689f3b4d447c4695e53878d58a04857047f0dbfd"},
	{"idle-buffers", "allocation-idle", "0f3508ea6fedc171a928df45a72b50efd24f6be4"},
	{"parser", "allocation-parser", "6e9707a8e6ccefe439e2deb4009db0b77f56bfa0"},
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	for _, s := range stages {
		if err := prepare(s); err != nil {
			return err
		}
	}
	if _, err := probe(stages[0], "generate"); err != nil {
		return err
	}
	for n, s := range stages {
		if n > 0 {
			if err := copyFixture(s); err != nil {
				return err
			}
		}
		if err := measure(s, n == len(stages)-1); err != nil {
			return err
		}
	}
	return nil
}

func prepare(s stage) error {
	raw, err := command(s.dir, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) != s.sha {
		return errors.New("allocation source identity mismatch")
	}
	if _, err := command(s.dir, "git", "diff", "--exit-code"); err != nil {
		return err
	}
	_, err = command(s.dir, "mise", "trust")
	return err
}

func command(dir, name string, args ...string) ([]byte, error) {
	// #nosec G204 -- fixed hosted proof plan, no shell or external argv.
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return raw, fmt.Errorf("%s: %w: %s", name, err, raw)
	}
	return raw, nil
}

func probe(s stage, arm string) ([]byte, error) {
	cmd := exec.Command("mise", "exec", "--", "go", "test", "-count=1", "-timeout=20m",
		"-run", "^TestMailHistoryProductionProbe$", "-v", "./internal/ledger")
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), "DIBS_HISTORY_PROBE_RECORDS=100000", "DIBS_HISTORY_PROBE_CASE=large-body",
		"DIBS_HISTORY_PROBE_ARM="+arm)
	raw, err := cmd.CombinedOutput()
	if _, writeErr := os.Stdout.Write(raw); writeErr != nil {
		return nil, writeErr
	}
	return raw, err
}

func copyFixture(s stage) error {
	dir := filepath.Join(s.dir, "internal", "ledger", ".history-production-probe")
	// #nosec G703 -- directory from the constant stage manifest.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, file := range []string{"key", "ledger.jsonl"} {
		source := filepath.Join(stages[0].dir, "internal", "ledger", ".history-production-probe", file)
		if err := copyFile(source, filepath.Join(dir, file)); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source, destination string) error {
	// #nosec G304 G703 -- constant fixture filenames in immutable hosted checkouts.
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	// #nosec G304 G703 -- constant fixture filenames in immutable hosted checkouts.
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Close())
}

func measure(s stage, required bool) error {
	for _, arm := range []string{"baseline", "baseline-repeat-1", "baseline-repeat-2"} {
		if _, err := probe(s, arm); err != nil {
			return fmt.Errorf("allocation setup %s/%s: %w", s.name, arm, err)
		}
	}
	raw, exit := probe(s, "production")
	receipt, err := productionReceipt(raw)
	if err != nil {
		return errors.Join(exit, err)
	}
	receipt["stage"], receipt["source"], receipt["original_checker_pass"] = s.name, s.sha, exit == nil
	result, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	fmt.Printf("HISTORY_ALLOCATION_STAGE %s\n", result)
	if exit != nil {
		// A preceding stage may fail its unchanged memory bound. Only that named
		// runtime failure is a usable measurement, never compilation/setup failure.
		text := string(raw)
		expected := strings.Contains(text, "production representation exceeds accepted retained-memory ceiling") ||
			strings.Contains(text, "warming peak exceeds steady heap plus")
		if required || !expected || strings.Contains(text, "setup:") || strings.Contains(text, "panic:") {
			return fmt.Errorf("allocation stage %s failed: %w", s.name, exit)
		}
	}
	return nil
}

func productionReceipt(raw []byte) (map[string]any, error) {
	pattern := regexp.MustCompile(`(?m)HISTORY_PRODUCTION_MEASUREMENT (\{[^\n]+\})`)
	for _, m := range pattern.FindAllSubmatch(raw, -1) {
		var row map[string]any
		if err := json.Unmarshal(m[1], &row); err != nil {
			return nil, err
		}
		if row["arm"] == "production" && row["case"] == "large-body" && row["records"] == float64(100000) {
			return row, nil
		}
	}
	return nil, errors.New("named native production allocation receipt missing")
}
