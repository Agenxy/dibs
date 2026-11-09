// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nativeHumanMediaRoundTrip(t *testing.T, call func(string, map[string]any) map[string]any,
	reopen func(string, string, string, string, int, ...string) map[string]any,
	token, posted, root string,
) {
	t.Helper()
	send := func(attachments []map[string]any, kinds ...string) (uint64, string, string) {
		t.Helper()
		kind := "question"
		if len(kinds) > 0 {
			kind = kinds[0]
		}
		result := call("send", map[string]any{
			"token": token, "to": "human", "type": kind,
			"body": "Native visual attachment round trip", "attachments": attachments,
		})
		serial := uint64(result["msg_serial"].(float64))
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			files, err := filepath.Glob(filepath.Join(posted, fmt.Sprintf("dibs.msg.*.%d.plist", serial)))
			if err != nil {
				t.Fatal(err)
			}
			if len(files) == 1 {
				return serial, strings.TrimSuffix(filepath.Base(files[0]), ".plist"), files[0]
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("media question notification was not persisted by the real presenter")
		return 0, "", ""
	}
	// Incompressible pixels make the media response exceed the retired 1 MiB
	// RPC cap. This remains a real PNG, not a signature-shaped byte string.
	rng := rand.New(rand.NewPCG(13, 29))
	picture := image.NewRGBA(image.Rect(0, 0, 1024, 512))
	for y := 0; y < 512; y++ {
		for x := 0; x < 1024; x++ {
			picture.SetRGBA(x, y, color.RGBA{uint8(rng.Uint32()), uint8(rng.Uint32()), uint8(rng.Uint32()), 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	if encoded.Len() <= 1<<20 {
		t.Fatal("setup: image does not exercise the large media RPC")
	}
	put := call("put_blob", map[string]any{"token": token, "data": base64.StdEncoding.EncodeToString(encoded.Bytes()), "mime": "image/png"})
	serial, id, path := send([]map[string]any{{"blob": put["blob"]}})
	t.Run("native-image-render", func(t *testing.T) {
		extra := []string{"DIBS_NATIVE_REOPEN_ZOOM=1"}
		if out := os.Getenv("DIBS_NATIVE_REOPEN_ARTIFACTS"); out != "" {
			extra = append(extra, "DIBS_NATIVE_REOPEN_SCREENSHOT="+filepath.Join(out, "native-image.png"))
		}
		view := reopen(id, path, "", "", -1, extra...)
		media, ok := view["media"].([]any)
		if !ok || len(media) != 1 {
			t.Fatalf("native window did not render its image attachment: %v", view)
		}
		item := media[0].(map[string]any)
		if item["kind"] != "image" || item["rendered"] != true {
			t.Errorf("image metadata did not correspond to a painted thumbnail: %v", item)
		}
		if zoom, _ := view["zoomHeight"].(float64); zoom <= 240 {
			t.Errorf("click did not configure a larger image panel: %v", view)
		}
		if view["actionsFit"] != true || view["kindFits"] != true {
			t.Errorf("media displaced actions/header: %v", view)
		}
	})
	node := strings.Split(id, ".")[2]
	oversized := filepath.Join(root, "over-preview-limit.png")
	if err := os.WriteFile(oversized, make([]byte, (16<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	large := call("put_blob", map[string]any{"token": token, "path": oversized, "mime": "image/png"})
	largeSerial, largeID, largePath := send([]map[string]any{{"blob": large["blob"]}})
	for _, test := range []struct {
		name, method string
		payload      map[string]any
		status       int
	}{
		{"media-node", "media", map[string]any{"node": "another-board", "serial": serial, "index": 0}, 404},
		{"media-index", "media", map[string]any{"node": node, "serial": serial, "index": -1}, 404},
		{"media-size", "media", map[string]any{"node": node, "serial": largeSerial, "index": 0}, 413},
		{"upload-node", "upload", map[string]any{"node": "another-board", "name": "one.bin", "mime": "application/octet-stream", "data": "AA=="}, 409},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, "probe-"+test.name+".json")
			data, err := json.Marshal(map[string]any{"method": test.method, "payload": test.payload})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			view := reopen(largeID, largePath, "", "", -1, "DIBS_NATIVE_REOPEN_PROBE="+path)
			probe, ok := view["probe"].(map[string]any)
			if !ok || probe["httpStatus"] != float64(test.status) || probe["ok"] == true || probe["hint"] == "" {
				t.Fatalf("signed native %s did not refuse with an honest hint: %v", test.name, view)
			}
		})
	}
	call("respond", map[string]any{"token": token, "msg_serial": largeSerial, "disposition": "withdraw"})
	for _, mime := range []string{"image/svg+xml", "text/html", "video/mp4"} {
		put := call("put_blob", map[string]any{"token": token, "data": base64.StdEncoding.EncodeToString([]byte("download-only content " + mime)), "mime": mime})
		fileSerial, fileID, filePath := send([]map[string]any{{"blob": put["blob"]}})
		t.Run("download-only-"+strings.ReplaceAll(mime, "/", "-"), func(t *testing.T) {
			probePath := filepath.Join(root, "download-probe.json")
			data, err := json.Marshal(map[string]any{"method": "media", "payload": map[string]any{"node": node, "serial": fileSerial, "index": 0}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(probePath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			view := reopen(fileID, filePath, "", "", -1, "DIBS_NATIVE_REOPEN_PROBE="+probePath)
			items, ok := view["media"].([]any)
			if !ok || len(items) != 1 || items[0].(map[string]any)["rendered"] != false {
				t.Errorf("non-image rendered inline: %v", view)
			}
			if ok && len(items) == 1 && items[0].(map[string]any)["mime"] != mime {
				t.Fatalf("setup: content-addressed registry did not retain the tested MIME %q: %v", mime, items)
			}
			probe, _ := view["probe"].(map[string]any)
			if probe["httpStatus"] != float64(415) {
				t.Errorf("native media endpoint accepted a download-only type: %v", probe)
			}
		})
		call("respond", map[string]any{"token": token, "msg_serial": fileSerial, "disposition": "withdraw"})
	}

	call("respond", map[string]any{"token": token, "msg_serial": serial, "disposition": "withdraw"})
	t.Run("native-comment-files-retry", func(t *testing.T) {
		request, requestID, requestPath := send(nil, "request")
		file := filepath.Join(root, "comment.bin")
		if err := os.WriteFile(file, []byte("comment file"), 0o600); err != nil {
			t.Fatal(err)
		}
		reopen(requestID, requestPath, "Reply without deciding", "A comment with files", -1,
			"DIBS_NATIVE_REOPEN_ATTACH="+file, "DIBS_NATIVE_REOPEN_REPEAT=1")
		inbox := call("inbox", map[string]any{"token": token})
		messages, _ := inbox["messages"].([]any)
		count := 0
		for _, raw := range messages {
			m := raw.(map[string]any)
			if m["type"] != "notify" {
				continue
			}
			read := call("read_mail", map[string]any{"token": token, "msg_serial": m["serial"], "include_body": true})
			message, _ := read["message"].(map[string]any)
			if strings.Contains(fmt.Sprint(message["body"]), fmt.Sprintf("attached to my reply to #%d.", request)) {
				count++
			}
		}
		if count != 1 {
			t.Errorf("same native submission sent %d file notifications, want 1", count)
		}
		read := call("read_mail", map[string]any{"token": token, "msg_serial": request, "include_body": true})
		message, _ := read["message"].(map[string]any)
		if message["state"] != "pending" {
			t.Errorf("comment decided the request: %v", message)
		}
		call("respond", map[string]any{"token": token, "msg_serial": request, "disposition": "withdraw"})
	})

	imageFile := filepath.Join(root, "clipboard.png")
	if err := os.WriteFile(imageFile, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"file", "drop", "paste", "draft-walk", "failed-files"} {
		serial, id, path = send(nil)
		var otherSerial uint64
		if input == "draft-walk" {
			otherSerial, _, _ = send(nil)
		}
		t.Run("native-reply-"+input, func(t *testing.T) {
			content := bytes.Repeat([]byte{0, 17, 128, 255}, 128*1024)
			file := filepath.Join(root, "reply-concept.bin")
			if err := os.WriteFile(file, content, 0o600); err != nil {
				t.Fatal(err)
			}
			mode := input
			if input == "paste" {
				file = imageFile
			}
			extra := []string{"DIBS_NATIVE_REOPEN_ATTACH=" + file, "DIBS_NATIVE_REOPEN_INPUT=" + mode}
			if input == "draft-walk" {
				extra = append(extra, "DIBS_NATIVE_REOPEN_CYCLE=1")
			}
			if input == "failed-files" {
				extra = append(extra, "DIBS_NATIVE_REOPEN_BAD_BLOB=1")
			}
			text := "See the attached concept"
			if input == "paste" {
				text = ""
			} // file-only replies are meaningful too
			view := reopen(id, path, "Send", text, -1, extra...)
			if input == "failed-files" {
				if view["state"] != "answered" || view["reply"] != false || !strings.Contains(fmt.Sprint(view["body"]), "files were not sent") {
					t.Fatalf("recorded decision/file failure was not shown honestly: %v", view)
				}
				read := call("read_mail", map[string]any{"token": token, "msg_serial": serial, "include_body": true})
				message, _ := read["message"].(map[string]any)
				if message["state"] != "answered" || message["response"] != text {
					t.Fatalf("file failure lost the decision: %v", read)
				}
				return
			}
			if input == "draft-walk" {
				trace, _ := view["draftTrace"].([]any)
				if len(trace) < 3 {
					t.Fatalf("draft walk not driven: %v", view)
				}
				for i, want := range []int{1, 0, 1} {
					files, _ := trace[i].(map[string]any)["files"].([]any)
					if len(files) != want {
						t.Errorf("draft files leaked or lost at step %d: %v", i, trace)
					}
				}
			}
			inbox := call("inbox", map[string]any{"token": token})
			messages, _ := inbox["messages"].([]any)
			for _, entry := range messages {
				m := entry.(map[string]any)
				if m["type"] != "notify" {
					continue
				}
				read := call("read_mail", map[string]any{"token": token, "msg_serial": m["serial"], "include_body": true})
				message, _ := read["message"].(map[string]any)
				if !strings.Contains(fmt.Sprint(message["body"]), fmt.Sprintf("#%d", serial)) {
					continue
				}
				attached, _ := message["attachments"].([]any)
				if len(attached) != 1 {
					t.Fatalf("read_mail lost reply attachment: %v", read)
				}
				blob := call("get_blob", map[string]any{"token": token, "blob": attached[0].(map[string]any)["blob"], "as": "inline"})
				data, _ := blob["bytes"].([]byte)
				if input == "paste" {
					if _, err := png.Decode(bytes.NewReader(data)); err != nil {
						t.Fatalf("pasted image was not a PNG: %v", err)
					}
				} else if !bytes.Equal(data, content) {
					t.Fatal("get_blob changed native reply file bytes")
				}
				return
			}
			t.Fatalf("native %s reply did not deliver a file notify to the asker: %v", input, inbox)
		})
		if otherSerial != 0 {
			call("respond", map[string]any{"token": token, "msg_serial": otherSerial, "disposition": "withdraw"})
		}
	}
}
