// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// publicinstall exercises published bytes, never a local candidate or a Mac
// cask. Receipts distinguish commands, verified archives and actual MCP work.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/selfupdate"
)

type options struct {
	mode, tag, out, runtime, image string
	fresh                          int
}

type step struct {
	Name    string   `json:"name"`
	Args    []string `json:"args"`
	Exit    int      `json:"exit"`
	Log     string   `json:"log"`
	Seconds float64  `json:"seconds"`
}

type receipt struct {
	Tag      string `json:"tag"`
	Platform string `json:"platform"`
	Mode     string `json:"mode"`
	Passed   bool   `json:"passed"`
	Error    string `json:"error,omitempty"`
	Steps    []step `json:"steps"`
	// These count only first install attempts, never setup/recovery commands.
	Attempts        int       `json:"fresh_install_attempts"`
	InstallFailed   int       `json:"fresh_install_failures"`
	EPIPE           int       `json:"epipe_failures"`
	Recovered       int       `json:"recovered_failed_installs"`
	FailureRate     *float64  `json:"fresh_install_failure_rate"`
	MCPRoundTrips   int       `json:"mcp_round_trips"`
	ArchiveVerified bool      `json:"archive_signature_and_digest_verified"`
	GoTag           string    `json:"go_install_tag"`
	ServiceLaunch   string    `json:"service_launch"`
	Environment     string    `json:"execution_environment,omitempty"`
	HostedRun       string    `json:"hosted_run,omitempty"`
	Findings        []finding `json:"findings,omitempty"`
}

type finding struct {
	Step    string `json:"step"`
	Warning string `json:"warning"`
	FixedBy string `json:"fixed_by,omitempty"`
}

type runner struct {
	ctx    context.Context
	root   string
	env    []string
	report receipt
}

func main() { os.Exit(runCLI()) }

func runCLI() int {
	var o options
	flag.StringVar(&o.mode, "mode", "native",
		"native archive/Go paths, linux containers, linux-hosted, or internal linux-child")
	flag.StringVar(&o.tag, "tag", "", "exact published tag, for example v0.0.17")
	flag.StringVar(&o.out, "out", "", "new receipt directory; existing directories are refused")
	flag.StringVar(&o.runtime, "runtime", "container", "container or docker; never starts the runtime service")
	flag.StringVar(&o.image, "image", "docker.io/homebrew/brew:latest", "Homebrew image; observed digest is recorded")
	flag.IntVar(&o.fresh, "fresh-installs", 5, "fresh Linux containers, 1 through 20")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	if err := execute(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, "publicinstall:", err)
		return 1
	}
	return 0
}

func validate(o options, goos string) error {
	if _, err := selfupdate.ReleaseForTag(o.tag); err != nil {
		return err
	}
	if o.out == "" {
		return errors.New("-out must name a new receipt directory")
	}
	if err := validateMode(o, goos); err != nil {
		return err
	}
	if o.fresh < 1 || o.fresh > 20 {
		return errors.New("-fresh-installs must be between 1 and 20")
	}
	if o.runtime != "container" && o.runtime != "docker" {
		return errors.New("-runtime must be container or docker")
	}
	return nil
}

func validateMode(o options, goos string) error {
	if goos != "darwin" && goos != "linux" {
		return errors.New("only macOS and Linux public paths are covered")
	}
	switch o.mode {
	case "native", "linux":
		return nil
	case "linux-child":
		if goos != "linux" || os.Getenv("DIBS_PUBLIC_INSTALL_CHILD") != "1" {
			return errors.New("linux-child is only for the explicitly isolated Linux container")
		}
	case "linux-hosted":
		if goos != "linux" || os.Getenv("GITHUB_ACTIONS") != "true" ||
			os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || o.fresh != 1 {
			return errors.New("linux-hosted requires one fresh attempt on an ephemeral GitHub-hosted Linux runner")
		}
	default:
		return errors.New("unknown -mode")
	}
	return nil
}

func execute(ctx context.Context, o options) (err error) {
	if err := validate(o, runtime.GOOS); err != nil {
		return err
	}
	root, err := filepath.Abs(o.out)
	if err != nil {
		return err
	}
	// #nosec G703 -- explicit NEW output root; never reuse or remove a caller's directory
	if err := os.Mkdir(root, 0o700); err != nil {
		return err
	}
	r := &runner{ctx: ctx, root: root, report: receipt{
		Tag: o.tag, Mode: o.mode,
		Platform: runtime.GOOS + "/" + runtime.GOARCH, ServiceLaunch: "not exercised; no native service registration",
	}}
	defer func() {
		if r.report.Attempts > 0 {
			rate := float64(r.report.InstallFailed) / float64(r.report.Attempts)
			r.report.FailureRate = &rate
		}
		r.report.Passed = err == nil
		if err != nil {
			r.report.Error = err.Error()
		}
		b, marshalErr := json.MarshalIndent(r.report, "", "  ")
		if marshalErr != nil {
			err = errors.Join(err, marshalErr)
			return
		}
		// #nosec G703 -- fixed receipt below the newly created output root
		if writeErr := os.WriteFile(filepath.Join(root, "receipt.json"), append(b, '\n'), 0o600); writeErr != nil {
			err = errors.Join(err, writeErr)
		}
	}()
	if o.mode == "linux" {
		return r.linuxContainers(o)
	}
	if err := r.prepareNativeMode(o); err != nil {
		return err
	}
	return r.native(o.tag)
}

func (r *runner) prepareNativeMode(o options) error {
	if o.mode == "linux-hosted" {
		r.report.Environment = "GitHub-hosted Linux runner; not the Mac container runtime"
		r.report.HostedRun = os.Getenv("GITHUB_SERVER_URL") + "/" + os.Getenv("GITHUB_REPOSITORY") +
			"/actions/runs/" + os.Getenv("GITHUB_RUN_ID") + "/attempts/" + os.Getenv("GITHUB_RUN_ATTEMPT")
		r.env = isolatedEnv(os.Getenv("HOME"), r.root)
		if err := r.brew(o.tag); err != nil {
			return err
		}
	}
	if o.mode == "linux-child" {
		marker, markerErr := os.ReadFile("/probe/owned-fixture")
		if markerErr != nil || string(marker) != o.tag+"\n" || r.root != "/receipts/run" {
			return errors.New("linux child requires its tag-bound readonly fixture marker and isolated receipt mount")
		}
		r.env = isolatedEnv(os.Getenv("HOME"), r.root)
		if err := r.brew(o.tag); err != nil {
			return err
		}
	}
	return nil
}

// No harness credentials, app session, board override or existing Go/Codex
// state can enter a child. PATH and explicit toolchain/cache settings suffice.
func isolatedEnv(home, root string) []string {
	var env []string
	for _, key := range []string{"PATH", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR", "GOTOOLCHAIN"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return append(env, "HOME="+home, "USERPROFILE="+home, "CODEX_HOME="+filepath.Join(home, ".codex"),
		"DIBS_DIR="+filepath.Join(home, ".dibs"), "DIBS_ADDR=127.0.0.1:1", "DIBS_NOTIFY=off", "DIBS_ALLOW_PARALLEL=1",
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"GOPATH="+filepath.Join(root, "gopath"), "GOCACHE="+filepath.Join(root, "go-cache"),
		"GOBIN="+filepath.Join(home, "bin"), "GOPROXY=https://proxy.golang.org,direct", "GOSUMDB=sum.golang.org",
		"HOMEBREW_NO_ANALYTICS=1", "HOMEBREW_NO_ENV_HINTS=1")
}

func (r *runner) command(name string, env []string, args ...string) (string, error) {
	start := time.Now()
	// #nosec G204 G702 -- argv only; explicit local tools or signature-verified fixture binaries
	cmd := exec.CommandContext(r.ctx, args[0], args[1:]...)
	cmd.Env = env
	cmd.Dir = r.root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	log := fmt.Sprintf("%03d-%s.log", len(r.report.Steps)+1, name)
	out := stdout.String() + stderr.String()
	r.recordWarnings(name, stdout.String()+"\n"+stderr.String())
	// #nosec G703 -- fixed step log under the new receipt directory
	writeErr := os.WriteFile(filepath.Join(r.root, log),
		[]byte("stdout:\n"+stdout.String()+"\nstderr:\n"+stderr.String()), 0o600)
	code := 0
	if err != nil {
		code = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
	}
	r.report.Steps = append(r.report.Steps, step{name, args, code, log, time.Since(start).Seconds()})
	if err != nil {
		return out, fmt.Errorf("%s exited %d (see %s): %w", name, code, log, err)
	}
	return stdout.String(), writeErr
}

func (r *runner) recordWarnings(name, out string) {
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Warning:") {
			continue
		}
		f := finding{Step: name, Warning: line}
		if strings.Contains(line, "Calling `postflight` is deprecated") {
			f.FixedBy = "PR #519 on main; published v0.0.16 still has the legacy postflight"
		}
		r.report.Findings = append(r.report.Findings, f)
	}
}

func (r *runner) native(tag string) error {
	rel, err := selfupdate.ReleaseForTag(tag)
	if err != nil {
		return err
	}
	archive := filepath.Join(r.root, "archive")
	if err := os.Mkdir(archive, 0o700); err != nil {
		return err
	} // #nosec G703 -- new fixture subdirectory
	checksums, err := selfupdate.Verify(r.ctx, &http.Client{Timeout: 2 * time.Minute}, rel, archive)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	if err := selfupdate.Fetch(r.ctx, client, rel, runtime.GOOS, runtime.GOARCH, archive, checksums); err != nil {
		return err
	}
	r.report.ArchiveVerified = true
	asset, err := selfupdate.ArchiveName(rel.Version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	archivePath := filepath.Join(archive, asset)
	if _, err := r.command("archive-shasum", os.Environ(), "shasum", "-a", "256", archivePath); err != nil {
		return err
	}
	for _, source := range []string{"archive", "go"} {
		home := filepath.Join(r.root, source+"-home")
		if err := os.Mkdir(home, 0o700); err != nil {
			return err
		} // #nosec G703 -- own fixture home
		env := isolatedEnv(home, r.root)
		bin := archive
		if source == "go" {
			bin = filepath.Join(home, "bin")
			for _, name := range []string{"dibs", "dibd"} {
				module := "github.com/agenxy/dibs/cmd/" + name + "@" + tag
				if _, err := r.command("go-install-"+name, env, "go", "install", module); err != nil {
					return err
				}
			}
			r.report.GoTag = tag
		}
		if err := r.smoke(bin, home, env); err != nil {
			return fmt.Errorf("%s MCP: %w", source, err)
		}
	}
	return nil
}

func caskVersion(out, tag string) error {
	var info struct {
		Casks []struct{ Token, Version string }
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return err
	}
	if len(info.Casks) != 1 || info.Casks[0].Token != "dibs" || info.Casks[0].Version != strings.TrimPrefix(tag, "v") {
		return errors.New("public tap does not name the requested tag; merge its cask before acceptance")
	}
	return nil
}

func (r *runner) brew(tag string) error {
	for _, args := range [][]string{
		{"brew", "update"}, {"brew", "--version"}, {"brew", "tap", "agenxy/tap"}, {"brew", "trust", "agenxy/tap"},
	} {
		if _, err := r.command("brew-setup", r.env, args...); err != nil {
			return err
		}
	}
	info, err := r.command("brew-tag", r.env, "brew", "info", "--json=v2", "--cask", "agenxy/tap/dibs")
	if err != nil {
		return err
	}
	if err := caskVersion(info, tag); err != nil {
		return err
	}
	if _, err := r.command("public-prerequisites", r.env, "brew", "install", "go", "cosign"); err != nil {
		return err
	}
	failed, err := r.brewFirstInstall(tag)
	if err != nil {
		return err
	}
	// This is intentionally outside Homebrew's temporary-HOME sandbox.
	if _, err := r.command("linux-service-setup", r.env, "dibs", "configure", "--service"); err != nil {
		return err
	}
	if _, err := r.command("brew-reinstall", r.env, "brew", "reinstall", "--cask", "agenxy/tap/dibs"); err != nil {
		return err
	}
	if _, err := r.command("linux-repeat-service-setup", r.env, "dibs", "configure", "--service"); err != nil {
		return err
	}
	prefix, err := r.command("brew-prefix", r.env, "brew", "--prefix")
	if err != nil {
		return err
	}
	if err := r.smoke(filepath.Join(strings.TrimSpace(prefix), "bin"), os.Getenv("HOME"), r.env); err != nil {
		return err
	}
	if failed {
		r.report.Recovered++
	}
	return nil
}

func (r *runner) brewFirstInstall(tag string) (bool, error) {
	r.report.Attempts++
	out, installErr := r.command("brew-install", r.env, "brew", "install", "--cask", "agenxy/tap/dibs")
	if installErr != nil {
		r.report.InstallFailed++
		if !strings.Contains(out, "Broken pipe") {
			return true, installErr
		}
		r.report.EPIPE++
		listed, err := r.command("brew-failed-registration", r.env, "brew", "list", "--cask", "--versions", "dibs")
		if err != nil || strings.TrimSpace(listed) != "dibs "+strings.TrimPrefix(tag, "v") {
			return true, errors.New("failed install is not registered at the requested version; recovery unproved")
		}
		if _, err := r.command("brew-recovery-upgrade", r.env, "brew", "upgrade", "--cask", "agenxy/tap/dibs"); err != nil {
			return true, err
		}
	}
	return installErr != nil, nil
}
