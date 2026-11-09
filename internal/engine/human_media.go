// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agenxy/dibs/internal/core"
)

// The person's side of attachments. Agents have always been able to attach
// files to mail; the person could see that a message had one and nothing more:
// the board printed a content hash where the screenshot should have been, and
// could not attach anything back. Everything here goes through the same blob
// store, quotas and maximum age as an agent's attachment, as the person's own
// agent, so an attachment from the person is an ordinary attachment.

// HumanUploadLimits returns immutable constructor limits without a writer hop.
func (e *Engine) HumanUploadLimits() (bytes, files int) {
	return e.state.Limits.MaxBlobSize, e.state.Limits.MaxAttachments
}

// MediaKind is what a person's surface may render inline: raster images and
// common video. Everything else, SVG and HTML above all because they can carry
// script, is offered as a download and never rendered in the board's origin.
func MediaKind(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif":
		return "image"
	case "video/mp4", "video/webm", "video/quicktime":
		return "video"
	}
	return ""
}

// MediaInfo describes one attachment for a person's surface, without its bytes.
type MediaInfo struct {
	Index   int    `json:"index"`
	Mime    string `json:"mime,omitempty"`
	Kind    string `json:"kind,omitempty"` // image | video | "" (download only)
	Size    int64  `json:"size,omitempty"`
	Expired bool   `json:"expired,omitempty"` // aged out or evicted; the message keeps its line
	File    bool   `json:"file,omitempty"`    // an advisory local path Dibs never opened
}

// mediaOfLocked lists a message's attachments for the person. On the loop.
func (e *Engine) mediaOfLocked(m *core.Message) []MediaInfo {
	var out []MediaInfo
	for i, a := range m.Attachments {
		info := MediaInfo{Index: i, Mime: a.Mime, Size: a.Size}
		if a.IsBlob() {
			info.Kind = MediaKind(a.Mime)
			info.Expired = e.state.Blobs[a.Blob] == nil
		} else {
			info.File = true
		}
		out = append(out, info)
	}
	return out
}

// MessageFile is one attachment's bytes, typed by the registry rather than by
// the sender.
type MessageFile struct {
	Mime string
	Kind string
	Size int64
	Data []byte
}

// MessageAttachment reads attachment index of message serial for the person.
// The board is the person's view of every message, so it may read any; the
// native window passes humanOnly and reads only mail to or from the person.
// Access is decided on the loop and the decrypt runs off it, as get_blob does.
func (e *Engine) MessageAttachment(ctx context.Context, serial uint64, index int, humanOnly bool) (MessageFile, error) {
	if e.blobs == nil {
		return MessageFile{}, errors.New("blob store not configured")
	}
	var id string
	var file MessageFile
	live := false
	_, err := e.query(ctx, func() core.Result {
		m := e.state.Messages[serial]
		if m == nil || index < 0 || index >= len(m.Attachments) || !m.Attachments[index].IsBlob() {
			return nil
		}
		if human := e.humanIdentityLocked(); humanOnly && m.To != human && m.From != human {
			return nil
		}
		id = m.Attachments[index].Blob
		if b := e.state.Blobs[id]; b != nil {
			file, live = MessageFile{Mime: b.Mime, Kind: MediaKind(b.Mime), Size: b.Size}, true
		}
		return nil
	})
	if err != nil {
		return MessageFile{}, err
	}
	if id == "" {
		return MessageFile{}, core.ErrNoBlob
	}
	if !live {
		return MessageFile{}, core.ErrBlobEvicted
	}
	data, err := e.blobs.Read(id)
	if err != nil {
		return MessageFile{}, mapBlobErr(err)
	}
	file.Data = data
	return file, nil
}

// HumanPutBlob stores a file the person is about to attach, as the person's
// own agent, under the same size limit, quota and age as any attachment.
func (e *Engine) HumanPutBlob(ctx context.Context, data []byte, mime string) (core.Result, error) {
	_, token, err := e.HumanAgent(ctx)
	if err != nil {
		return nil, err
	}
	return e.PutBlob(ctx, token, data, "", mime)
}

// HumanReplyFiles sends the files the person attached to a reply. A reply is
// text stored on the asker's message, so the files follow it as a notify to
// the asker naming the message they answer, rather than widening respond and
// the snapshot it is stored in. Call it after the reply itself succeeded.
// The optional reply submission id deduplicates a retry of this file notify
// separately from the text reply's op id. Omit it only for a one-shot caller.
func (e *Engine) HumanReplyFiles(
	ctx context.Context, serial uint64, blobs []string, submission ...string,
) (core.Result, error) {
	if len(blobs) == 0 {
		return nil, nil
	}
	_, token, err := e.HumanAgent(ctx)
	if err != nil {
		return nil, err
	}
	var asker string
	if _, err := e.query(ctx, func() core.Result {
		if m := e.state.Messages[serial]; m != nil && m.To == e.humanIdentityLocked() {
			asker = m.From
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if asker == "" {
		return nil, ErrNotTheHumans
	}
	atts := make([]core.Attachment, len(blobs))
	for i, b := range blobs {
		atts[i] = core.Attachment{Blob: b}
	}
	noun := "file"
	if len(blobs) > 1 {
		noun = "files"
	}
	var opID string
	if len(submission) > 0 && submission[0] != "" {
		opID = "human-reply:" + submission[0] + "/files"
	}
	return e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: token, To: asker, MsgType: core.MsgNotify,
		Body:        fmt.Sprintf("The %s attached to my reply to #%d.", noun, serial),
		Attachments: atts, OpID: opID,
	})
}
