// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package invites

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/selfupdate"
)

// prepareGuestRelease runs only for a direct-IP mint or an explicit private
// export, AFTER issuer authorization but BEFORE recovery/key disclosure. It is
// outside core and the writer, uses no network client, and never gates ordinary
// invitation issuance, list, revoke or private fleet access.
func (s *Service) prepareGuestRelease(
	ctx context.Context, export bool,
) (*selfupdate.GuestReleaseMetadata, *selfupdate.GuestProvisioning, string) {
	const incomplete = "INCOMPLETE: verified artifact metadata is not guest provisioning or runtime compatibility " +
		"acceptance; literal steps appear only in an explicit private export"
	if !export && s.endpointInfo().Mode != "direct-ip" {
		return nil, nil, ""
	}
	if selfupdate.GuestSupportingMinimum == "" {
		return nil, nil, "INCOMPLETE: supporting guest release minimum is unset; " +
			"no artifact metadata or provisioning is offered"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	v, err := s.releaseEvidence.Load(ctx, s.Store.Dir, build.Version)
	if err != nil {
		return nil, nil, "INCOMPLETE: retained release evidence was refused; " +
			"use dibs invite --verify-release with an exact supporting tag; no artifact metadata is offered"
	}
	snapshot, err := v.GuestSnapshot(selfupdate.GuestSupportingMinimum, build.Version)
	if err != nil {
		return nil, nil, "INCOMPLETE: retained release is incompatible or lacks required signed archive/member digests; " +
			"verify this board's own or a newer supporting tag; no artifact metadata is offered"
	}
	m := snapshot.Metadata()
	if !export {
		return &m, nil, incomplete
	}
	p, err := snapshot.Provisioning()
	if err != nil {
		return &m, nil, "INCOMPLETE: verified provisioning projection was refused; repair the issuer before exporting"
	}
	return &m, &p, incomplete
}
