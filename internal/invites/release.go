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
func (s *Service) prepareGuestRelease(ctx context.Context, export bool) (*selfupdate.GuestReleaseMetadata, string) {
	const incomplete = "INCOMPLETE: verified artifact metadata is not guest provisioning or runtime compatibility " +
		"acceptance; no bridge download/run instructions are offered"
	if !export && s.endpointInfo().Mode != "direct-ip" {
		return nil, ""
	}
	if selfupdate.GuestSupportingMinimum == "" {
		return nil, "INCOMPLETE: supporting guest release minimum is unset; no artifact metadata is offered"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	v, err := s.releaseEvidence.Load(ctx, s.Store.Dir, build.Version)
	if err != nil {
		return nil, "INCOMPLETE: retained release evidence was refused; use dibs invite --verify-release with an exact " +
			"supporting tag; no artifact metadata is offered"
	}
	snapshot, err := v.GuestSnapshot(selfupdate.GuestSupportingMinimum, build.Version)
	if err != nil {
		return nil, "INCOMPLETE: retained release is incompatible or lacks required signed archive/member digests; " +
			"verify this board's own or a newer supporting tag; no artifact metadata is offered"
	}
	m := snapshot.Metadata()
	return &m, incomplete
}
