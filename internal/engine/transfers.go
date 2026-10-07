// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// TransferIdentity pins an existing identity, not a resumable agent credential.
// Created prevents a replacement at the same address inheriting a ticket.
type TransferIdentity struct {
	Agent       string
	Created     uint64
	Reservation uint64
	MaxSize     int64
	Size        int64
	Mime        string
}

type transferReservation struct {
	agent  string
	size   int64
	future int64
}

// AuthorizeTransfer authenticates on the writer and atomically reserves upload
// space BEFORE ciphertext staging. A download uses the existing mail/owner rule.
// An unknown upload size reserves its entire admitted maximum, conservatively.
func (e *Engine) AuthorizeTransfer(ctx context.Context, token, blob string, size *int64) (TransferIdentity, error) {
	return e.authorizeTransfer(ctx, token, blob, "", size, false)
}

// AuthorizeUpload also checks conservative future ownership. Proven dedup for
// an existing owner costs staging space, but must not cost ownership twice.
func (e *Engine) AuthorizeUpload(ctx context.Context, token, hash string, size *int64) (TransferIdentity, error) {
	return e.authorizeTransfer(ctx, token, "", "sha256:"+hash, size, true)
}

func (e *Engine) authorizeTransfer(
	ctx context.Context, token, blob, expected string, size *int64, future bool,
) (TransferIdentity, error) {
	res, err := e.transferAdmission(ctx, func() core.Result {
		if err := ctx.Err(); err != nil {
			return core.Result{"error": err}
		}
		a, refused := e.authRead(token, time.Now())
		if refused != nil {
			return refused
		}
		if a.Status == core.StatusClosed {
			return core.Result{"error": core.ErrBadToken}
		}
		if _, invited := InvitationFrom(ctx); invitedAgent(a) && !invited {
			return core.Result{"error": inviteRefusal("invited transfers require the public invitation listener")}
		}
		identity := TransferIdentity{Agent: a.ID, Created: a.CreatedSerial, MaxSize: int64(e.state.Limits.MaxBlobSize)}
		if err := e.admitTransferLocked(&identity, blob, size); err != nil {
			return core.Result{"error": err}
		}
		if future {
			if err := e.reserveOwnershipLocked(identity, expected); err != nil {
				delete(e.transfers, identity.Reservation)
				return core.Result{"error": err}
			}
		}
		return core.Result{"identity": identity}
	})
	if err != nil {
		return TransferIdentity{}, err
	}
	i, _ := res["identity"].(TransferIdentity)
	return i, nil
}

func (e *Engine) reserveOwnershipLocked(i TransferIdentity, expected string) error {
	additional := i.MaxSize
	if existing := e.state.Blobs[expected]; existing != nil && existing.Owners[i.Agent] {
		additional = 0
	}
	budget := e.state.AgentBlobBytes(i.Agent) + additional
	for _, r := range e.transfers {
		if r.agent == i.Agent {
			budget += r.future
		}
	}
	if budget > int64(e.state.Limits.PerAgentBlobBytes) {
		return core.ErrQuota
	}
	r := e.transfers[i.Reservation]
	r.future = additional
	e.transfers[i.Reservation] = r
	return nil
}

func (e *Engine) reserveTransferLocked(i *TransferIdentity, size int64) error {
	var total, own int64
	for _, r := range e.transfers {
		total += r.size
		if r.agent == i.Agent {
			own += r.size
		}
	}
	if own+size > int64(e.state.Limits.PerAgentBlobBytes) {
		return core.ErrQuota
	}
	if total+size > int64(e.state.Limits.BlobStoreBytes) {
		return core.ErrStoreFull
	}
	if err := e.stagingDiskSpace(total + size); err != nil {
		return err
	}
	if e.transfers == nil {
		e.transfers = map[uint64]transferReservation{}
	}
	e.transferNext++
	i.Reservation = e.transferNext
	e.transfers[i.Reservation] = transferReservation{agent: i.Agent, size: size}
	return nil
}

// CheckTransfer repeats identity, issuer and content access on EACH byte-plane
// request. It resolves the current token internally; token rotation is not a
// transfer of authority, and no credential is returned to the byte handler.
func (e *Engine) CheckTransfer(ctx context.Context, i TransferIdentity, blob string) error {
	_, err := e.query(ctx, func() core.Result {
		_, err := e.transferAgentLocked(ctx, i, blob)
		if err != nil {
			return core.Result{"error": err}
		}
		return core.Result{"ok": true}
	})
	return err
}

func (e *Engine) transferAgentLocked(ctx context.Context, i TransferIdentity, blob string) (*core.Agent, error) {
	a := e.state.Agents[i.Agent]
	if a == nil || a.CreatedSerial != i.Created || a.Status == core.StatusClosed {
		return nil, core.ErrBadToken
	}
	if invitation, ok := InvitationFrom(ctx); ok && !e.ownsInvitation(&invitation, a) {
		return nil, inviteRefusal("transfer is not owned by this invitation")
	}
	if blob != "" && !e.state.BlobAccessible(blob, a.ID) {
		return nil, core.ErrNoBlob
	}
	return a, nil
}

// CommitTransfer registers only previously durable bytes, without a second
// authorization rate charge. Revalidation on this writer closes the commit race.
func (e *Engine) CommitTransfer(
	ctx context.Context, i TransferIdentity, blob string, size int64, mime string,
) (core.Result, error) {
	req := e.registrationRequest(blob, func() core.Result {
		a, err := e.transferAgentLocked(ctx, i, "")
		if err != nil {
			return core.Result{"error": err}
		}
		reservation, ok := e.transfers[i.Reservation]
		if !ok || reservation.agent != i.Agent || size > reservation.size {
			return core.Result{"error": core.ErrQuota}
		}
		op := &core.Op{Kind: core.OpPutBlob, Token: a.Token, Blob: blob, Size: size, Mime: mime}
		res, err := e.applyAndLedger(op, time.Now())
		if err != nil {
			return core.Result{"error": err}
		}
		delete(e.transfers, i.Reservation)
		return res
	})
	return e.send(ctx, req)
}

func (e *Engine) admitTransferLocked(identity *TransferIdentity, blob string, size *int64) error {
	if blob != "" {
		if !core.ValidBlobID(blob) {
			return core.ErrBadID
		}
		if !e.state.BlobAccessible(blob, identity.Agent) {
			return core.ErrNoBlob
		}
		b := e.state.Blobs[blob]
		identity.Size, identity.Mime = b.Size, b.Mime
		return nil
	}
	reserve := identity.MaxSize
	if size != nil {
		if *size < 0 || *size > reserve {
			return core.ErrTooLargeBlob(e.state.Limits.MaxBlobSize)
		}
		reserve = *size
		identity.MaxSize = *size
	}
	return e.reserveTransferLocked(identity, reserve)
}

// ReleaseTransfer releases only derived staging admission; it is never ledgered.
func (e *Engine) ReleaseTransfer(ctx context.Context, reservation uint64) error {
	_, err := e.query(ctx, func() core.Result { delete(e.transfers, reservation); return core.Result{"ok": true} })
	return err
}
