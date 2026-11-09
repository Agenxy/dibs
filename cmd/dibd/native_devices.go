// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/agenxy/dibs/internal/humankey"
)

// The person adds a phone from the Mac app, and no admin password is typed.
//
//	POST /api/human/native/devices         {node, label, public_key, transcript_digest} → {key_id, ...}
//	POST /api/human/native/devices/list    {node}                                       → {devices}
//	POST /api/human/native/devices/revoke  {node, key_id}                               → {revoked}
//
// The authority is the click in the installed app, proved the way every
// native route proves it: the kernel's audit token for the connecting
// process, checked against the installed app's code identity. The phone
// never sees an admin password or a board secret, and nothing the phone
// signs can add a key: its session and answer signatures are over other
// domains, and this route takes none. What the phone's key then does is the
// existing /api/human/challenge and /api/human/session, exactly as for a key
// enrolled with the password.
//
// A grant is idempotent on the public key, which is how an unknown outcome
// is reconciled: retry with the SAME key and read what comes back. The
// transcript digest returned is the one the key was FIRST granted under, so
// a client can tell its own attempt from an earlier one.

// deviceLabelMax is the pairing wire's bound on a node or label, in bytes.
const deviceLabelMax = 128

type nativeDeviceRequest struct {
	Node       string `json:"node"`
	Label      string `json:"label"`
	PublicKey  string `json:"public_key"`
	Transcript string `json:"transcript_digest"`
	KeyID      string `json:"key_id"`
}

type nativeDevice struct {
	KeyID      string    `json:"key_id"`
	Label      string    `json:"label"`
	Kind       string    `json:"kind"`
	Enrolled   time.Time `json:"enrolled"`
	Transcript string    `json:"transcript_digest,omitempty"`
}

func deviceView(k humankey.Key) nativeDevice {
	kind := k.Kind
	if kind == "" {
		kind = "relay"
	}
	return nativeDevice{KeyID: k.ID, Label: k.Label, Kind: kind, Enrolled: k.Enrolled, Transcript: k.Transcript}
}

// deviceRequest attests the peer, reads the body and checks it names this
// board. The node is what the person chose on the Mac; a grant meant for
// another board is refused, never applied here.
func (h *nativeHumanAPI) deviceRequest(w http.ResponseWriter, r *http.Request) (nativeDeviceRequest, bool) {
	var req nativeDeviceRequest
	if !h.attest(w, r) || !decodeBody(w, r, &req) {
		return req, false
	}
	if req.Node != h.eng.NodeID() {
		humanRefuse(w, http.StatusConflict, "this request names a different board",
			"send it to the board the person selected, whose node is in its pairing context")
		return req, false
	}
	return req, true
}

// validDeviceLabel is the pairing wire's rule, so the label stored is the
// byte string the comparison code was bound to: nonempty, at most 128 bytes
// of UTF-8, and no control characters. It is not trimmed or normalised. It
// is valid UTF-8 already, because the JSON decoder replaced anything else.
func validDeviceLabel(label string) error {
	if label == "" || len(label) > deviceLabelMax {
		return errors.New("the device label must be 1 to 128 bytes of UTF-8")
	}
	for _, c := range label {
		if c < 0x20 || c == 0x7f {
			return errors.New("the device label contains a control character")
		}
	}
	return nil
}

func validTranscriptDigest(d string) bool {
	if len(d) != 64 {
		return false
	}
	for _, c := range d {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (h *nativeHumanAPI) grantDevice(w http.ResponseWriter, r *http.Request) {
	req, ok := h.deviceRequest(w, r)
	if !ok {
		return
	}
	if err := validDeviceLabel(req.Label); err != nil {
		humanRefuse(w, http.StatusBadRequest, err.Error(), "send the label exactly as the pairing transcript bound it")
		return
	}
	if !validTranscriptDigest(req.Transcript) {
		humanRefuse(w, http.StatusBadRequest, "transcript_digest must be a SHA-256 in lowercase hex",
			"send the digest of the pairing transcript both screens confirmed")
		return
	}
	k, err := h.human.keys.AddDevice(req.PublicKey, req.Label, req.Transcript, h.human.now())
	if err != nil {
		humanRefuse(w, http.StatusBadRequest, err.Error(),
			"send the phone's P-256 public key as base64 DER SubjectPublicKeyInfo")
		return
	}
	slog.Info("the person added a device from the Mac app", "key", k.ID, "label", k.Label)
	nativeHumanJSON(w, map[string]any{"ok": true, "node": h.eng.NodeID(), "device": deviceView(k)})
}

func (h *nativeHumanAPI) listDevices(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.deviceRequest(w, r); !ok {
		return
	}
	keys, err := h.human.keys.List()
	if err != nil {
		humanRefuse(w, http.StatusInternalServerError, err.Error(), "the board's human-keys.json needs repair")
		return
	}
	devices := make([]nativeDevice, 0, len(keys))
	for _, k := range keys {
		devices = append(devices, deviceView(k))
	}
	sort.SliceStable(devices, func(i, j int) bool { return devices[i].Enrolled.Before(devices[j].Enrolled) })
	nativeHumanJSON(w, map[string]any{"ok": true, "node": h.eng.NodeID(), "devices": devices})
}

// revokeDevice removes any enrolled key, a phone or a relay, and ends its
// sessions now rather than at the next request that would have noticed.
// Revoking a key that is not there is not an error, so a retry is safe.
func (h *nativeHumanAPI) revokeDevice(w http.ResponseWriter, r *http.Request) {
	req, ok := h.deviceRequest(w, r)
	if !ok {
		return
	}
	if req.KeyID == "" {
		humanRefuse(w, http.StatusBadRequest, "key_id is required", "take it from devices/list")
		return
	}
	removed, err := h.human.keys.Remove(req.KeyID)
	if err != nil {
		humanRefuse(w, http.StatusInternalServerError, err.Error(), "the board's human-keys.json needs repair")
		return
	}
	h.human.ledger.Revoke(req.KeyID)
	if removed {
		slog.Info("the person removed a device from the Mac app", "key", req.KeyID)
	}
	nativeHumanJSON(w, map[string]any{"ok": true, "node": h.eng.NodeID(), "revoked": removed})
}
