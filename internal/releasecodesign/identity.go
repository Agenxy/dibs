// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package releasecodesign pins the macOS identity whose grants updates retain.
package releasecodesign

import (
	"fmt"
	"strings"
)

// CertificateSHA1 is the self-signed Dibs Release Codesign certificate root.
// This is a codesign requirement fingerprint, not an artifact integrity hash.
// Rotation deliberately invalidates existing grants; see docs/RELEASE.md.
const CertificateSHA1 = "27113117309b43e4748ed37d7002d1122a7af9ae"

func CheckCertificate(hash string) error {
	if !strings.EqualFold(hash, CertificateSHA1) {
		return fmt.Errorf("release signing certificate changed: got %q, pinned %s; "+
			"refuse to invalidate macOS grants silently; follow docs/RELEASE.md's deliberate rotation procedure", hash, CertificateSHA1)
	}
	return nil
}

func Requirement(identifier string) string {
	return fmt.Sprintf(`identifier %q and certificate root = H"%s"`, identifier, CertificateSHA1)
}

// Check inspects codesign's diagnostic output, never the signature's binary
// representation. Ad-hoc signatures are allowed only for offline rehearsals.
func Check(out, identifier string, allowAdhoc bool) error {
	var requirement string
	adhoc := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		adhoc = adhoc || line == "Signature=adhoc"
		if after, ok := strings.CutPrefix(strings.TrimPrefix(line, "# "), "designated => "); ok {
			requirement = after
		}
	}
	if adhoc && allowAdhoc {
		return nil
	}
	if requirement != Requirement(identifier) {
		return fmt.Errorf("release designated requirement changed: got %q, pinned %q; "+
			"refuse packaging; see docs/RELEASE.md's deliberate certificate rotation procedure", requirement, Requirement(identifier))
	}
	return nil
}
