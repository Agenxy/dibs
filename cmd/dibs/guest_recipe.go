package main

import (
	"bytes"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/guesttrust"
	"github.com/agenxy/dibs/internal/invites"
	"github.com/agenxy/dibs/internal/selfupdate"
)

type guestAsset = selfupdate.GuestAsset

type guestRecipe struct {
	Version  int                              `json:"schema_version"`
	Name     string                           `json:"name"`
	Endpoint string                           `json:"endpoint"`
	Key      string                           `json:"invitation_key"`
	Nonce    string                           `json:"recovery_nonce,omitempty"`
	Expires  time.Time                        `json:"expires_at"`
	PEM      string                           `json:"ca_pem"`
	Pin      string                           `json:"ca_spki_sha256"`
	Release  *selfupdate.GuestReleaseMetadata `json:"bridge_release,omitempty"`
}

// A guest recipe is immutable permission, not machine discovery. Opening it
// never initializes local board configuration, credentials or fleet trust.
func readGuestRecipe(path string) (*guestRecipe, *url.URL, *x509.Certificate, error) {
	b, err := privateGuestFile(path, 64<<10)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := uniqueGuestJSON(b); err != nil {
		return nil, nil, nil, err
	}
	var recipe guestRecipe
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&recipe); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid guest recipe: %w; ask the issuer for an original version-1 file", err)
	}
	if err := recipe.validateIdentity(); err != nil {
		return nil, nil, nil, err
	}
	u, ip, err := guestEndpoint(recipe.Endpoint)
	if err != nil {
		return nil, nil, nil, err
	}
	ca, err := guesttrust.ParseRoot([]byte(recipe.PEM), ip, recipe.Pin, time.Now())
	if err != nil {
		return nil, nil, nil, err
	}
	if recipe.Nonce != "" && (len(recipe.Nonce) < 32 || len(recipe.Nonce) > 1024) {
		return nil, nil, nil, errors.New("guest recovery nonce is malformed; ask the issuer for the original private recipe")
	}
	return &recipe, u, ca, nil
}

func (r guestRecipe) validateIdentity() error {
	if r.Version != 1 || !invites.ValidName(r.Name) || !guestInvitationKey(r.Key) ||
		r.Expires.IsZero() || !time.Now().Before(r.Expires) {
		return errors.New("guest recipe has invalid identity, credential or expiry; " +
			"ask the issuer for a live version-1 invitation")
	}
	if r.Release != nil {
		if err := r.Release.Validate(); err != nil {
			return fmt.Errorf("invalid guest release metadata: %w", err)
		}
	}
	return nil
}

func guestEndpoint(raw string) (*url.URL, netip.Addr, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, netip.Addr{}, errors.New("guest endpoint is not a URL; use the issuer's literal IPv6 HTTPS /mcp endpoint")
	}
	ip, ierr := netip.ParseAddr(u.Hostname())
	if ierr != nil || !ip.Is6() || ip.Is4In6() || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() ||
		!guestURLShape(u, raw, ip) {
		return nil, netip.Addr{}, errors.New("guest endpoint must be one canonical literal IPv6 HTTPS address " +
			"with explicit port and exact /mcp; no DNS, credentials, query or redirects")
	}
	return u, ip, nil
}

func guestURLShape(u *url.URL, raw string, ip netip.Addr) bool {
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || u.Host != net.JoinHostPort(ip.String(), strconv.Itoa(port)) {
		return false
	}
	return u.Scheme == "https" && u.User == nil && u.RawQuery == "" && !u.ForceQuery &&
		u.Fragment == "" && u.RawFragment == "" && u.Path == "/mcp" && u.RawPath == "" &&
		u.Opaque == "" && u.String() == raw
}

func guestInvitationKey(key string) bool {
	if !strings.HasPrefix(key, invites.Prefix) {
		return false
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(key, invites.Prefix))
	return err == nil && len(raw) == 32
}

func privateGuestFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("guest recipe path must be absolute; keep it in a private directory")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open guest directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	parent, err := root.Stat(".")
	if err != nil || !parent.IsDir() || !privateGuestOwned(parent) {
		return nil, errors.New("guest recipe directory must be owned by you and private (0700); " +
			"fix its permissions, not TLS")
	}
	return readPrivateGuestRoot(root, filepath.Base(path), limit)
}

func privateGuestOwned(info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 == 0 && guestFileOwned(info)
}

func readPrivateGuestRoot(root *os.Root, name string, limit int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("read guest recipe: %w; ask its issuer for the file", err)
	}
	if !info.Mode().IsRegular() || !privateGuestOwned(info) {
		return nil, errors.New("guest recipe must be a private owned regular file (0600), not a symlink; " +
			"fix the private handoff")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open guest recipe: %w", err)
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("guest recipe changed while opening; stop and check the private handoff")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read guest recipe: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, errors.New("guest private file exceeds its bound; ask the issuer for the original recipe")
	}
	return b, nil
}

// encoding/json intentionally accepts duplicate keys. Permission files do not:
// neither a hidden second endpoint nor a second credential may silently win.
func uniqueGuestJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	if err := guestJSONValue(d, 0); err != nil {
		return fmt.Errorf("invalid guest JSON: %w; obtain the original private recipe", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("guest recipe has trailing JSON; obtain the original private recipe")
	}
	return nil
}

func guestJSONValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("recipe JSON nesting exceeds 16")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, nested := t.(json.Delim)
	if !nested {
		return nil
	}
	switch delim {
	case '{':
		if err := guestJSONObject(d, depth); err != nil {
			return err
		}
	case '[':
		for d.More() {
			if err := guestJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected recipe JSON delimiter")
	}
	_, err = d.Token()
	return err
}

func guestJSONObject(d *json.Decoder, depth int) error {
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return errors.New("duplicate recipe JSON field")
		}
		// encoding/json also matches struct tags case-insensitively. Accept
		// only the schema's canonical lowercase keys so Endpoint cannot hide
		// beside endpoint and silently overwrite the operator's permission.
		if name != strings.ToLower(name) {
			return errors.New("noncanonical recipe JSON field; use the original lowercase schema keys")
		}
		seen[name] = true
		if err := guestJSONValue(d, depth+1); err != nil {
			return err
		}
	}
	return nil
}
