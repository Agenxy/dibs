// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

// The whole tree seals ALL inputs, including future dependencies. Explicit
// objects make workflow/tool/build provenance inspectable as well.
var publicationObjects = []string{
	".github/workflows/release.yml", publicationWorkflow,
	".github/workflows/release-proof-check.yml",
	".github/workflows/release-finalize.yml", ".github/workflows/publish-mcp.yml",
	"tools/releaseflow", "tools/signrelease", "tools/archivecheck", "tools/mcpbundle",
	"tools/stampserver", "tools/registrypublish", ".goreleaser.yml", "mise.toml",
	"go.mod", "go.sum", "internal/selfupdate", "internal/release", "internal/build",
	"cmd/dibs", "cmd/dibd",
}

type publicationProof struct {
	Schema      int               `json:"schema"`
	Scope       string            `json:"scope"`
	Repository  string            `json:"repository"`
	RunID       string            `json:"run_id"`
	Attempt     string            `json:"attempt"`
	WorkflowSHA string            `json:"workflow_sha"`
	Version     string            `json:"version"`
	SHA         string            `json:"sha"`
	Tag         string            `json:"tag"`
	EvidenceTag string            `json:"evidence_tag"`
	Identity    string            `json:"identity"`
	ReleaseID   uint64            `json:"release_id"`
	Objects     map[string]string `json:"objects"`
	Assets      map[string]string `json:"assets"`
	Negative    *bool             `json:"negative_control"`
	DraftFound  *bool             `json:"draft_discovered"`
	Uploaded    *bool             `json:"uploaded"`
	Readback    *bool             `json:"draft_readback"`
	Immutable   *bool             `json:"immutable_public"`
	Readonly    *bool             `json:"public_retry_readonly"`
	DryPlans    *bool             `json:"downstream_dry_plans_only"`
}

func evidenceTag(c config, attempt string) string {
	return "rehearsal-proof-v" + c.version + "-" + c.sha + "-" + c.publicationRun + "-" + attempt
}

func bindPublicationRun(c *config, r receipt) error {
	if !positiveID(r.PublicationRun) || c.publicationRun != "" && c.publicationRun != r.PublicationRun {
		return errors.New("full-publication run must be present in and match the authenticated preflight receipt")
	}
	c.publicationRun = r.PublicationRun
	return nil
}

func requirePublication(ctx context.Context, c config, run runner) error {
	// Both old controls remain NON-PUBLISHING: failed preflight writes no
	// receipt; delivery writes rehearsal:true, refused before tag/sign/publish.
	if (c.phase == "validate" || c.phase == "preflight") && (c.rehearsal || c.deliveryRehearsal) {
		return nil
	}
	if !positiveID(c.publicationRun) {
		return errors.New("a successful exact-candidate full-publication rehearsal run is required BEFORE production tagging")
	}
	d, err := rehearsalTarget(c)
	if err != nil {
		return err
	}
	proof, err := authenticatedPublication(ctx, c, d, run)
	if err != nil {
		return err
	}
	objects, err := sourceObjects(ctx, c, run)
	if err != nil {
		return err
	}
	if !maps.Equal(objects, proof.Objects) {
		return errors.New("full-publication proof tested different tree/workflow/tool/build inputs; " +
			"main movement requires a new rehearsal")
	}
	return verifyPublicationPayload(ctx, c, d, proof, run)
}

func verifyPublicationPayload(
	ctx context.Context, c config, d publicationTarget, proof publicationProof, run runner,
) error {
	s, err := publicRelease(ctx, c, d, d.tag)
	if err != nil {
		return err
	}
	if s.ID != proof.ReleaseID || len(s.Assets) != len(proof.Assets) {
		return errors.New("full-publication evidence's exact payload release/asset set is changed")
	}
	dir, err := publicDownload(ctx, c, d, d.tag, s, assets(c.version), 128*1024*1024)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	scratch := c
	scratch.destination, scratch.negativeControl = d, false
	if err = validateTargetAssets(ctx, scratch, dir, run); err != nil {
		return err
	}
	got, err := assetDigests(scratch, dir)
	if err != nil {
		return err
	}
	if !maps.Equal(got, proof.Assets) {
		return errors.New("full-publication public payload digests differ from the signed receipt")
	}
	return nil
}

func sourceObjects(ctx context.Context, c config, run runner) (map[string]string, error) {
	objects := make(map[string]string)
	for _, path := range append([]string{"tree"}, publicationObjects...) {
		ref := c.sha + ":" + path
		if path == "tree" {
			ref = c.sha + "^{tree}"
		}
		out, err := run(ctx, nil, "git", "rev-parse", "--verify", ref)
		if err != nil {
			return nil, fmt.Errorf("full-publication source object %s: %w", path, err)
		}
		oid := strings.TrimSpace(string(out))
		if !shaPattern.MatchString(oid) {
			return nil, fmt.Errorf("full-publication source object %s has invalid OID", path)
		}
		objects[path] = oid
	}
	return objects, nil
}

func authenticatedPublication(
	ctx context.Context, c config, d publicationTarget, run runner,
) (publicationProof, error) {
	var zero publicationProof
	endpoint := "https://api.github.com/repos/" + d.repository + "/actions/runs/" + c.publicationRun
	out, err := publicBytes(ctx, c, endpoint, 1024*1024)
	if err != nil {
		return zero, err
	}
	var meta runMeta
	if err = json.Unmarshal(out, &meta); err != nil {
		return zero, err
	}
	if err = validPublicationRun(meta, c, d); err != nil {
		return zero, err
	}
	out, err = publicBytes(ctx, c, endpoint+"/attempts/"+fmt.Sprint(meta.Attempt)+"/jobs?per_page=100", 1024*1024)
	if err != nil {
		return zero, err
	}
	if err = publicationJob(out); err != nil {
		return zero, err
	}
	tag := evidenceTag(c, fmt.Sprint(meta.Attempt))
	s, err := publicRelease(ctx, c, d, tag)
	if err != nil {
		return zero, err
	}
	if len(s.Assets) != 2 {
		return zero, errors.New("immutable evidence release must have exactly receipt and bundle")
	}
	dir, err := publicDownload(ctx, c, d, tag, s, []string{publicationFile, publicationBundle}, 512*1024)
	if err != nil {
		return zero, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	scratch := c
	scratch.destination = d
	// Signature FIRST, parsing second. Immutability and API permission confer
	// no authorship. No Actions artifact or local receipt fallback exists.
	if err = verifyScratchSignature(ctx, scratch,
		filepath.Join(dir, publicationFile), filepath.Join(dir, publicationBundle), run); err != nil {
		return zero, err
	}
	proof, err := readPublication(filepath.Join(dir, publicationFile))
	if err != nil {
		return zero, err
	}
	if err = validatePublication(proof, c, d, meta); err != nil {
		return zero, err
	}
	return proof, nil
}

func validPublicationRun(meta runMeta, c config, d publicationTarget) error {
	if fmt.Sprint(meta.ID) != c.publicationRun || meta.Attempt == 0 || meta.Event != "workflow_dispatch" ||
		meta.Status != "completed" || meta.Conclusion != "success" || meta.HeadSHA != c.sha ||
		meta.Path != d.workflow || meta.HeadBranch != d.tag || meta.Repository.FullName != d.repository {
		return errors.New("full-publication run is not the bound workflow's successful exact-source unique-tag run")
	}
	return nil
}

func publicationJob(data []byte) error {
	var jobs struct {
		Total int                                         `json:"total_count"`
		Jobs  []struct{ Name, Status, Conclusion string } `json:"jobs"`
	}
	if err := json.Unmarshal(data, &jobs); err != nil {
		return err
	}
	if jobs.Total != len(jobs.Jobs) || jobs.Total > 100 || jobs.Total < 1 {
		return errors.New("full-publication jobs response is incomplete or unbounded")
	}
	count := 0
	for _, job := range jobs.Jobs {
		if job.Name == publicationArtifact {
			if job.Status != "completed" || job.Conclusion != "success" {
				return errors.New("full-publication job is not successful in the latest attempt")
			}
			count++
		}
	}
	if count != 1 {
		return errors.New("exactly one successful full-publication job is required")
	}
	return nil
}

func readPublication(path string) (publicationProof, error) {
	var proof publicationProof
	// #nosec G304,G703 -- fixed name in an owned public download directory.
	f, err := os.Open(path)
	if err != nil {
		return proof, err
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(io.LimitReader(f, 32*1024+1))
	if err != nil || len(body) > 32*1024 {
		return proof, errors.Join(err, errors.New("invalid or oversized full-publication receipt"))
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&proof); err != nil {
		return proof, err
	}
	if err = dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return proof, errors.New("full-publication receipt has trailing data")
	}
	return proof, nil
}

func validatePublication(p publicationProof, c config, d publicationTarget, m runMeta) error {
	if err := validatePublicationBinding(p, c, d, m); err != nil {
		return err
	}
	for _, outcome := range []*bool{p.DraftFound, p.Uploaded, p.Readback, p.Immutable, p.Readonly, p.DryPlans} {
		if outcome == nil || !*outcome {
			return errors.New("full-publication receipt does not positively prove every required publication door")
		}
	}
	if len(p.Objects) != len(publicationObjects)+1 || len(p.Assets) != len(assets(c.version)) {
		return errors.New("full-publication receipt has incomplete source-object or asset set")
	}
	for _, path := range append([]string{"tree"}, publicationObjects...) {
		if !shaPattern.MatchString(p.Objects[path]) {
			return errors.New("full-publication receipt has missing or invalid source provenance")
		}
	}
	for _, name := range assets(c.version) {
		if !checksumPattern.MatchString(p.Assets[name]) {
			return errors.New("full-publication receipt has missing or invalid asset digests")
		}
	}
	return nil
}

func validatePublicationBinding(p publicationProof, c config, d publicationTarget, m runMeta) error {
	if p.Schema != 1 || p.ReleaseID == 0 || p.Negative == nil || *p.Negative {
		return errors.New("invalid full-publication schema/release/negative flag")
	}
	got := map[string]string{
		"scope": p.Scope, "repository": p.Repository, "run": p.RunID, "attempt": p.Attempt,
		"workflow_sha": p.WorkflowSHA, "version": p.Version, "sha": p.SHA, "tag": p.Tag,
		"evidence_tag": p.EvidenceTag, "identity": p.Identity,
	}
	want := map[string]string{
		"scope": publicationArtifact, "repository": d.repository, "run": c.publicationRun,
		"attempt": fmt.Sprint(m.Attempt), "workflow_sha": c.sha, "version": c.version, "sha": c.sha, "tag": d.tag,
		"evidence_tag": evidenceTag(c, fmt.Sprint(m.Attempt)), "identity": d.identity(),
	}
	for field, value := range want {
		if got[field] != value {
			return fmt.Errorf("full-publication signed receipt has wrong %s", field)
		}
	}
	return nil
}

func writePublication(c config, objects map[string]string, id uint64, sums map[string]string) error {
	if os.Getenv("RUNNER_TEMP") == "" {
		return errors.New("runner-owned full-publication receipt path is required")
	}
	c.publicationRun = c.runID
	d, yes, no := targetOf(c), true, false
	p := publicationProof{
		Schema: 1, Scope: publicationArtifact, Repository: d.repository,
		RunID: c.runID, Attempt: c.attempt, WorkflowSHA: c.sha, Version: c.version, SHA: c.sha,
		Tag: d.tag, EvidenceTag: evidenceTag(c, c.attempt), Identity: d.identity(), ReleaseID: id,
		Objects: objects, Assets: sums,
		Negative: &no, DraftFound: &yes, Uploaded: &yes, Readback: &yes, Immutable: &yes, Readonly: &yes, DryPlans: &yes,
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	// #nosec G703 -- fixed filename under trusted runner directory, no dispatch path.
	return os.WriteFile(filepath.Join(os.Getenv("RUNNER_TEMP"), publicationFile), data, 0o600)
}
