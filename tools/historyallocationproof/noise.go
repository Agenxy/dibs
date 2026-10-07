package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Noise keeps the original heap ceiling and reports five identical-fixture
// repetitions before any policy change. Only the measurement point is altered.
func measureNoise() error {
	s := stages[len(stages)-1]
	if err := prepare(s); err != nil {
		return err
	}
	if err := installNoiseMeasurement(s); err != nil {
		return err
	}
	if _, err := probe(s, "generate"); err != nil {
		return err
	}
	for _, arm := range []string{"baseline", "baseline-repeat-1", "baseline-repeat-2"} {
		if _, err := probe(s, arm); err != nil {
			return err
		}
	}
	var minimum, maximum float64
	var failures []error
	for repeat := 1; repeat <= 5; repeat++ {
		heap, verdict, err := noiseRepeat(s, repeat)
		if err != nil {
			return err
		}
		if repeat == 1 {
			minimum, maximum = heap, heap
		}
		minimum, maximum = min(minimum, heap), max(maximum, heap)
		if verdict != nil {
			failures = append(failures, fmt.Errorf("noise repeat %d: %w", repeat, verdict))
		}
	}
	fmt.Printf("HISTORY_ALLOCATION_NOISE_SPREAD minimum_heap_bytes=%.0f maximum_heap_bytes=%.0f spread_bytes=%.0f\n",
		minimum, maximum, maximum-minimum)
	return errors.Join(failures...)
}

func namedMemoryFailure(raw []byte) bool {
	text := string(raw)
	expected := strings.Contains(text, "production representation exceeds accepted retained-memory ceiling") ||
		strings.Contains(text, "warming peak exceeds steady heap plus")
	return expected && !strings.Contains(text, "setup:") && !strings.Contains(text, "panic:")
}

func installNoiseMeasurement(s stage) error {
	path := filepath.Join(s.dir, "internal", "ledger", "mail_history_production_probe_test.go")
	// #nosec G304 G703 -- fixed source manifest, only a disposable hosted checkout.
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	old := []byte("\truntime.GC()\n\truntime.ReadMemStats(&mem)\n\treceipt := map[string]any{")
	if bytes.Count(original, old) != 1 {
		return errors.New("noise measurement insertion must match once")
	}
	replacement := []byte(`	runtime.GC()
 time.Sleep(25 * time.Millisecond)
 runtime.GC()
 runtime.ReadMemStats(&mem)
 if arm == "production" {
  profile, profileErr := os.Create(filepath.Join(dir, "allocation-heap.pprof"))
  if profileErr != nil {
   t.Fatal("setup: heap profile:", profileErr)
  }
  profileErr = pprof.WriteHeapProfile(profile)
  closeErr := profile.Close()
  if profileErr != nil || closeErr != nil {
   t.Fatal("setup: heap profile:", profileErr, closeErr)
  }
 }
 receipt := map[string]any{`)
	changed := bytes.Replace(original, old, replacement, 1)
	oldImport := []byte(`"runtime"`)
	if bytes.Count(changed, oldImport) != 1 {
		return errors.New("noise profile import must match once")
	}
	changed = bytes.Replace(changed, oldImport, []byte("\"runtime\"\n\"runtime/pprof\""), 1)
	// #nosec G703 -- exact measured-only substitution in a disposable source fixture.
	return os.WriteFile(path, changed, 0o600)
}

func retainNoiseProfile(s stage, repeat int) error {
	dir := filepath.Join(s.dir, "internal", "ledger", ".history-production-probe")
	source := filepath.Join(dir, "allocation-heap.pprof")
	destination := filepath.Join(dir, fmt.Sprintf("allocation-heap-%d.pprof", repeat))
	if err := copyFile(source, destination); err != nil {
		return err
	}
	relative := filepath.Join("internal", "ledger", ".history-production-probe", fmt.Sprintf("allocation-heap-%d.pprof", repeat))
	raw, err := command(s.dir, "mise", "exec", "--", "go", "tool", "pprof", "-top", "-inuse_space", "-nodefraction=0", relative)
	if err != nil {
		return err
	}
	fmt.Printf("HISTORY_ALLOCATION_PROFILE repeat=%d (sampled inuse_space, not exact retained bytes)\n%s", repeat, raw)
	// #nosec G703 -- fixed hosted evidence artifact path.
	return os.WriteFile(destination+".txt", raw, 0o600)
}

func noiseRepeat(s stage, repeat int) (float64, error, error) {
	raw, verdict := probe(s, "production")
	receipt, err := productionReceipt(raw)
	if err != nil {
		return 0, verdict, errors.Join(verdict, err)
	}
	if verdict != nil && !namedMemoryFailure(raw) {
		return 0, verdict, verdict
	}
	heap, ok := receipt["heap_alloc_bytes"].(float64)
	if !ok {
		return 0, verdict, errors.New("noise heap receipt missing")
	}
	receipt["repeat"], receipt["source"], receipt["original_checker_pass"] = repeat, s.sha, verdict == nil
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return 0, verdict, err
	}
	fmt.Printf("HISTORY_ALLOCATION_NOISE %s\n", encoded)
	return heap, verdict, retainNoiseProfile(s, repeat)
}
