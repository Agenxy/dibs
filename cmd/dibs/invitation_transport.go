// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/localtransport"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/transport"
)

var warnLegacyInvitation sync.Once

const invitationForwardHint = "invitation bearer tokens on a plaintext loopback SSH forward can be captured " +
	"by another OS user squatting that forwarded port. " +
	"The existing labs-broker forward remains supported for migration; " +
	"serve verified HTTPS at the board and carry TLS through the SSH forward, then use its https:// endpoint " +
	"and trust the board CA. Never send the local coordination secret through this route"

// The operator's existing invitation-only forward is separate from local
// coordination. It never acquires or refreshes X-Dibs-Local, even on Windows.
func invitationClient(timeout time.Duration) *http.Client {
	base, _ := url.Parse(mcpEndpoint())
	selected, trustErr := paths.SelectDir(false)
	var roots *x509.CertPool
	if trustErr == nil {
		roots, trustErr = invitationTrustIn(selected.Dir)
	}
	rt := &http.Transport{
		TLSClientConfig:     &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13},
		TLSHandshakeTimeout: 5 * time.Second,
	}
	return &http.Client{
		Timeout: timeout, CheckRedirect: localtransport.RefuseRedirect,
		Transport: transport.Stamp(&invitationRoundTripper{base: base, next: rt, trustErr: trustErr}),
	}
}

func invitationTrustIn(dir string) (*x509.CertPool, error) {
	if localtransport.Supported() {
		if err := localtransport.CheckDir(dir, false); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// An invite needs no local board. With no profile, use only
				// system TLS roots and never read trust through an unsafe path.
				return nil, nil
			}
			return nil, err
		}
	}
	return trustedPoolIn(dir), nil
}

type invitationRoundTripper struct {
	base     *url.URL
	next     *http.Transport
	trustErr error
}

func (t *invitationRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.trustErr != nil {
		return nil, t.trustErr
	}
	if t.base == nil || r.URL.User != nil || r.URL.Scheme != t.base.Scheme || r.URL.Host != t.base.Host ||
		r.Header.Get("X-Dibs-Local") != "" || r.Header.Get("X-Dibs-Admin") != "" {
		return nil, errors.New("invitation request changed endpoint or carried a local/admin credential; " +
			"use only the issued invitation endpoint")
	}
	if r.URL.Scheme == "http" && transport.IsLoopback(r.URL.Host) {
		warnLegacyInvitation.Do(func() { _, _ = fmt.Fprintln(os.Stderr, "Dibs: "+invitationForwardHint) })
	} else if r.URL.Scheme != "https" {
		return nil, errors.New("invitation requests require verified HTTPS " +
			"or the explicitly documented legacy loopback forward")
	}
	return t.next.RoundTrip(r)
}

func (t *invitationRoundTripper) CloseIdleConnections() { t.next.CloseIdleConnections() }
