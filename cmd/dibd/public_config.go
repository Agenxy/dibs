// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"flag"

	"github.com/agenxy/dibs/internal/boardconfig"
)

func publicOptions(o *daemonOpts) boardconfig.PublicConfig {
	return boardconfig.PublicConfig{
		Host: *o.publicHost, IP: *o.publicIP, URL: *o.publicURL, Addr: *o.publicAddr, Forward: *o.publicForward,
		AckUnverifiedGuestClient: *o.ackGuest, ACMEAcceptTerms: *o.acceptACME,
	}
}

func loadConfigWithPublicFlags(dir string, o *daemonOpts, fs *flag.FlagSet) (Config, error) {
	cfg, err := boardconfig.LoadWithPublicOverrides(dir, func(p *boardconfig.PublicConfig) {
		// Visit, rather than nonzero values: false and empty are real overrides.
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "public-host":
				p.Host = *o.publicHost
			case "public-ip":
				p.IP = *o.publicIP
			case "public-url":
				p.URL = *o.publicURL
			case "public-addr":
				p.Addr = *o.publicAddr
			case "public-forward":
				p.Forward = *o.publicForward
			case "ack-unverified-guest-client":
				p.AckUnverifiedGuestClient = *o.ackGuest
			case "acme-accept-terms":
				p.ACMEAcceptTerms = *o.acceptACME
			}
		})
	})
	if err != nil {
		return cfg, err
	}
	p := cfg.Public
	*o.publicHost, *o.publicIP, *o.publicURL = p.Host, p.IP, p.URL
	*o.publicAddr, *o.publicForward = p.Addr, p.Forward
	*o.ackGuest, *o.acceptACME = p.AckUnverifiedGuestClient, p.ACMEAcceptTerms
	return cfg, nil
}
