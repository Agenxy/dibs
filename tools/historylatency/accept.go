// historylatency runs the unchanged production resource probe on fresh hosted
// runners. Its only overlay is a separate no-warm control test; production and
// the original resource/warming tests remain byte-identical to the pinned SHA.
package main

import (
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

const acceptedSource = "a709e2746f4522591a112aefa0cace11e14e5687"

//go:embed control.go.txt
var controlTest []byte

func main() {
	if err := runAcceptance(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = "source"
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != acceptedSource {
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
	fmt.Printf("HISTORY_ORIGINAL_ACCEPTANCE_SOURCE %s repeat=%s\n", acceptedSource, os.Getenv("DIBS_HISTORY_REPEAT"))
	return nil
}

func originalProbe(arm, test string) error {
	// #nosec G204 -- fixed trusted CI diagnostic commands and test names
	cmd := exec.Command("mise", "exec", "--", "go", "test", "-count=1", "-timeout=20m",
		"-run", "^"+test+"$", "-v", "./internal/ledger")
	cmd.Dir = "source"
	cmd.Env = append(os.Environ(), "DIBS_HISTORY_PROBE_ARM="+arm)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
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
