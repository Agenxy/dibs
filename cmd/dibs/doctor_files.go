package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Ask the live daemon, not saved flags or a guessed public hostname. The
// private request is board-authenticated; the public probe sends NO credential.
func checkPublicTLS(client *http.Client, secret string, ok func(string), warn func(string, string)) {
	req, err := http.NewRequest(http.MethodGet, origin()+"/api/transfer-status", nil)
	if err != nil {
		return
	}
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return
	} // older daemon or unavailable local status
	var status struct {
		PublicOrigin string `json:"public_origin"`
	}
	if json.NewDecoder(resp.Body).Decode(&status) != nil || status.PublicOrigin == "" {
		return
	}
	probePublicTLS(client, status.PublicOrigin, ok, warn)
}

func probePublicTLS(client *http.Client, publicOrigin string, ok func(string), warn func(string, string)) {
	u, err := url.Parse(publicOrigin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		warn("public transfer origin is not an HTTPS origin",
			"configure public-url as an HTTPS origin without credentials or path")
		return
	}
	req, err := http.NewRequest(http.MethodHead, strings.TrimSuffix(publicOrigin, "/")+"/mcp", nil)
	if err != nil {
		return
	}
	// Keep the existing certificate trust, but do not follow an external redirect.
	probe := *client
	probe.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := probe.Do(req)
	if err != nil {
		warn("public TLS edge could not be measured",
			"check public DNS, listener, certificate trust and proxy reachability; no credential was sent")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.TLS == nil || resp.TLS.Version < tls.VersionTLS13 {
		version := "no TLS"
		if resp.TLS != nil {
			version = tls.VersionName(resp.TLS.Version)
		}
		warn("public edge negotiated "+version+", below TLS 1.3",
			"set the public TLS proxy's minimum version to TLS 1.3; Dibs does not change proxy settings")
		return
	}
	ok(fmt.Sprintf("public edge negotiated %s (no credential sent)", tls.VersionName(resp.TLS.Version)))
}
