// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package selfupdate

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/agenxy/dibs/internal/release"
)

// GuestSupportingMinimum stays unset until release preparation identifies the
// first published COMPLETE guest contract. Only compiled test fixtures set it
// before then; no environment, board config or invitation can lower this floor.
var GuestSupportingMinimum = ""

// GuestVersionHeader is an HTTP compatibility declaration, NOT authentication.
// The bridge stamps its actual build on every request; harness clientInfo,
// recipe metadata and a legacy session never supply this evidence.
const GuestVersionHeader = "X-Dibs-Guest-Version"

// CheckGuestBridgeVersion refuses non-release images once the compiled floor
// is assigned. Unset is the unreleased checkpoint, not version zero: it offers
// no provisioning, while leaving existing invitation transport unchanged.
func CheckGuestBridgeVersion(version, minimum string) error {
	if minimum == "" {
		return nil
	}
	_, err := SelectGuestRelease("v"+strings.TrimPrefix(version, "v"), minimum, "")
	return err
}

// SelectGuestRelease is the single issuer rule used by explicit acquisition
// and daemon admission. A development board is not relabelled as a release.
func SelectGuestRelease(tag, minimum, boardVersion string) (Release, error) {
	if minimum == "" {
		return Release{}, errors.New("INCOMPLETE: supporting guest release minimum is unset; " +
			"no release record or invitation was changed")
	}
	want, err := ReleaseForTag(tag)
	if err != nil {
		return Release{}, err
	}
	floor, err := ReleaseForTag(minimum)
	if err != nil {
		return Release{}, errors.New("compiled guest release minimum is invalid; repair the build, not its cache")
	}
	if cmp, _ := release.Compare(want.Version, floor.Version); cmp < 0 {
		return Release{}, fmt.Errorf("release %s predates the compiled guest minimum %s; "+
			"verify a supporting release instead", tag, minimum)
	}
	if own, err := ReleaseForTag("v" + strings.TrimPrefix(boardVersion, "v")); err == nil {
		if cmp, _ := release.Compare(want.Version, own.Version); cmp < 0 {
			return Release{}, fmt.Errorf("release %s is older than this board build %s; "+
				"verify its own or a newer tag", tag, boardVersion)
		}
	}
	return want, nil
}

// GuestAsset describes a member of an immutable release archive. It grants no
// authority to install, run a bridge, or change a harness configuration.
type GuestAsset struct {
	OS         string `json:"goos"`
	Arch       string `json:"goarch"`
	URL        string `json:"url"`
	ArchiveSHA string `json:"archive_sha256"`
	BinarySHA  string `json:"dibs_sha256"`
}

// GuestReleaseMetadata is the detached private-handoff projection, not a new
// release manifest. BoardBuild always describes the ACTUAL running issuer.
type GuestReleaseMetadata struct {
	Tag        string       `json:"tag"`
	BoardBuild string       `json:"board_build"`
	Status     string       `json:"provisioning_status"`
	Assets     []GuestAsset `json:"assets"`
}

// GuestReleaseSnapshot can only be constructed from signature-verified
// evidence plus compiled selection. Its public projection is a detached copy.
type GuestReleaseSnapshot struct{ metadata GuestReleaseMetadata }

// Metadata returns public artifact information without sharing its backing slice.
func (s GuestReleaseSnapshot) Metadata() GuestReleaseMetadata {
	m := s.metadata
	m.Assets = append([]GuestAsset(nil), m.Assets...)
	return m
}

// GuestSnapshot does no I/O. Its receiver is immutable verified evidence,
// never a caller-supplied JSON verified flag or checksum string.
func (v VerifiedRelease) GuestSnapshot(minimum, boardVersion string) (GuestReleaseSnapshot, error) {
	rel, err := SelectGuestRelease(v.tag, minimum, boardVersion)
	if err != nil {
		return GuestReleaseSnapshot{}, err
	}
	sums, err := GuestReleaseDigests(v.checksums)
	if err != nil {
		return GuestReleaseSnapshot{}, err
	}
	m := GuestReleaseMetadata{Tag: rel.Tag, BoardBuild: boardVersion, Status: "INCOMPLETE"}
	for _, target := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		archive, err := ArchiveName(rel.Version, target[0], target[1])
		if err != nil {
			return GuestReleaseSnapshot{}, err
		}
		member, err := GuestMemberName(target[0], target[1])
		if err != nil {
			return GuestReleaseSnapshot{}, err
		}
		if sums[archive] == "" || sums[member] == "" {
			return GuestReleaseSnapshot{}, fmt.Errorf("INCOMPLETE: signed %s names no exact archive/member digests "+
				"for %s/%s; verify a supporting release", rel.Tag, target[0], target[1])
		}
		m.Assets = append(m.Assets, GuestAsset{
			OS: target[0], Arch: target[1], URL: DownloadURL(rel.Tag, archive),
			ArchiveSHA: sums[archive], BinarySHA: sums[member],
		})
	}
	if err := m.Validate(); err != nil {
		return GuestReleaseSnapshot{}, err
	}
	return GuestReleaseSnapshot{metadata: m}, nil
}

// Validate checks handoff shape only; it is NOT issuer signature verification.
// The guest's trust boundary remains the same private handoff as its CA pin.
func (m GuestReleaseMetadata) Validate() error {
	rel, err := ReleaseForTag(m.Tag)
	if err != nil {
		return err
	}
	if m.Status != "INCOMPLETE" || m.BoardBuild == "" || len(m.BoardBuild) > 256 ||
		strings.ContainsAny(m.BoardBuild, "\r\n\x00") || len(m.Assets) != 3 {
		return errors.New("guest release metadata needs actual board provenance and exactly three published targets")
	}
	seen := map[string]bool{}
	for _, a := range m.Assets {
		member, err := a.validate(rel)
		if err != nil {
			return err
		}
		if seen[member] {
			return errors.New("guest release metadata repeats or invents a published target")
		}
		seen[member] = true
	}
	return nil
}

func (a GuestAsset) validate(rel Release) (string, error) {
	member, err := GuestMemberName(a.OS, a.Arch)
	if err != nil {
		return "", err
	}
	archive, err := ArchiveName(rel.Version, a.OS, a.Arch)
	if err != nil || a.URL != DownloadURL(rel.Tag, archive) {
		return "", errors.New("guest asset URL is not the exact immutable release archive")
	}
	for _, digest := range []string{a.ArchiveSHA, a.BinarySHA} {
		b, err := hex.DecodeString(digest)
		if err != nil || len(b) != sha256.Size || hex.EncodeToString(b) != digest {
			return "", errors.New("guest asset needs canonical SHA-256 archive and executable digests")
		}
	}
	return member, nil
}

// GuestReleaseDigests is shared by actual archive verification and issuer
// admission: malformed or duplicate lines can never silently select a digest.
func GuestReleaseDigests(text string) (map[string]string, error) {
	out := map[string]string{}
	s := bufio.NewScanner(strings.NewReader(text))
	for s.Scan() {
		digest, name, ok := strings.Cut(s.Text(), "  ")
		decoded, err := hex.DecodeString(digest)
		if !ok || err != nil || len(decoded) != sha256.Size || name == "" {
			return nil, errors.New("release checksum input contains a malformed SHA-256 line")
		}
		if _, exists := out[name]; exists {
			return nil, fmt.Errorf("release checksum input repeats %s", name)
		}
		out[name] = strings.ToLower(digest)
	}
	return out, s.Err()
}
