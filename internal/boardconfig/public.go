// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	xport "github.com/agenxy/dibs/internal/transport"
)

// PublicConfig is the optional invitation-only listener, not the fleet listener.
// Each field maps to one daemon flag; Forward explicitly names an operator's
// loopback forwarding boundary, never an implicitly trusted plaintext network.
type PublicConfig struct {
	Host                     string `toml:"host"`
	IP                       string `toml:"ip"`
	URL                      string `toml:"url"`
	Addr                     string `toml:"addr"`
	Forward                  string `toml:"forward"`
	AckUnverifiedGuestClient bool   `toml:"ack_unverified_guest_client"`
	ACMEAcceptTerms          bool   `toml:"acme_accept_terms"`
}

// PublicResolved is validated configuration, not a measurement of reachability.
type PublicResolved struct {
	URL, Addr, Host, Mode string
	IP                    netip.Addr
}

func (c Config) validatePublic() error {
	_, err := ResolvePublic(c.Public)
	if err != nil {
		return fmt.Errorf("[public]: %w", err)
	}
	return nil
}

func dnsHostname(host string) bool {
	if len(host) > 253 || host == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range strings.ToLower(label) {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// ResolvePublic is the one pure validator for file settings and daemon flags.
// Interface assignment and TLS provisioning remain at the daemon edge.
func ResolvePublic(o PublicConfig) (PublicResolved, error) {
	if o.Forward != "" {
		return resolveForwardPublic(o)
	}
	var c PublicResolved
	if o.IP != "" {
		if o.Host != "" || o.URL != "" || o.ACMEAcceptTerms {
			return c, errors.New("choose public-ip OR public-host OR public-url; ACME terms apply only to public-host")
		}
		if !o.AckUnverifiedGuestClient {
			return c, errors.New("no native guest client is verified for direct-IP CA trust yet; " +
				"to opt in explicitly use --ack-unverified-guest-client with --public-ip")
		}
		return resolveIPPublic(o)
	}
	if o.AckUnverifiedGuestClient {
		return c, errors.New("ack-unverified-guest-client requires public-ip")
	}
	if o.Host != "" && o.URL != "" {
		return c, errors.New("choose public-host OR public-url, not both")
	}
	if o.Host == "" && o.URL == "" {
		if o.Addr != "" || o.ACMEAcceptTerms {
			return c, errors.New("public-addr and acme-accept-terms require a public-host or public-url")
		}
		c.Mode = "disabled"
		return c, nil
	}
	if host := strings.ToLower(o.Host); host != "" {
		return resolveACMEPublic(o, host)
	}
	return resolveProxyPublic(o)
}

// An operator names ONE address, never a wildcard or a discovered candidate.
// Assignment is measured; stability and WAN reachability are not inferred.
func resolveIPPublic(o PublicConfig) (PublicResolved, error) {
	var c PublicResolved
	ip, err := netip.ParseAddr(o.IP)
	if err != nil || !IsPublicGuestIPv6(ip) {
		return c, errors.New("public-ip must be an unzoned global IPv6 address, " +
			"not private, link-local, documentation or an IPv4 address")
	}
	c = PublicResolved{Mode: "direct-ip", IP: ip, Addr: publicDefault(o.Addr, net.JoinHostPort(ip.String(), "4778"))}
	host, port, err := net.SplitHostPort(c.Addr)
	bound, parseErr := netip.ParseAddr(host)
	n, portErr := strconv.Atoi(port)
	if err != nil || parseErr != nil || bound != ip || portErr != nil || n < 1 || n > 65535 {
		return PublicResolved{}, errors.New("public-addr must bind exactly public-ip " +
			"and a numeric port from 1 through 65535; " +
			"wildcard listeners are refused")
	}
	c.URL = "https://" + net.JoinHostPort(ip.String(), strconv.Itoa(n))
	return c, nil
}

// IsPublicGuestIPv6 is the direct-IP guest listener's public scope rule.
func IsPublicGuestIPv6(ip netip.Addr) bool {
	// Public global unicast allocation, conservatively excluding reserved
	// documentation and protocol-assignment blocks. IsGlobalUnicast alone also
	// admits ULA and documentation addresses, which are not public endpoints.
	return ip.Is6() && !ip.Is4In6() && ip.Zone() == "" &&
		netip.MustParsePrefix("2000::/3").Contains(ip) &&
		!netip.MustParsePrefix("2001::/23").Contains(ip) &&
		!netip.MustParsePrefix("2001:db8::/32").Contains(ip) &&
		!netip.MustParsePrefix("3fff::/20").Contains(ip)
}

func resolveACMEPublic(o PublicConfig, host string) (PublicResolved, error) {
	var c PublicResolved
	if !dnsHostname(host) || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return c, errors.New("public-host must be a DNS hostname, not a URL, IP or wildcard")
	}
	if !o.ACMEAcceptTerms {
		return c, errors.New("public-host needs explicit --acme-accept-terms; " +
			"alternatively use --public-url behind your own TLS proxy")
	}
	c = PublicResolved{Mode: "acme", URL: "https://" + host, Addr: publicDefault(o.Addr, ":443"), Host: host}
	_, port, err := net.SplitHostPort(c.Addr)
	if err != nil || port != "443" {
		return c, errors.New("public-host uses TLS-ALPN-01: public-addr must listen on port 443; " +
			"use public-url for a TLS-terminating proxy")
	}
	return c, nil
}

func resolveProxyPublic(o PublicConfig) (PublicResolved, error) {
	var c PublicResolved
	u, err := url.Parse(o.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return c, errors.New("public-url must be an HTTPS origin without credentials, query, fragment or path; " +
			"e.g. https://board.example.com")
	}
	if !dnsHostname(u.Hostname()) && net.ParseIP(u.Hostname()) == nil {
		return c, errors.New("public-url requires a DNS hostname or IP literal")
	}
	u.Host = strings.ToLower(u.Host)
	c = PublicResolved{
		Mode: "tls-proxy", URL: strings.TrimSuffix(u.String(), "/"), Addr: publicDefault(o.Addr, "127.0.0.1:4778"),
	}
	if !xport.IsLoopback(c.Addr) {
		return c, errors.New("public-url's plaintext proxy listener must be loopback; set --public-addr 127.0.0.1:4778")
	}
	return c, nil
}

func publicDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func resolveForwardPublic(o PublicConfig) (PublicResolved, error) {
	if o.Host != "" || o.IP != "" || o.URL != "" || o.ACMEAcceptTerms || o.AckUnverifiedGuestClient {
		return PublicResolved{}, errors.New("public-forward is exclusive with public-host, public-ip, public-url " +
			"and their acknowledgements")
	}
	u, err := url.Parse(o.Forward)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" || !publicForwardAddress(u.Host) {
		return PublicResolved{}, errors.New("public-forward must be an HTTP origin with a literal loopback address " +
			"and numeric port, without credentials, path, query or fragment; e.g. http://127.0.0.1:4778")
	}
	c := PublicResolved{URL: u.String(), Addr: publicDefault(o.Addr, "127.0.0.1:4778"), Mode: "loopback-forward"}
	if !publicForwardAddress(c.Addr) {
		return PublicResolved{}, errors.New("public-forward's local public-addr must be a literal loopback address " +
			"and numeric port; wildcards and hostnames are refused")
	}
	return c, nil
}

func publicForwardAddress(address string) bool {
	return xport.IsLoopback(address) && literalPublicAddress(address)
}

// Scope is decided by transport.IsLoopback above. This checks only spelling
// and the port: a literal IP without a zone, and an explicit numeric port.
func literalPublicAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	ip, ipErr := netip.ParseAddr(host)
	number, portErr := strconv.Atoi(port)
	return err == nil && ipErr == nil && ip.Zone() == "" &&
		portErr == nil && number >= 1 && number <= 65535
}
