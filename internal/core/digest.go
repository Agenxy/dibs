// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// COUNTS AND ALL THE FIELDS, because a flat list of strings is ambiguous.
//
// The lists were appended one after another with nothing saying where one ended
// and the next began, so these two different messages digested identically:
//
//	choices ["x", "", "/tmp/a", ""]  with no attachments
//	choices ["x"]                    with attachment {path: "/tmp/a"}
//
// The second retry is then answered ok:true, deduplicated:true, and what is
// stored is the first: one message offering four answers, the other offering one
// answer and a file. Size and Mime were missing outright, so two attachments
// differing only in what they claim to be were the same message here.
//
// Lengths first, every field included. Found by a pre-release review.
func sendDigest(op *Op) string {
	parts := []string{op.To, op.MsgType, op.Body, itoa(op.DeadlineSec), op.Grant, op.Adopt}
	parts = append(parts, itoa(len(op.Choices)))
	parts = append(parts, op.Choices...)
	parts = append(parts, itoa(len(op.Milestones)))
	parts = append(parts, op.Milestones...)
	if op.Track {
		parts = append(parts, "track")
	}
	if op.RequestPriority != "" {
		parts = append(parts, "request_priority", op.RequestPriority)
	}
	parts = append(parts, itoa(len(op.Attachments)))
	for _, a := range op.Attachments {
		parts = append(parts, a.Blob, a.Path, a.Hash, a.Mime, itoa(int(a.Size)))
	}
	return digestOf(parts...)
}

// digestOf hashes a list of strings so that no two different lists collide.
//
// LENGTH-PREFIXED, not delimiter-separated. This wrote each part followed by a
// NUL, which is only unambiguous if no part can CONTAIN a NUL, and JSON accepts
// \u0000 quite happily while Admit has no reason to refuse it. So these two
// distinct answer spaces encoded to the same bytes:
//
//	["a\x00b", "c"]
//	["a", "b\x00c"]
//
// Reusing an op_id with the second is then answered ok:true, deduplicated:true,
// and the question that stands is the first, offering different answers than
// the caller asked for. An encoding collision, not a hash collision: no amount
// of SHA-256 helps.
//
// The previous round of this added element COUNTS, which fixed the boundary
// between two lists and left the boundary between two elements exactly as it
// was. Prefixing each part with its length closes both, for any bytes at all.
// Found by a pre-release review, on the fix for the same defect.
func digestOf(parts ...string) string {
	h := sha256.New()
	var n [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}
