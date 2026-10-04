package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func publicResponse(ctx context.Context, c config, url string) (*http.Response, error) {
	client := c.publicClient // Only a private test seam; no URL/client option in main.
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// The transport is the seam, not the security policy. Even fixture clients
	// enter the production redirect/token boundary; do not mutate a shared client.
	clientCopy := *client
	clientCopy.CheckRedirect = publicProofRedirect
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "dibs-releaseflow")
	if req.URL.Scheme == "https" && req.URL.Host == "api.github.com" {
		token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
		if token == "" {
			return nil, errors.New("GitHub API proof reads require the existing job GITHUB_TOKEN; " +
				"no anonymous or local-file fallback")
		}
		// Rate-limit authentication ONLY: neither this token nor HTTP 200 is
		// proof authority. Asset/CDN requests never carry it. No new credential.
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := clientCopy.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("public proof GET %s returned HTTP %d; "+
			"verify the job token's public-read access; no anonymous or local-file fallback", url, resp.StatusCode)
	}
	return resp, nil
}

func publicProofRedirect(req *http.Request, via []*http.Request) error {
	host := req.URL.Hostname()
	if req.URL.Scheme != "https" || req.URL.User != nil || len(via) >= 5 ||
		(host != "github.com" && host != "api.github.com" && !strings.HasSuffix(host, ".githubusercontent.com")) {
		return errors.New("public proof redirect is not bounded HTTPS")
	}
	if req.URL.Host != "api.github.com" {
		req.Header.Del("Authorization")
	}
	return nil
}

func publicBytes(ctx context.Context, c config, url string, limit int64) ([]byte, error) {
	resp, err := publicResponse(ctx, c, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("public proof response exceeds bound")
	}
	return data, nil
}

func publicRelease(ctx context.Context, c config, d publicationTarget, tag string) (releaseStatus, error) {
	var s releaseStatus
	data, err := publicBytes(ctx, c, "https://api.github.com/repos/"+d.repository+"/releases/tags/"+tag, 1024*1024)
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(data, &s); err != nil {
		return s, err
	}
	if s.ID == 0 || s.Tag != tag || s.Draft || !s.Immutable {
		return s, errors.New("public proof release is not the exact positive-ID immutable public tag")
	}
	return s, nil
}

func publicDownload(ctx context.Context, c config, d publicationTarget, tag string,
	s releaseStatus, names []string, limit int64,
) (string, error) {
	seen := make(map[string]bool)
	for _, a := range s.Assets {
		if seen[a.Name] {
			return "", errors.New("public proof release has duplicate assets")
		}
		seen[a.Name] = true
	}
	for _, name := range names {
		if !seen[name] {
			return "", fmt.Errorf("public proof release lacks %s", name)
		}
	}
	dir, err := os.MkdirTemp("", "dibs-public-proof-")
	if err != nil {
		return "", err
	}
	for _, name := range names {
		if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
			_ = os.RemoveAll(dir)
			return "", errors.New("public proof asset is not a fixed basename")
		}
		url := "https://github.com/" + d.repository + "/releases/download/" + tag + "/" + name
		if err = downloadPublicFile(ctx, c, url, filepath.Join(dir, name), limit); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

func downloadPublicFile(ctx context.Context, c config, url, path string, limit int64) error {
	resp, err := publicResponse(ctx, c, url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// #nosec G304,G703 -- fixed basename in this call's new private directory.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	closeErr := f.Close()
	if n > limit {
		return errors.Join(copyErr, closeErr, errors.New("public proof asset exceeds bound"))
	}
	return errors.Join(copyErr, closeErr)
}
