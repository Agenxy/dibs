// releaseflow is the typed release boundary. Inputs arrive through environment,
// never through interpolated shell. Preflight has no remote write operation;
// commit-tag is a different job behind its success.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/agenxy/dibs/internal/release"
)

const (
	repository   = "Agenxy/dibs"
	workflowPath = ".github/workflows/release.yml"
	artifactName = "release-preflight"
	phases       = "validate, preflight, commit-tag, authorize-receipt, authorize, finalize, " +
		"delivery-rehearsal, full-publication-validate, full-publication, publish or cask"
)

var (
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	shaPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type config struct {
	version, sha, phase, receiptPath, runID, attempt, workflowSHA string
	target, publicationRun                                        string
	negativeControl                                               bool
	destination                                                   publicationTarget
	publicationAudit                                              *publicationAudit
	publicClient                                                  *http.Client
	rehearsal                                                     bool
	deliveryRehearsal                                             bool
}

type receipt struct {
	Schema         int    `json:"schema"`
	Repository     string `json:"repository"`
	RunID          string `json:"run_id"`
	Attempt        string `json:"attempt"`
	WorkflowSHA    string `json:"workflow_sha"`
	Version        string `json:"version"`
	SHA            string `json:"sha"`
	Tree           string `json:"tree"`
	TagOID         string `json:"tag_oid"`
	Rehearsal      *bool  `json:"rehearsal"`
	PublicationRun string `json:"full_publication_run,omitempty"`
}

// command is a test seam at the subprocess boundary, not a second release path.
// Tests drive execute(), the same entry point main invokes, including Git setup.
type runner func(context.Context, []string, string, ...string) ([]byte, error)

func command(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	// #nosec G204,G702 -- call sites use fixed executables, typed argv and validated version/SHA.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	verify := name == "cosign" && len(args) > 0 && args[0] == "verify-blob"
	if name != "git" && name != "gh" && !verify {
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return nil, nil
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return out, nil
}

func main() {
	phase := flag.String("phase", "", phases)
	flag.Parse()
	c := config{
		phase: *phase, version: os.Getenv("DIBS_RELEASE_VERSION"), sha: os.Getenv("DIBS_RELEASE_SHA"),
		receiptPath: os.Getenv("DIBS_RELEASE_RECEIPT"), runID: os.Getenv("DIBS_PREFLIGHT_RUN"),
		attempt: os.Getenv("GITHUB_RUN_ATTEMPT"), workflowSHA: os.Getenv("GITHUB_SHA"),
		rehearsal: os.Getenv("DIBS_RELEASE_REHEARSAL") == "true",
		target:    os.Getenv("DIBS_RELEASE_TARGET"), publicationRun: os.Getenv("DIBS_FULL_PUBLICATION_RUN"),
		negativeControl: os.Getenv("DIBS_RELEASE_DISCOVERY_NEGATIVE_CONTROL") == "true",
	}
	if c.runID == "" {
		c.runID = os.Getenv("GITHUB_RUN_ID")
	}
	c.deliveryRehearsal = os.Getenv("DIBS_RELEASE_DELIVERY_REHEARSAL") == "true"
	if c.receiptPath == "" && os.Getenv("RUNNER_TEMP") != "" {
		c.receiptPath = filepath.Join(os.Getenv("RUNNER_TEMP"), "dibs-preflight.json")
	}
	if err := execute(context.Background(), c, command); err != nil {
		fmt.Fprintln(os.Stderr, "releaseflow:", err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, c config, run runner) error {
	destination, err := resolveTarget(c)
	if err != nil {
		return err
	}
	c.destination = destination
	if c.phase == "finalize" {
		return finalize(ctx, c, run)
	}
	if !versionPattern.MatchString(c.version) || !shaPattern.MatchString(c.sha) {
		return errors.New("release requires a canonical MAJOR.MINOR.PATCH version and full lowercase SHA")
	}
	switch c.phase {
	case "preflight":
		return preflight(ctx, c, run)
	case "commit-tag":
		return commitTag(ctx, c, run)
	case "validate":
		return validateCandidate(ctx, c, run)
	case "full-publication-validate", "full-publication":
		return fullPublication(ctx, c, run)
	case "authorize", "authorize-receipt":
		return authorize(ctx, c, run)
	case "delivery-rehearsal":
		return receiveRehearsal(ctx, c, run)
	case "publish", "cask":
		gate := c
		gate.phase = "authorize"
		if err := execute(ctx, gate, run); err != nil {
			return err
		}
		if c.phase == "cask" {
			return publishCask(ctx, c, run)
		}
		return publish(ctx, c, run)
	default:
		return errors.New("choose -phase " + phases)
	}
}

func validateCandidate(ctx context.Context, c config, run runner) error {
	if err := mainContext(); err != nil {
		return err
	}
	if err := candidate(ctx, c, run); err != nil {
		return err
	}
	return requirePublication(ctx, c, run)
}

func finalize(ctx context.Context, c config, run runner) error {
	r, err := authenticatedReceipt(ctx, c.runID, run)
	if errors.Is(err, errNotPreflight) {
		fmt.Println("not a preflight dispatch; no handoff")
		return nil
	}
	if err != nil {
		return err
	}
	ref, mode := "v"+r.Version, "publish-only"
	if *r.Rehearsal {
		ref, mode = "main", "delivery-rehearsal"
	}
	args := []string{
		"workflow", "run", "release.yml", "--repo", repository,
		"--ref", ref, "-f", "mode=" + mode, "-f", "version=" + r.Version,
		"-f", "sha=" + r.SHA, "-f", "preflight_run=" + r.RunID,
	}
	if !*r.Rehearsal {
		if !positiveID(r.PublicationRun) {
			return errors.New("authenticated preflight lacks a full-publication run; refuse publication dispatch")
		}
		args = append(args, "-f", "full_publication_run="+r.PublicationRun)
	}
	_, err = run(ctx, nil, "gh", args...)
	return err
}

func commitTag(ctx context.Context, c config, run runner) error {
	if err := mainContext(); err != nil {
		return err
	}
	r, err := readReceipt(c.receiptPath)
	if err != nil {
		return err
	}
	if r.RunID != c.runID || r.Attempt != c.attempt || r.WorkflowSHA != c.workflowSHA ||
		r.Version != c.version || r.SHA != c.sha {
		return errors.New("current-job preflight receipt does not match this exact run/candidate")
	}
	if *r.Rehearsal {
		return errors.New("rehearsal receipt can NEVER create a tag")
	}
	if err = bindPublicationRun(&c, r); err != nil {
		return err
	}
	if err = candidate(ctx, c, run); err != nil {
		return err
	}
	if err = requirePublication(ctx, c, run); err != nil {
		return err
	}
	if err = localTag(ctx, c, run); err != nil {
		return err
	}
	got, err := run(ctx, nil, "git", "rev-parse", "refs/tags/v"+c.version)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(got)) != r.TagOID {
		return errors.New("annotated tag object differs from the preflight's exact tag provenance")
	}
	_, err = run(ctx, nil, "git", "push", "origin", "refs/tags/v"+c.version+":refs/tags/v"+c.version)
	return err
}

func authorize(ctx context.Context, c config, run runner) error {
	if os.Getenv("GITHUB_EVENT_NAME") != "workflow_dispatch" || os.Getenv("GITHUB_REF") != "refs/tags/v"+c.version {
		return errors.New("publication must be dispatched at the exact tag, preserving its signing identity")
	}
	r, err := authenticatedReceipt(ctx, c.runID, run)
	if err != nil {
		return err
	}
	if r.Version != c.version || r.SHA != c.sha {
		return errors.New("successful preflight is for a different version/SHA")
	}
	if *r.Rehearsal {
		return errors.New("rehearsal receipt can NEVER sign or publish")
	}
	if err = bindPublicationRun(&c, r); err != nil {
		return err
	}
	if err = requirePublication(ctx, c, run); err != nil {
		return err
	}
	if c.phase == "authorize" {
		if err = checkoutMatches(ctx, c.sha, r.Tree, run); err != nil {
			return err
		}
	}
	if err = remoteTag(ctx, c, true, run); err != nil {
		return err
	}
	got, err := run(ctx, nil, "git", "ls-remote", "--tags", "origin", "refs/tags/v"+c.version)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(got))
	if len(fields) != 2 || fields[0] != r.TagOID {
		return errors.New("remote annotated tag object differs from preflight")
	}
	return nil
}

func receiveRehearsal(ctx context.Context, c config, run runner) error {
	if err := mainContext(); err != nil {
		return err
	}
	r, err := authenticatedReceipt(ctx, c.runID, run)
	if err != nil {
		return err
	}
	if !*r.Rehearsal || r.Version != c.version || r.SHA != c.sha {
		return errors.New("receiver requires matching authenticated rehearsal receipt")
	}
	fmt.Printf("Authenticated rehearsal receipt %s delivered through workflow_dispatch; "+
		"no tag, release signing or publication.\n", r.RunID)
	return nil
}

func mainContext() error {
	if os.Getenv("GITHUB_EVENT_NAME") != "workflow_dispatch" || os.Getenv("GITHUB_REF") != "refs/heads/main" ||
		os.Getenv("GITHUB_REPOSITORY") != repository {
		return errors.New("preflight/tag creation must be dispatched from protected main in Agenxy/dibs")
	}
	return nil
}

func preflight(ctx context.Context, c config, run runner) error {
	if err := preparePreflight(ctx, c, run); err != nil {
		return err
	}
	return buildPreflight(ctx, c, run)
}

func preparePreflight(ctx context.Context, c config, run runner) error {
	if err := mainContext(); err != nil {
		return err
	}
	if !positiveID(c.runID) || !positiveID(c.attempt) || !shaPattern.MatchString(c.workflowSHA) {
		return errors.New("preflight requires its Actions run, attempt and workflow SHA")
	}
	if err := candidate(ctx, c, run); err != nil {
		return err
	}
	if err := requirePublication(ctx, c, run); err != nil {
		return err
	}
	if err := localTag(ctx, c, run); err != nil {
		return err
	}
	return nil
}

func buildPreflight(ctx context.Context, c config, run runner) error {
	if c.rehearsal {
		return errors.New("deliberate preflight gate failure: no receipt, remote tag or publication")
	}
	// Keep the ordinary full gate. Only the EXTRA snapshot pass uses the exact
	// production version; task ci's normal archive probe keeps its own snapshot.
	if _, err := run(ctx, nil, "mise", "exec", "--", "task", "ci"); err != nil {
		return err
	}
	if _, err := run(ctx, nil, "go", "run", "./tools/sigstore-root-check"); err != nil {
		return err
	}
	env := []string{"DIBS_SNAPSHOT_RELEASE_VERSION=" + c.version, "HOMEBREW_TAP_DEPLOY_KEY=unused-offline-generation"}
	if _, err := run(ctx, env, "goreleaser", "release", "--snapshot", "--clean",
		"--skip=sign,publish,announce,validate"); err != nil {
		return err
	}
	if _, err := run(ctx, nil, "go", "run", "./tools/archivecheck"); err != nil {
		return err
	}
	if _, err := run(ctx, nil, "go", "run", "./tools/mcpbundle", "-version", c.version); err != nil {
		return err
	}
	if err := stageUnsigned(c); err != nil {
		return err
	}
	if err := candidate(ctx, c, run); err != nil {
		return err
	}
	return writeReceipt(ctx, c, run)
}

func writeReceipt(ctx context.Context, c config, run runner) error {
	tree, err := run(ctx, nil, "git", "rev-parse", "HEAD^{tree}")
	if err != nil {
		return err
	}
	tag, err := run(ctx, nil, "git", "rev-parse", "refs/tags/v"+c.version)
	if err != nil {
		return err
	}
	rehearsal := c.deliveryRehearsal
	r := receipt{
		Schema: 1, Repository: repository, RunID: c.runID, Attempt: c.attempt, WorkflowSHA: c.workflowSHA,
		Version: c.version, SHA: c.sha, Tree: strings.TrimSpace(string(tree)),
		TagOID: strings.TrimSpace(string(tag)), Rehearsal: &rehearsal,
		PublicationRun: c.publicationRun,
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if c.receiptPath == "" {
		return errors.New("DIBS_RELEASE_RECEIPT is required; no proof was written")
	}
	// #nosec G703 -- path supplied by the trusted runner, never dispatch input.
	return os.WriteFile(c.receiptPath, data, 0o600)
}

func candidate(ctx context.Context, c config, run runner) error {
	if _, err := run(ctx, nil, "git", "fetch", "origin", "main"); err != nil {
		return err
	}
	head, err := run(ctx, nil, "git", "rev-parse", "HEAD", "origin/main")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != c.sha+"\n"+c.sha {
		return errors.New("candidate must be the exact checked-out protected-main tip")
	}
	status, err := run(ctx, nil, "git", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(status)) != "" {
		return errors.New("candidate contains tracked changes")
	}
	root, err := run(ctx, nil, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	rootPath := strings.TrimSpace(string(root))
	v, err := release.Current(rootPath)
	if err != nil {
		return err
	}
	if v != c.version {
		return errors.New("candidate changelog version differs from dispatch")
	}
	for _, path := range release.Manifests {
		// #nosec G304 -- Git's repository root and frozen release manifest list.
		data, err := os.ReadFile(filepath.Join(rootPath, path))
		if err != nil {
			return err
		}
		var manifest struct {
			Version string `json:"version"`
		}
		if err = json.Unmarshal(data, &manifest); err != nil {
			return err
		}
		if manifest.Version != c.version {
			return fmt.Errorf("%s is not stamped at %s", path, c.version)
		}
	}
	return remoteTag(ctx, c, false, run)
}

func remoteTag(ctx context.Context, c config, required bool, run runner) error {
	ref := "refs/tags/" + targetOf(c).tag
	out, err := run(ctx, nil, "git", "ls-remote", "--tags", "origin", ref, ref+"^{}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) == "" {
		if required {
			return errors.New("exact immutable release tag is absent")
		}
		return nil
	}
	// Prefer the peeled commit of an annotated tag; a lightweight tag's one
	// line is checked too. Never mistake the tag-object SHA for the commit.
	commit := ""
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return errors.New("malformed remote tag response")
		}
		if fields[1] == ref && commit == "" || fields[1] == ref+"^{}" {
			commit = fields[0]
		}
	}
	if commit != c.sha {
		return errors.New("immutable remote tag already points at a different SHA; never move it")
	}
	return nil
}

func localTag(ctx context.Context, c config, run runner) error {
	ref := "refs/tags/v" + c.version
	out, err := run(ctx, nil, "git", "tag", "--list", "v"+c.version)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != "" {
		kind, err := run(ctx, nil, "git", "cat-file", "-t", ref)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(kind)) != "tag" {
			return errors.New("release provenance requires an annotated tag")
		}
		got, err := run(ctx, nil, "git", "rev-parse", ref+"^{commit}")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(got)) != c.sha {
			return errors.New("existing local release tag is for a different SHA")
		}
		return nil
	}
	date, err := run(ctx, nil, "git", "show", "-s", "--format=%cI", c.sha)
	if err != nil {
		return err
	}
	// Both jobs produce the SAME annotated object, not just the same label and
	// commit. The candidate commit date fixes otherwise ambient tagger time.
	env := []string{"GIT_COMMITTER_DATE=" + strings.TrimSpace(string(date))}
	if _, err = run(ctx, env, "git", "-c", "tag.gpgsign=false", "-c", "user.name=Dibs release",
		"-c", "user.email=release@users.noreply.github.com",
		"tag", "-a", "v"+c.version, c.sha, "-m", "v"+c.version); err != nil {
		return err
	}
	return nil
}

func checkoutMatches(ctx context.Context, sha, tree string, run runner) error {
	out, err := run(ctx, nil, "git", "rev-parse", "HEAD", "HEAD^{tree}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != sha+"\n"+tree {
		return errors.New("publisher checkout is not the preflight's exact candidate/tree")
	}
	return nil
}

func positiveID(s string) bool {
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == s
}

func readReceipt(path string) (receipt, error) {
	var r receipt
	// #nosec G304 -- runner-owned path or a file in its private artifact stage.
	f, err := os.Open(path)
	if err != nil {
		return r, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil {
		return r, err
	}
	if len(data) > 8192 {
		return r, errors.New("receipt exceeds 8 KiB bound")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&r); err != nil {
		return r, err
	}
	if err = dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return r, errors.New("receipt has trailing or oversized data")
	}
	return r, validateReceipt(r)
}

func validateReceipt(r receipt) error {
	if r.Rehearsal == nil || r.Schema != 1 || r.Repository != repository ||
		!positiveID(r.RunID) || !positiveID(r.Attempt) || !versionPattern.MatchString(r.Version) {
		return errors.New("invalid release preflight receipt")
	}
	for _, sha := range []string{r.WorkflowSHA, r.SHA, r.Tree, r.TagOID} {
		if !shaPattern.MatchString(sha) {
			return errors.New("invalid release preflight receipt SHA")
		}
	}
	return nil
}
