// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/invites"
	"github.com/agenxy/dibs/internal/localtransport"
)

// Ask the live daemon, not saved flags or a guessed public hostname. The
// private request is board-authenticated; the public probe sends NO credential.
func checkPublicTLS(client *http.Client, secret string, ok func(string), warn func(string, string)) {
	if checkGuestTLS(client, secret, ok, warn) {
		return
	}
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
		Mode         string `json:"mode"`
		Addr         string `json:"addr"`
	}
	if json.NewDecoder(resp.Body).Decode(&status) != nil {
		return
	}
	if status.Mode == "disabled" {
		ok("public invitation listener is disabled")
		return
	}
	if status.Mode == "loopback-forward" {
		ok("loopback-forward invitation listener bound at " + status.Addr + "; far-side origin " +
			status.PublicOrigin + "; operator forward reachability is unmeasured")
		warn("invitation token can be captured at a squatted plaintext forward port", invitationForwardHint)
		return // the advertised loopback is on the far side, not this machine
	}
	if status.PublicOrigin == "" {
		return
	}
	if status.Mode != "" {
		ok("public invitation listener " + status.Mode + " bound at " + status.Addr)
	}
	// This measurement targets the public TLS endpoint and carries no local,
	// invitation or admin credential. The private client must stay on IPC.
	probeClient := &http.Client{
		Timeout:       5 * time.Second,
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trustedPool(), MinVersion: tls.VersionTLS13}},
		CheckRedirect: localtransport.RefuseRedirect,
	}
	defer probeClient.CloseIdleConnections()
	probePublicTLS(probeClient, status.PublicOrigin, ok, warn)
}

func checkGuestTLS(client *http.Client, secret string, ok func(string), warn func(string, string)) bool {
	req, err := http.NewRequest(http.MethodGet, origin()+"/api/guest-status", nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var info invites.EndpointInfo
	if json.NewDecoder(io.LimitReader(resp.Body, 32<<10)).Decode(&info) != nil || info.Mode != "direct-ip" {
		return false
	}
	if info.Reason != "" || info.URL == "" {
		warn("public guest endpoint withdrawn: "+info.Reason,
			"fix the selected assigned address and restart; a changed scope requires new guest CA trust and invitations")
		return true
	}
	warn("guest address stability is "+info.Stability+"; inbound WAN reachability is unmeasured",
		"test IPv6 HTTPS from a separate network; local assignment and local TLS are not a WAN proof")
	if len(info.VerifiedClients) == 0 {
		warn("no native guest client has verified CA trust yet",
			"do not import the guest CA into system trust or disable TLS verification; wait for a verified client adapter")
	}
	block, _ := pem.Decode([]byte(info.CAPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		warn("guest CA status is not a certificate", "inspect guest-ca.pem; do not replace fleet tls-ca files")
		return true
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA {
		warn("guest CA status is invalid", "inspect the private guest listener signing identity")
		return true
	}
	pin := sha256.Sum256(ca.RawSubjectPublicKeyInfo)
	if hex.EncodeToString(pin[:]) != info.CASPKIPin {
		warn("guest CA pin does not match its PEM", "restore matching guest trust material; never bypass TLS verification")
		return true
	}
	// This one measurement has its OWN roots and transport, not global root
	// import and not the private board's credential-bearing transport.
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	probePublicTLS(&http.Client{Transport: transport, Timeout: 5 * time.Second}, info.URL,
		func(msg string) {
			ok("local guest TLS measurement: " + msg + "; not a WAN or native-client trust proof")
		}, warn)
	return true
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
