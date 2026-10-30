// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/localtransport"
	"github.com/agenxy/dibs/internal/paths"
)

const clientTransportEnv = "DIBS_TRANSPORT"

// The explicit target kind accompanies the selected profile, rather than
// guessing that any loopback port is this directory's daemon. HTTPS legacy
// addresses remain safe; a legacy plaintext forward gets the local IPC refusal
// and migration hint instead of silently receiving a secret.
func clientTransportIn(dir string) (localtransport.Kind, error) {
	value := os.Getenv(clientTransportEnv)
	if value == "" {
		cfg, err := boardconfig.Load(dir)
		var unknown *boardconfig.UnknownSettingsError
		if err != nil && !errors.As(err, &unknown) {
			return "", err
		}
		value = cfg.ClientTransport
	}
	if value == "" {
		value = string(localtransport.Local)
		if strings.EqualFold(statedScheme(rawAddrIn(dir)), "https") {
			value = string(localtransport.HTTPS)
		}
	}
	return localtransport.Parse(value)
}

func (g *guardedTransport) secureRoundTrip(r *http.Request, dir string) (*http.Response, error) {
	kind, err := clientTransportIn(dir)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	if g.secure == nil || g.secureDir != dir || g.secureKind != kind {
		if old, ok := g.secure.(interface{ CloseIdleConnections() }); ok {
			old.CloseIdleConnections()
		}
		base, ok := g.next.(*http.Transport)
		if !ok {
			g.mu.Unlock()
			return nil, fmt.Errorf("board client has no verifiable HTTP transport; " +
				"use the shared local IPC or verified HTTPS client")
		}
		base = base.Clone()
		if base.TLSClientConfig == nil {
			base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			base.TLSClientConfig = base.TLSClientConfig.Clone()
		}
		base.TLSClientConfig.RootCAs = trustedPoolIn(dir)
		g.secure = localtransport.RoundTripper(dir, kind, base)
		g.secureDir, g.secureKind = dir, kind
	}
	rt := g.secure
	g.mu.Unlock()
	return rt.RoundTrip(r)
}

func localSecret() (string, error) {
	selected, err := paths.SelectDir(false)
	if err != nil {
		return "", err
	}
	return localSecretIn(selected.Dir)
}

func localSecretIn(dir string) (string, error) {
	if localtransport.Supported() {
		if err := localtransport.CheckDir(dir, false); err != nil {
			return "", err
		}
		if err := localtransport.PrivateFile(filepath.Join(dir, "local.secret"), false); err != nil {
			return "", err
		}
	}
	// #nosec G304 -- directory selected from this CLI's local configuration.
	b, err := os.ReadFile(filepath.Join(dir, "local.secret"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (g *guardedTransport) CloseIdleConnections() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if rt, ok := g.secure.(interface{ CloseIdleConnections() }); ok {
		rt.CloseIdleConnections()
	}
}
