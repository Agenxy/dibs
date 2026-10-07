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
	{"classified-capture", "scope-after", "8a9b36599df08785eb574b1bc64d95fbb723fc67"},
}

type writerCostReceipt struct {
	Arm       string  `json:"arm"`
	Samples   int     `json:"samples"`
	P99       float64 `json:"p99_seconds"`
	Initial   int     `json:"initial_live_messages"`
	Final     int     `json:"final_live_messages"`
	CopiedMin int     `json:"copied_min"`
	CopiedMax int     `json:"copied_max"`
}

type writerCostPair struct {
	Repeat int               `json:"repeat"`
	Arm    string            `json:"arm"`
	Before writerCostReceipt `json:"before"`
	After  writerCostReceipt `json:"after"`
}

func measureWriterCost() error {
	// Same encrypted fixture, same native observer and same host for both arms.
	if err := installWriterCostFixture(); err != nil {
		return err
	}
	if _, err := writerCostProbe(writerStages[0], "generate"); err != nil {
		return err
	}
	if err := copyWriterCostFixture(); err != nil {
		return err
	}
	for repeat := 1; repeat <= 3; repeat++ {
		for _, arm := range []string{"check-in", "send", "none"} {
			if err := pairWriterCost(repeat, arm); err != nil {
				return err
			}
		}
	}
	return nil
}

func installWriterCostFixture() error {
	// #nosec G304 -- fixed native Go fixture template.
	raw, err := os.ReadFile("tools/historyallocationproof/writer_cost.go.txt")
	if err != nil {
		return err
	}
	for _, s := range writerStages {
		if err := prepare(s); err != nil {
			return err
		}
		path := filepath.Join(s.dir, "internal/ledger/history_writer_scope_cost_test.go")
		// #nosec G703 -- fixed test file in a disposable immutable source checkout.
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func copyWriterCostFixture() error {
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
	return nil
}

func pairWriterCost(repeat int, arm string) error {
	before, err := writerReceipt(writerStages[0], arm)
	if err != nil {
		return err
	}
	after, err := writerReceipt(writerStages[1], arm)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(writerCostPair{repeat, arm, before, after})
	if err != nil {
		return err
	}
	fmt.Printf("HISTORY_WRITER_SCOPE_PAIRED %s\n", raw)
	return nil
}

func writerReceipt(s stage, arm string) (writerCostReceipt, error) {
	var receipt writerCostReceipt
	raw, err := writerCostProbe(s, arm)
	if err != nil {
		return receipt, err
	}
	match := regexp.MustCompile(`HISTORY_WRITER_SCOPE_COST (\{[^\n]+\})`).FindSubmatch(raw)
	if len(match) != 2 {
		return receipt, errors.New("missing valid native 10k writer receipt")
	}
	if err := json.Unmarshal(match[1], &receipt); err != nil {
		return receipt, err
	}
	if receipt.Initial != 10000 || receipt.Arm != arm || receipt.Samples <= 0 || receipt.P99 <= 0 {
		return receipt, errors.New("native live-mail setup counts changed")
	}
	return receipt, nil
}

func writerCostProbe(s stage, arm string) ([]byte, error) {
	// #nosec G204 -- immutable test plan, no shell or external command arguments.
	cmd := exec.Command("mise", "exec", "--", "go", "test", "-count=1", "-timeout=10m",
		"-run", "^TestHistoryWriterScopeCost$", "-v", "./internal/ledger")
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), "DIBS_HISTORY_SCOPE_ARM="+arm)
	raw, err := cmd.CombinedOutput()
	if _, writeErr := os.Stdout.Write(raw); writeErr != nil {
		return nil, writeErr
	}
	return raw, err
}
