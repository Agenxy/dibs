// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	xport "github.com/agenxy/dibs/internal/transport"
)

type fileDescriptor struct{ Transport, Method, URL string }

type fileAuthorization struct{ Upload, Download fileDescriptor }

type fileResult struct {
	Blob string `json:"blob"`
	Size int64  `json:"size"`
	Mime string `json:"mime,omitempty"`
	Path string `json:"path,omitempty"`
}

// filesCmd is intentionally narrow: put stores one file, get reads one blob.
// Secrets come from environment/config, never argv; stdout never carries tickets.
func filesCmd(verb string, args []string) error {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, jsonHelp)
	mime, output := "application/octet-stream", ""
	synopsis := "usage: dibs put [--mime <type>] <file>"
	if verb == "put" {
		fs.StringVar(&mime, "mime", mime, "content type")
	} else {
		synopsis = "usage: dibs get <blob> [-o <path>]"
		fs.StringVar(&output, "o", "", "destination file (must not already exist)")
		fs.StringVar(&output, "out", "", "destination file (must not already exist)")
	}
	// Position-first is the documented spelling; flag-first also works.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(append([]string(nil), args[1:]...), args[0])
	}
	if err := parseFlagsUsage(fs, args, synopsis); err != nil {
		return err
	}
	var result fileResult
	var err error
	switch {
	case fs.NArg() != 1:
		err = fmt.Errorf("usage: dibs %s <file-or-blob> [--json]", verb)
	case os.Getenv("DIBS_TOKEN") == "":
		err = errors.New("set DIBS_TOKEN to the token returned by register; for public boards also set DIBS_INVITE")
	case verb == "put":
		result, err = putFile(fs.Arg(0), mime)
	default:
		result, err = getFile(fs.Arg(0), output)
	}
	if err != nil {
		if *asJSON {
			var domain *core.Error
			if !errors.As(err, &domain) {
				domain = &core.Error{
					Code: "E_FILE_TRANSFER", Msg: err.Error(),
					Hint: "check the source/destination and board access, then retry",
				}
			}
			if printErr := printJSON(domain); printErr != nil {
				return printErr
			}
			return reportedError{err}
		}
		return err
	}
	if *asJSON {
		return printJSON(result)
	}
	if verb == "put" {
		fmt.Println(result.Blob)
	} else {
		fmt.Println(result.Path)
	}
	return nil
}

func fileCall(tool string, args map[string]any, out any) error {
	args["token"] = os.Getenv("DIBS_TOKEN")
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, mcpEndpoint(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if invite := os.Getenv("DIBS_INVITE"); invite != "" {
		req.Header.Set("Authorization", "Bearer "+invite)
	} else {
		secret, err := localSecret()
		if err != nil {
			return err
		}
		req.Header.Set("X-Dibs-Local", secret)
	}
	client := daemonClient(30 * time.Second)
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("MCP connection failed; check board address, certificate trust and credentials")
	}
	defer func() { _ = resp.Body.Close() }()
	var envelope struct {
		Error  *struct{ Message string } `json:"error"`
		Result struct {
			IsError bool
			Content []struct{ Text string }
		}
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return errors.New("invalid MCP response; retry authorization")
	}
	if resp.StatusCode != 200 || envelope.Error != nil || len(envelope.Result.Content) != 1 {
		return errors.New("file authorization refused; use the board's HTTP(S) MCP endpoint with current credentials")
	}
	text := []byte(envelope.Result.Content[0].Text)
	if envelope.Result.IsError {
		var domain core.Error
		if err = json.Unmarshal(text, &domain); err != nil {
			return err
		}
		return &domain
	}
	return json.Unmarshal(text, out)
}

func authorizeFile(tool string, args map[string]any) (fileDescriptor, error) {
	var authorization fileAuthorization
	if err := fileCall(tool, args, &authorization); err != nil {
		return fileDescriptor{}, err
	}
	descriptor := authorization.Upload
	if tool == "download" {
		descriptor = authorization.Download
	}
	if err := validateFileURL(descriptor.URL); err != nil {
		return fileDescriptor{}, err
	}
	return descriptor, nil
}

func validateFileURL(target string) error {
	u, err := url.Parse(target)
	board, berr := url.Parse(mcpEndpoint())
	if err != nil || berr != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.Host != board.Host || u.Scheme != board.Scheme ||
		!strings.HasPrefix(u.Path, "/files/") || (u.Scheme != "https" && (u.Scheme != "http" || !xport.IsLoopback(u.Host))) {
		return errors.New("refusing file descriptor outside the authenticated board origin or TLS boundary")
	}
	return nil
}

func byteClient() *http.Client {
	rt := http.DefaultTransport.(*http.Transport).Clone()
	rt.TLSClientConfig = &tls.Config{RootCAs: trustedPool(), MinVersion: tls.VersionTLS13}
	return &http.Client{
		Transport: rt, Timeout: 2 * time.Minute,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// requestBytes redacts transport errors: net/http embeds the full request URL
// (including its bearer capability) in the otherwise ordinary error string.
func requestBytes(client *http.Client, req *http.Request) (*http.Response, error) {
	if err := validateFileURL(req.URL.String()); err != nil {
		return nil, err
	}
	resp, err := client.Do(req) //nolint:gosec // G704: same authenticated origin and TLS checked above; redirects refused
	if err != nil {
		return nil, errors.New("byte connection failed; no ticket was printed; retry from the accepted offset")
	}
	return resp, nil
}

func putFile(path, mime string) (fileResult, error) {
	f, err := os.Open(path) //nolint:gosec // G304: explicit user-supplied source, never a board-named path
	if err != nil {
		return fileResult{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fileResult{}, err
	}
	if !info.Mode().IsRegular() {
		return fileResult{}, errors.New("put requires a regular file")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return fileResult{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	args := map[string]any{"size": info.Size(), "sha256": digest, "mime": mime, "name": filepath.Base(path)}
	descriptor, err := authorizeFile("upload", args)
	if err != nil {
		return fileResult{}, err
	}
	return uploadFile(f, info.Size(), digest, descriptor, args)
}

func getFile(blob, output string) (fileResult, error) {
	if !core.ValidBlobID(blob) {
		return fileResult{}, core.ErrBadID
	}
	if output == "" {
		output = strings.TrimPrefix(blob, "sha256:")
	}
	abs, err := filepath.Abs(output)
	if err != nil {
		return fileResult{}, err
	}
	if _, err = os.Lstat(abs); !os.IsNotExist(err) {
		return fileResult{}, errors.New("destination exists or cannot be checked; choose a new -o path")
	}
	f, err := os.CreateTemp(filepath.Dir(abs), ".dibs-download-*")
	if err != nil {
		return fileResult{}, err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	descriptor, err := authorizeFile("download", map[string]any{"blob": blob})
	if err != nil {
		return fileResult{}, err
	}
	size, err := downloadFile(f, blob, descriptor)
	if err != nil {
		return fileResult{}, err
	}
	if err = f.Sync(); err != nil {
		return fileResult{}, err
	}
	if err = f.Close(); err != nil {
		return fileResult{}, err
	}
	// Link refuses an existing destination even if another process created it
	// after our check. Never overwrite a user's file to publish a download.
	if err = os.Link(f.Name(), abs); err != nil {
		return fileResult{}, err
	}
	return fileResult{Blob: blob, Size: size, Path: abs}, nil
}
