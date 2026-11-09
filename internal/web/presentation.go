// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package web

import (
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

// Presentation fields are additive. Event and Message remain stable-id ledger
// objects; the current address is resolved only when the human view is read.
type eventPresentation struct {
	core.Event
	AgentName string `json:"agent_name,omitempty"`
	ToName    string `json:"to_name,omitempty"`
}

type messagePresentation struct {
	*core.Message
	FromName    string                   `json:"from_name"`
	ToName      string                   `json:"to_name"`
	AdoptName   string                   `json:"adopt_name,omitempty"`
	Attachments []attachmentPresentation `json:"attachments,omitempty"`
}

type attachmentPresentation struct {
	core.Attachment
	Kind    string `json:"kind,omitempty"`
	Expired bool   `json:"expired,omitempty"`
}

func presentAttachments(attachments []core.Attachment, media []engine.MediaInfo) []attachmentPresentation {
	out := make([]attachmentPresentation, len(attachments))
	for i, a := range attachments {
		out[i] = attachmentPresentation{Attachment: a}
		if i < len(media) {
			out[i].Kind, out[i].Expired = media[i].Kind, media[i].Expired
		}
	}
	return out
}

func displayName(names map[string]string, id string) string {
	if id == "" {
		return ""
	}
	if name := names[id]; name != "" {
		return name
	}
	return id
}
