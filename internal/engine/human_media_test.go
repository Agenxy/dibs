// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/blobstore"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

func TestMediaKindRendersOnlyRasterImagesAndVideo(t *testing.T) {
	for mime, want := range map[string]string{
		"image/png": "image", "IMAGE/JPEG": "image", "image/webp": "image",
		"video/mp4": "video", "video/quicktime": "video",
		// Script-capable or unknown types are downloads, never rendered.
		"image/svg+xml": "", "text/html": "", "application/pdf": "", "": "",
	} {
		if got := MediaKind(mime); got != want {
			t.Errorf("MediaKind(%q) = %q, want %q", mime, got, want)
		}
	}
}

// An agent's screenshot reaches the person as bytes, a file the person
// attaches to a reply reaches the agent, and an aged-out attachment says it
// expired instead of vanishing.
func TestThePersonSeesAndSendsAttachments(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := blobstore.New(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	limits := core.DefaultLimits()
	limits.BlobMaxAge = 2 * time.Hour // short, so the sweep below ages out the file and not the mail
	e := New(core.NewState("media-node", limits), &memLedger{}, deadProber{})
	e.SetBlobs(store)
	go e.Run(ctx)
	human, _, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	register := func(name string) string {
		t.Helper()
		r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name, Nonce: name + "-nonce"})
		if err != nil || r["token"] == nil {
			t.Fatalf("register %s: %v %v", name, r, err)
		}
		return r["token"].(string)
	}
	agent, other := register("designer"), register("bystander")
	png := []byte("\x89PNG\r\n\x1a\nnot really a png")
	put, err := e.PutBlob(ctx, agent, png, "", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	shot := put["blob"].(string)
	send := func(token, to, kind string) uint64 {
		t.Helper()
		r, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: token, To: to, MsgType: kind,
			Body: "see attached", Attachments: []core.Attachment{{Blob: shot}}})
		if err != nil || r["msg_serial"] == nil {
			t.Fatalf("send: %v %v", r, err)
		}
		return r["msg_serial"].(uint64)
	}
	toHuman := send(agent, human, core.MsgQuestion)
	file, err := e.MessageAttachment(ctx, toHuman, 0, true)
	if err != nil || !bytes.Equal(file.Data, png) || file.Kind != "image" || file.Mime != "image/png" {
		t.Fatalf("the person's screenshot: %+v %v", file, err)
	}
	view, err := e.NativeHumanMessage(ctx, "media-node", toHuman)
	if err != nil || len(view.Media) != 1 || view.Media[0].Kind != "image" || view.Media[0].Expired {
		t.Fatalf("native view media: %+v %v", view.Media, err)
	}
	// The native window reads only the person's own mail; the board sees all.
	between := send(agent, "bystander", core.MsgNotify)
	if _, err := e.MessageAttachment(ctx, between, 0, true); !errors.Is(err, core.ErrNoBlob) {
		t.Fatalf("native read of another agent's mail: %v", err)
	}
	if _, err := e.MessageAttachment(ctx, between, 0, false); err != nil {
		t.Fatalf("board read: %v", err)
	}
	if _, err := e.MessageAttachment(ctx, toHuman, 1, true); !errors.Is(err, core.ErrNoBlob) {
		t.Fatalf("an index past the attachments: %v", err)
	}

	// The person answers with a concept image of their own.
	concept := []byte("a concept image")
	up, err := e.HumanPutBlob(ctx, concept, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AnswerNativeHuman(ctx, "media-node", toHuman, "answer", "like this", ""); err != nil {
		t.Fatal(err)
	}
	res, err := e.HumanReplyFiles(ctx, toHuman, []string{up["blob"].(string)})
	if err != nil || res["msg_serial"] == nil {
		t.Fatalf("reply files: %v %v", res, err)
	}
	got, err := e.GetBlob(ctx, agent, up["blob"].(string), "inline")
	if err != nil || !bytes.Equal(got["bytes"].([]byte), concept) {
		t.Fatalf("the asker cannot read the person's file: %v %v", got, err)
	}
	if _, err := e.GetBlob(ctx, other, up["blob"].(string), "inline"); err == nil {
		t.Fatal("a bystander read the person's reply file")
	}
	if _, err := e.HumanReplyFiles(ctx, between, []string{up["blob"].(string)}); !errors.Is(err, ErrNotTheHumans) {
		t.Fatalf("reply files on mail that was not the person's: %v", err)
	}

	// Past the maximum age the attachment is gone, and both surfaces say so.
	if _, err := e.query(ctx, func() core.Result {
		_, _ = e.exec(&core.Op{Kind: core.OpSweep}, time.Now().Add(3*time.Hour))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.MessageAttachment(ctx, toHuman, 0, true); !errors.Is(err, core.ErrBlobEvicted) {
		t.Fatalf("an expired attachment: %v", err)
	}
	view, err = e.NativeHumanMessage(ctx, "media-node", toHuman)
	if err != nil || len(view.Media) != 1 || !view.Media[0].Expired {
		t.Fatalf("native view of an expired attachment: %+v %v", view.Media, err)
	}
}
