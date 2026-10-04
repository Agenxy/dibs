package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var errNotPreflight = errors.New("completed dispatch has no preflight job")

type runMeta struct {
	ID                              uint64 `json:"id"`
	Attempt                         uint64 `json:"run_attempt"`
	Event, Status, Conclusion, Path string
	HeadBranch                      string `json:"head_branch"`
	HeadSHA                         string `json:"head_sha"`
	Repository                      struct {
		FullName string `json:"full_name"`
	}
}

// authenticatedReceipt fetches proof FROM Actions, not a caller's path or
// display title. Success at another workflow/ref/run/candidate is not proof.
func authenticatedReceipt(ctx context.Context, id string, run runner) (receipt, error) {
	var zero receipt
	if !positiveID(id) {
		return zero, errors.New("a successful preflight run ID is required")
	}
	endpoint := "repos/" + repository + "/actions/runs/" + id
	out, err := run(ctx, nil, "gh", "api", endpoint)
	if err != nil {
		return zero, err
	}
	var meta runMeta
	if err = json.Unmarshal(out, &meta); err != nil {
		return zero, err
	}
	if err = meta.validate(id); err != nil {
		return zero, err
	}
	// Only the latest attempt's successful jobs count. A failed rerun must not
	// inherit a successful preflight job from an earlier attempt.
	out, err = run(ctx, nil, "gh", "api", endpoint+"/attempts/"+fmt.Sprint(meta.Attempt)+"/jobs?per_page=100")
	if err != nil {
		return zero, err
	}
	if err = successfulPreflight(out); err != nil {
		return zero, err
	}
	out, err = run(ctx, nil, "gh", "api", endpoint+"/artifacts?per_page=100")
	if err != nil {
		return zero, err
	}
	name := artifactName + "-" + fmt.Sprint(meta.Attempt)
	if err = checkArtifact(out, name); err != nil {
		return zero, err
	}
	return downloadReceipt(ctx, id, name, meta, run)
}

func (m runMeta) validate(id string) error {
	if fmt.Sprint(m.ID) != id || m.Event != "workflow_dispatch" || m.HeadBranch != "main" ||
		m.Path != workflowPath || m.Status != "completed" || m.Conclusion != "success" ||
		m.Repository.FullName != repository || !shaPattern.MatchString(m.HeadSHA) || m.Attempt == 0 {
		return errors.New("receipt source is not this workflow's successful protected-main dispatch")
	}
	return nil
}

func successfulPreflight(out []byte) error {
	var jobs struct {
		Total int                                         `json:"total_count"`
		Jobs  []struct{ Name, Status, Conclusion string } `json:"jobs"`
	}
	if err := json.Unmarshal(out, &jobs); err != nil {
		return err
	}
	if jobs.Total > len(jobs.Jobs) {
		return errors.New("preflight jobs response is incomplete")
	}
	count := 0
	for _, j := range jobs.Jobs {
		if j.Name != "preflight" {
			continue
		}
		if j.Status != "completed" || j.Conclusion != "success" {
			return errors.New("successful run has no successful preflight job")
		}
		count++
	}
	if count == 0 {
		return errNotPreflight
	}
	if count != 1 {
		return errors.New("ambiguous preflight job")
	}
	return nil
}

func checkArtifact(out []byte, name string) error {
	var artifacts struct {
		Total     int `json:"total_count"`
		Artifacts []struct {
			ID      uint64
			Name    string
			Expired bool
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(out, &artifacts); err != nil {
		return err
	}
	if artifacts.Total > len(artifacts.Artifacts) {
		return errors.New("preflight artifacts response is incomplete")
	}
	count := 0
	for _, a := range artifacts.Artifacts {
		if a.Name == name && !a.Expired && a.ID > 0 {
			count++
		}
	}
	if count != 1 {
		return errors.New("one immutable non-expired preflight artifact is required")
	}
	return nil
}

func downloadReceipt(ctx context.Context, id, name string, meta runMeta, run runner) (receipt, error) {
	var zero receipt
	dir, err := os.MkdirTemp("", "dibs-preflight-proof-")
	if err != nil {
		return zero, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if _, err = run(ctx, nil, "gh", "run", "download", id, "--repo", repository,
		"--name", name, "--dir", dir); err != nil {
		return zero, err
	}
	r, err := readReceipt(filepath.Join(dir, "dibs-preflight.json"))
	if err != nil {
		return zero, err
	}
	if r.RunID != id || r.Attempt != fmt.Sprint(meta.Attempt) || r.WorkflowSHA != meta.HeadSHA {
		return zero, errors.New("artifact is not bound to the authenticated run/attempt/workflow SHA")
	}
	return r, nil
}
