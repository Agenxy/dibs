// historylatency runs hosted latency experiments against an immutable source.
// Production and its original resource tests remain byte-identical to that SHA.
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const acceptedSource = "e771d2f6603cbed768025a1107615c524669c02b"

const oldSource = "a709e2746f4522591a112aefa0cace11e14e5687"

//go:embed control.go.txt
var controlTest []byte

//go:embed shared_disk.go.txt
var sharedDiskTest []byte

//go:embed activity.go.txt
var activityTest []byte

//go:embed restated.go.txt
var restatedTest []byte

func main() {
	if err := runSelected(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runSelected() error {
	if os.Getenv("DIBS_HISTORY_EXPERIMENT") == "restated-acceptance" {
		return runRestated()
	}
	if os.Getenv("DIBS_HISTORY_EXPERIMENT") == "wiring-old" {
		return runOldWiring()
	}
	if os.Getenv("DIBS_HISTORY_EXPERIMENT") == "shared-disk" {
		return runSharedDisk()
	}
	return runAcceptance()
}

func runRestated() (result error) {
	if err := verifySource(); err != nil {
		return err
	}
	trust := exec.Command("mise", "trust")
	trust.Dir, trust.Stdout, trust.Stderr = "source", os.Stdout, os.Stderr
	if err := trust.Run(); err != nil {
		return err
	}
	if err := os.WriteFile("source/internal/ledger/history_restated_acceptance_test.go", restatedTest, 0o600); err != nil {
		return err
	}
	defer func() { result = errors.Join(result, unchangedSource()) }()
	// Keep the original generator, boot/heap gates and warming sampler verbatim.
	// The explicit new product criterion lives in a separate real-door fixture.
	for _, arm := range []string{"generate", "baseline", "baseline-repeat-1", "baseline-repeat-2", "production"} {
		if err := originalProbe(arm, "TestMailHistoryProductionProbe"); err != nil {
			return err
		}
	}
	return originalProbe("", "TestHistoryRestatedAcceptance")
}

func runOldWiring() (result error) {
	if err := verifySource(); err != nil {
		return err
	}
	trust := exec.Command("mise", "trust")
	trust.Dir, trust.Stdout, trust.Stderr = "source", os.Stdout, os.Stderr
	if err := trust.Run(); err != nil {
		return err
	}
	if err := os.WriteFile("source/internal/ledger/history_activity_old_proof_test.go", activityTest, 0o600); err != nil {
		return err
	}
	restore, err := diskFileSeams()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, restore(), unchangedSource()) }()
	cmd := probeCommand("TestHistoryWriterActivityThroughRealAppend")
	out, probeErr := cmd.CombinedOutput()
	if _, err := os.Stdout.Write(out); err != nil {
		return err
	}
	if probeErr == nil || !bytes.Contains(out, []byte("actual Append did not hold activity through Write/Sync")) ||
		!bytes.Contains(out, []byte("actual failed Append did not hold and release activity")) {
		return errors.New("old writer activity proof did not fail both intended runtime assertions")
	}
	fmt.Println("HISTORY_WRITER_WIRING_OLD_PROOF intended_red=true successful_and_failed_sync=true")
	return nil
}

func runSharedDisk() (result error) {
	if err := verifySource(); err != nil {
		return err
	}
	trust := exec.Command("mise", "trust")
	trust.Dir, trust.Stdout, trust.Stderr = "source", os.Stdout, os.Stderr
	if err := trust.Run(); err != nil {
		return err
	}
	if err := os.WriteFile("source/internal/ledger/history_shared_disk_test.go", sharedDiskTest, 0o600); err != nil {
		return err
	}
	// Only widen two concrete file fields for the test double. The real Open,
	// Replay, Append/Sync, serving trigger and builder remain on their usual path.
	// Restore and verify tracked source even when the probe returns RED.
	restore, err := diskFileSeams()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, restore(), unchangedSource()) }()
	if err := originalProbe("", "TestHistoryWriterActivityThroughRealAppend"); err != nil {
		return err
	}
	if err := originalProbe("generate", "TestMailHistoryProductionProbe"); err != nil {
		return err
	}
	return originalProbe("", "TestHistorySharedDiskDiagnostic")
}

func diskFileSeams() (func() error, error) {
	paths := []string{"source/internal/ledger/ledger.go", "source/internal/ledger/mail_history_bootstrap.go"}
	originals := make([][]byte, len(paths))
	widened := make([][]byte, len(paths))
	for i, path := range paths {
		// #nosec G304 -- the two fixed source paths immediately above
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		originals[i] = raw
		widened[i], err = widenDiskField(raw, i == 1)
		if err != nil {
			return nil, err
		}
	}
	restore := func() error { return restoreDiskFields(paths, originals, widened) }
	for i, path := range paths {
		// #nosec G304 -- the two fixed source paths immediately above
		if err := os.WriteFile(path, widened[i], 0o600); err != nil {
			return nil, errors.Join(err, restore())
		}
	}
	return restore, nil
}

func widenDiskField(raw []byte, bootstrap bool) ([]byte, error) {
	already := "f        ledgerFile"
	if bootstrap {
		already = "file    io.ReaderAt"
	}
	if bytes.Count(raw, []byte(already)) == 1 {
		return raw, nil // fixed production already exposes the native file ports
	}
	old := "f        *os.File"
	replacement := "f interface { io.ReadWriteSeeker; io.ReaderAt; " +
		"Sync() error; Close() error; Truncate(int64) error }"
	if bootstrap {
		old, replacement = "file    *os.File", "file    io.ReaderAt"
		if bytes.Count(raw, []byte("\n\t\"os\"")) != 1 {
			return nil, errors.New("setup: unexpected bootstrap os import")
		}
		raw = bytes.Replace(raw, []byte("\n\t\"os\""), nil, 1)
	}
	if bytes.Count(raw, []byte(old)) != 1 {
		return nil, errors.New("setup: unexpected concrete file seam")
	}
	return bytes.Replace(raw, []byte(old), []byte(replacement), 1), nil
}

func restoreDiskFields(paths []string, originals, widened [][]byte) error {
	var errs []error
	for i, path := range paths {
		// #nosec G304 -- only the fixed source paths in diskFileSeams reach here
		raw, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(raw, widened[i]) {
			errs = append(errs, errors.New("setup: widened source changed during diagnostic"), err)
		}
		// #nosec G304 -- only the fixed source paths in diskFileSeams reach here
		errs = append(errs, os.WriteFile(path, originals[i], 0o600))
	}
	return errors.Join(errs...)
}

func runAcceptance() error {
	count, err := strconv.Atoi(os.Getenv("DIBS_HISTORY_PROBE_RECORDS"))
	if err != nil || (count != 1_000_000 && count != 2_000_000) {
		return errors.New("setup: records must be 1M or 2M")
	}
	if err := verifySource(); err != nil {
		return err
	}
	trust := exec.Command("mise", "trust")
	trust.Dir, trust.Stdout, trust.Stderr = "source", os.Stdout, os.Stderr
	if err := trust.Run(); err != nil {
		return err
	}
	if err := os.WriteFile("source/internal/ledger/history_no_warm_acceptance_test.go", controlTest, 0o600); err != nil {
		return err
	}
	for _, arm := range []string{"generate", "baseline", "baseline-repeat-1", "baseline-repeat-2"} {
		if err := originalProbe(arm, "TestMailHistoryProductionProbe"); err != nil {
			return err
		}
	}
	if err := originalProbe("", "TestHistoryNoWarmAcceptanceControl"); err != nil {
		return err
	}
	probeErr := originalProbe("production", "TestMailHistoryProductionProbe")
	// Always read a completed probe's receipts even if a resource gate failed.
	// A setup failure without receipts is a failure, not a latency measurement.
	receiptErr := judgeReceipts(count)
	cleanErr := unchangedSource()
	return errors.Join(probeErr, receiptErr, cleanErr)
}

func verifySource() error {
	expected := acceptedSource
	if os.Getenv("DIBS_HISTORY_EXPERIMENT") == "wiring-old" {
		expected = oldSource
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = "source"
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != expected {
		return fmt.Errorf("setup: wrong source %q", out)
	}
	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = "source"
	out, err = cmd.Output()
	if err != nil {
		return err
	}
	if len(out) != 0 {
		return fmt.Errorf("setup: dirty source %q", out)
	}
	fmt.Printf("HISTORY_ORIGINAL_ACCEPTANCE_SOURCE %s repeat=%s\n", expected, os.Getenv("DIBS_HISTORY_REPEAT"))
	return nil
}

func originalProbe(arm, test string) error {
	cmd := probeCommand(test)
	cmd.Env = append(os.Environ(), "DIBS_HISTORY_PROBE_ARM="+arm)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func probeCommand(test string) *exec.Cmd {
	// #nosec G204 -- fixed trusted CI diagnostic commands and test names
	cmd := exec.Command("mise", "exec", "--", "go", "test", "-count=1", "-timeout=20m",
		"-run", "^"+test+"$", "-v", "./internal/ledger")
	cmd.Dir = "source"
	return cmd
}

func unchangedSource() error {
	cmd := exec.Command("git", "diff", "--exit-code", "HEAD")
	cmd.Dir, cmd.Stdout, cmd.Stderr = "source", os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("setup: tracked source changed: %w", err)
	}
	return nil
}

func judgeReceipts(records int) error {
	raw, err := os.ReadFile("source/internal/ledger/.history-production-probe/no-warm.json")
	if err != nil {
		return err
	}
	var control struct {
		Samples int     `json:"samples"`
		Records int     `json:"records"`
		P99     float64 `json:"p99_seconds"`
	}
	if err := json.Unmarshal(raw, &control); err != nil {
		return err
	}
	if control.Samples != 2048 || control.Records != records || control.P99 <= 0 {
		return errors.New("setup: invalid no-warm receipt")
	}
	raw, err = os.ReadFile("source/internal/ledger/.history-production-probe/production.json")
	if err != nil {
		return err
	}
	var production struct {
		Records int     `json:"records"`
		P99     float64 `json:"warming_coordination_p99_seconds"`
	}
	if err := json.Unmarshal(raw, &production); err != nil {
		return err
	}
	if production.Records != records || production.P99 <= 0 {
		return errors.New("setup: invalid production receipt")
	}
	bound := math.Max(2*control.P99, .010)
	fmt.Printf("HISTORY_ORIGINAL_ACCEPTANCE records=%d repeat=%s "+
		"no_warm_p99_ms=%.6f warm_p99_ms=%.6f bound_ms=%.6f passes=%t\n",
		records, os.Getenv("DIBS_HISTORY_REPEAT"), control.P99*1000, production.P99*1000, bound*1000, production.P99 <= bound)
	if production.P99 > bound {
		return errors.New("original warming writer p99 exceeds prespecified bound")
	}
	return nil
}
