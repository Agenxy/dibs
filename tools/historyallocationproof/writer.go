package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

var writerStages = []stage{
	{"full-capture", "scope-before", "b640e08d79e262e09a9f187cabf6d572e229a75a"},
	{"classified-capture", "scope-after", "52818a7cb66f06ccc7996fab59e3de710f39701a"},
}

func measureWriterCost() error {
	// Same encrypted fixture, same native observer and same host for both arms.
	// There is no locally authored baseline timing or acceptance substitute.
	for _, s := range writerStages {
		if err := prepare(s); err != nil {
			return err
		}
		// #nosec G304 -- fixed native Go fixture template.
		raw, err := os.ReadFile("tools/historyallocationproof/writer_cost.go.txt")
		if err != nil {
			return err
		}
		// #nosec G703 -- fixed test file in the disposable immutable source checkout.
		if err := os.WriteFile(filepath.Join(s.dir, "internal/ledger/history_writer_scope_cost_test.go"), raw, 0o600); err != nil {
			return err
		}
	}
	if _, err := writerCostProbe(writerStages[0], "generate"); err != nil {
		return err
	}
	dir := filepath.Join(writerStages[1].dir, "internal/ledger/.history-scope-cost")
	// #nosec G703 -- fixed fixture directory in the after checkout.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, file := range []string{"key", "ledger.jsonl"} {
		source := filepath.Join(writerStages[0].dir, "internal/ledger/.history-scope-cost", file)
		if err := copyFile(source, filepath.Join(dir, file)); err != nil {
			return err
		}
	}
	for repeat := 1; repeat <= 3; repeat++ {
		for _, arm := range []string{"check-in", "send", "none"} {
			receipts := make([]map[string]any, 0, 2)
			for _, s := range writerStages {
				raw, err := writerCostProbe(s, arm)
				if err != nil {
					return err
				}
				match := regexp.MustCompile(`HISTORY_WRITER_SCOPE_COST (\{[^\n]+\})`).FindSubmatch(raw)
				if len(match) != 2 {
					return errors.New("missing valid native 10k writer receipt")
				}
				var receipt map[string]any
				if err := json.Unmarshal(match[1], &receipt); err != nil {
					return err
				}
				if receipt["initial_live_messages"] != float64(10000) || receipt["arm"] != arm {
					return errors.New("native live-mail setup counts changed")
				}
				receipts = append(receipts, receipt)
			}
			raw, err := json.Marshal(map[string]any{"repeat": repeat, "arm": arm, "before": receipts[0], "after": receipts[1]})
			if err != nil {
				return err
			}
			fmt.Printf("HISTORY_WRITER_SCOPE_PAIRED %s\n", raw)
		}
	}
	return nil
}

func writerCostProbe(s stage, arm string) ([]byte, error) {
	// #nosec G204 -- immutable test plan, no shell or external command arguments.
	cmd := exec.Command("mise", "exec", "--", "go", "test", "-count=1", "-timeout=10m", "-run", "^TestHistoryWriterScopeCost$", "-v", "./internal/ledger")
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), "DIBS_HISTORY_SCOPE_ARM="+arm)
	raw, err := cmd.CombinedOutput()
	if _, writeErr := os.Stdout.Write(raw); writeErr != nil {
		return nil, writeErr
	}
	return raw, err
}
