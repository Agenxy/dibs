// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/tls"
	"os"

	"github.com/agenxy/dibs/internal/localtransport"
)

func checkPrivateTLSKey(path string) error {
	if path == "" || !localtransport.Supported() {
		return nil
	}
	return localtransport.PrivateFile(path, false)
}

func loadPrivateTLSKeyPair(certFile, keyFile string) (tls.Certificate, error) {
	certificate, err := os.ReadFile(certFile) // #nosec G304 -- operator-selected public certificate, never a private key
	if err != nil {
		return tls.Certificate{}, err
	}
	key, err := localtransport.ReadPrivateFile(keyFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certificate, key)
}
