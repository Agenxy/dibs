// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

func (r *runner) prepareLinux(o options) (string, string, error) {
	if _, err := r.command("runtime-version", os.Environ(), o.runtime, "--version"); err != nil {
		return "", "", err
	}
	if o.runtime == "container" {
		properties, err := r.command("runtime-properties", os.Environ(), "container", "system", "property", "list")
		if err != nil {
			return "", "", err
		}
		var props struct{ Vminit struct{ Image string } }
		if _, err := toml.Decode(properties, &props); err != nil || props.Vminit.Image == "" {
			return "", "", errors.New("cannot identify the runtime's vminit image; inspect container system property list")
		}
		_, err = r.command("runtime-init-image", os.Environ(), "container", "image", "inspect", props.Vminit.Image)
		if err != nil {
			return "", "", fmt.Errorf("runtime prerequisite missing: pull %s and run a smoke container first: %w",
				props.Vminit.Image, err)
		}
	}
	if _, err := r.command("image-pull", os.Environ(), o.runtime, "image", "pull", o.image); err != nil {
		return "", "", err
	}
	inspection, err := r.command("image-inspect", os.Environ(), o.runtime, "image", "inspect", o.image)
	if err != nil {
		return "", "", err
	}
	image, err := resolvedImage(o.image, inspection)
	if err != nil {
		return "", "", err
	}
	probe := filepath.Join(r.root, "probe")
	// #nosec G301 G703 -- readonly container input mount, deliberately readable by its unprivileged user
	if err := os.Mkdir(probe, 0o755); err != nil {
		return "", "", err
	}
	// #nosec G306 G703 -- public tag marker only, on that readonly input mount
	if err := os.WriteFile(filepath.Join(probe, "owned-fixture"), []byte(o.tag+"\n"), 0o644); err != nil {
		return "", "", err
	}
	env := append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	repo, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	output := filepath.Join(probe, "publicinstall")
	args := []string{"go", "-C", repo, "build", "-o", output, "./tools/publicinstall"}
	if _, err := r.command("build-linux-checker", env, args...); err != nil {
		return "", "", err
	}
	return image, probe, nil
}

func (r *runner) linuxContainers(o options) error {
	image, probe, err := r.prepareLinux(o)
	if err != nil {
		return err
	}
	var failures []error
	for i := 0; i < o.fresh; i++ {
		child, err := r.linuxAttempt(o, image, probe, i)
		if err != nil {
			failures = append(failures, err)
		}
		r.report.Attempts += child.Attempts
		r.report.InstallFailed += child.InstallFailed
		r.report.EPIPE += child.EPIPE
		r.report.Recovered += child.Recovered
		r.report.MCPRoundTrips += child.MCPRoundTrips
		if r.ctx.Err() != nil {
			break
		}
	}
	if r.report.Attempts != o.fresh {
		failures = append(failures, errors.New("not all fresh attempts reached the install step"))
	}
	if r.report.InstallFailed != r.report.Recovered {
		failures = append(failures, errors.New("failed install recovery is unproved"))
	}
	if len(failures) == 0 {
		r.report.ArchiveVerified = true
		r.report.GoTag = o.tag
	}
	return errors.Join(failures...)
}

func (r *runner) linuxAttempt(o options, image, probe string, i int) (receipt, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return receipt{}, err
	}
	name := "dibs-public-" + hex.EncodeToString(nonce)
	root := filepath.Join(r.root, fmt.Sprintf("linux-%02d", i+1))
	// The unprivileged container owns only this newly made empty mount.
	if err := os.Mkdir(root, 0o777); err != nil {
		return receipt{}, err
	} // #nosec G301 G703 -- explicit receipt-only mount, no credentials
	if err := os.Chmod(root, 0o777); err != nil {
		return receipt{}, err
	} // #nosec G302 G703 -- same unprivileged receipt mount
	args := []string{
		o.runtime, "run", "--rm", "--name", name, "--mount", "type=bind,source=" + probe + ",target=/probe,readonly",
		"--mount", "type=bind,source=" + root + ",target=/receipts", "--env", "DIBS_PUBLIC_INSTALL_CHILD=1",
		image, "/probe/publicinstall", "-mode", "linux-child", "-tag", o.tag, "-out", "/receipts/run",
	}
	_, runErr := r.command(fmt.Sprintf("linux-%02d", i+1), os.Environ(), args...)
	if r.ctx.Err() != nil {
		return receipt{}, errors.Join(runErr, cleanupFixture(o.runtime, name))
	}
	// #nosec G304 G703 -- exact child fixture's receipt
	b, readErr := os.ReadFile(filepath.Join(root, "run", "receipt.json"))
	var child receipt
	if readErr != nil || json.Unmarshal(b, &child) != nil {
		return receipt{}, errors.Join(runErr, fmt.Errorf("linux attempt %d has no valid receipt", i+1))
	}
	if child.Tag != o.tag || child.Mode != "linux-child" || !strings.HasPrefix(child.Platform, "linux/") {
		return receipt{}, errors.New("child receipt does not identify the requested Linux fixture")
	}
	if !child.Passed {
		return child, errors.Join(runErr, fmt.Errorf("linux attempt %d failed: %s", i+1, child.Error))
	}
	if child.MCPRoundTrips != 6 || !child.ArchiveVerified || child.GoTag != o.tag || child.Attempts != 1 {
		return child, errors.New("passing child omitted required archive, Go or MCP proof")
	}
	return child, runErr
}

func cleanupFixture(runtime, name string) error {
	// A killed client need not kill its container. Remove only this exact
	// random fixture name; never prune or stop any runtime service.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	verb := []string{"rm", "--force", name}
	if runtime == "container" {
		verb = []string{"delete", "--force", name}
	}
	// #nosec G204 G702 -- supported runtime and own random fixture name, argv only
	return exec.CommandContext(ctx, runtime, verb...).Run()
}

func resolvedImage(image, inspection string) (string, error) {
	var items []struct {
		RepoDigests   []string
		Configuration struct{ Descriptor struct{ Digest string } }
	}
	if err := json.Unmarshal([]byte(inspection), &items); err != nil || len(items) != 1 {
		return "", errors.New("image inspection did not identify one image")
	}
	digest := items[0].Configuration.Descriptor.Digest
	reference := strings.Split(image, "@")[0] + "@" + digest
	if len(items[0].RepoDigests) > 0 {
		reference = items[0].RepoDigests[0]
	}
	_, sum, ok := strings.Cut(reference, "@sha256:")
	decoded, err := hex.DecodeString(sum)
	if !ok || err != nil || len(decoded) != 32 {
		return "", errors.New("image inspection has no content digest")
	}
	return reference, nil
}
