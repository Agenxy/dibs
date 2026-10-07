// historylatency is a disposable hosted diagnostic, never a release component.
// It overlays one identical test on two immutable sources and shares encrypted
// fixture bytes. Production files in both checkouts remain untouched.
package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const samplerSource = "a709e2746f4522591a112aefa0cace11e14e5687"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if n := os.Getenv("DIBS_HISTORY_PROBE_RECORDS"); n != "1000000" && n != "2000000" {
		return fmt.Errorf("setup: records must be 1000000 or 2000000")
	}
	if err := prepareSources(); err != nil {
		return err
	}
	if err := prepareFixture(); err != nil {
		return err
	}
	return measureArms()
}

func prepareSources() error {
	for _, root := range []string{"old", "new"} {
		if err := verify(root, samplerSource); err != nil {
			return err
		}
		trust := exec.Command("mise", "trust")
		trust.Dir, trust.Stdout, trust.Stderr = root, os.Stdout, os.Stderr
		if err := trust.Run(); err != nil {
			return fmt.Errorf("setup: trust %s: %w", root, err)
		}
		destination := filepath.Join(root, "internal/ledger/mail_history_warming_probe_test.go")
		if err := os.Remove(destination); err != nil {
			return err
		}
		if err := copyFile("tools/historylatency/sampler.go.txt", destination); err != nil {
			return err
		}
	}
	return nil
}

func prepareFixture() error {
	if err := command("old", "generate", "", "go", "test", "-count=1", "-timeout=20m",
		"-run", "^TestMailHistoryProductionProbe$", "-v", "./internal/ledger"); err != nil {
		return fmt.Errorf("setup: generator: %w", err)
	}
	fixture := "internal/ledger/.history-production-probe"
	if err := os.MkdirAll(filepath.Join("new", fixture), 0o700); err != nil {
		return err
	}
	for _, name := range []string{"key", "ledger.jsonl"} {
		if err := copyFile(filepath.Join("old", fixture, name), filepath.Join("new", fixture, name)); err != nil {
			return err
		}
	}
	var fixtureHashes [][32]byte
	for _, root := range []string{"old", "new"} {
		hash, err := printHash(filepath.Join(root, fixture, "ledger.jsonl"))
		if err != nil {
			return err
		}
		fixtureHashes = append(fixtureHashes, hash)
	}
	if fixtureHashes[0] != fixtureHashes[1] {
		return fmt.Errorf("setup: encrypted fixture hashes disagree")
	}
	return nil
}

func measureArms() error {
	if err := prepareControls(); err != nil {
		return err
	}
	// Prespecified ABBA order prevents always giving the newer source the warm
	// runner. Each test process starts from a fresh copy of the same S0 bytes.
	var failed bool
	for n, root := range []string{"old", "new", "new", "old"} {
		label := fmt.Sprintf("sampler-%s-%d", root, n+1)
		if err := command(root, "production", label, "go", "test", "-count=1", "-timeout=10m",
			"-run", "^TestMailHistoryProductionProbe$", "-v", "./internal/ledger"); err != nil {
			fmt.Fprintln(os.Stderr, label, err)
			failed = true
		}
	}
	for _, root := range []string{"old", "new"} {
		check := exec.Command("git", "diff", "--name-only", "HEAD")
		check.Dir = root
		out, err := check.Output()
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(out)) != "internal/ledger/mail_history_warming_probe_test.go" {
			return fmt.Errorf("setup: unexpected source change in %s: %q", root, out)
		}
	}
	if failed {
		return fmt.Errorf("one or more latency arms failed; retain all receipts")
	}
	return nil
}

func prepareControls() error {
	for _, root := range []string{"old", "new"} {
		for _, arm := range []string{"baseline", "baseline-repeat-1", "baseline-repeat-2"} {
			if err := command(root, arm, "", "go", "test", "-count=1", "-timeout=10m",
				"-run", "^TestMailHistoryProductionProbe$", "-v", "./internal/ledger"); err != nil {
				return err
			}
		}
		if err := command(root, "", "no-warm-"+root, "go", "test", "-count=1", "-timeout=10m",
			"-run", "^TestHistorySamplerNoWarm$", "-v", "./internal/ledger"); err != nil {
			return err
		}
	}
	return nil
}

func verify(root, expected string) error {
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "HEAD"}, expected},
		{[]string{"status", "--porcelain"}, ""},
	} {
		cmd := exec.Command("git", check.args...) // #nosec G204 -- constant diagnostic arguments
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("setup: %s %v: %w", root, check.args, err)
		}
		if strings.TrimSpace(string(out)) != check.want {
			return fmt.Errorf("setup: %s %v: got %q want %q", root, check.args, out, check.want)
		}
	}
	fmt.Printf("HISTORY_LATENCY_SOURCE root=%s sha=%s\n", root, expected)
	return nil
}

func command(root, arm, label string, args ...string) error {
	// #nosec G204 -- fixed hosted diagnostic commands
	cmd := exec.Command("mise", append([]string{"exec", "--"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DIBS_HISTORY_PROBE_ARM="+arm, "DIBS_HISTORY_LATENCY_LABEL="+label)
	period := "0"
	if root == "new" {
		period = "100"
	}
	cmd.Env = append(cmd.Env, "DIBS_HISTORY_HEAP_SAMPLE_PERIOD_MS="+period)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func copyFile(from, to string) error {
	in, err := os.Open(from) // #nosec G304 -- fixed trusted CI checkout paths
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- fixed trusted CI checkout paths
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func printHash(path string) ([32]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- fixed encrypted fixture path
	if err != nil {
		return [32]byte{}, err
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	n, err := io.Copy(hash, f)
	if err != nil {
		return [32]byte{}, err
	}
	fmt.Printf("HISTORY_LATENCY_FIXTURE path=%s bytes=%d sha256=%x\n", path, n, hash.Sum(nil))
	var sum [32]byte
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}
