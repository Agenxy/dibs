// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real shipped Swift broker + real AppKit controls. Compile-time fixtures
// double only OS notification contact and user gestures. No live window,
// notification authorisation, external app, or process-openable action route.
func TestNativePromptFullTextChoicesTypingAndNoImplicitApproval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	contents := filepath.Join(t.TempDir(), "PromptFixture.app", "Contents")
	macos, res := filepath.Join(contents, "MacOS"), filepath.Join(contents, "Resources")
	for _, p := range []string{macos, res} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fonts := filepath.Join(res, "Fonts")
	if err := os.MkdirAll(fonts, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AtkinsonHyperlegibleNext[wght].ttf", "AtkinsonHyperlegibleMono[wght].ttf"} {
		body, err := os.ReadFile(filepath.Join("app", "fonts", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fonts, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>org.agenxy.dibs.prompt-fixture</string><key>CFBundleExecutable</key><string>dibs-notify</string><key>CFBundlePackageType</key><string>APPL</string><key>LSUIElement</key><true/></dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(macos, "dibs-notify")
	if out, err := exec.CommandContext(ctx, "swiftc", "-D", "DIBS_BROKER_FIXTURE", "-D", "DIBS_READING_FIXTURE", "-o", binary, nativePromptSource(t)).CombinedOutput(); err != nil {
		t.Fatalf("native build: %v\n%s", err, out)
	}
	icon := filepath.Join(t.TempDir(), "icon")
	if out, err := exec.CommandContext(ctx, "swiftc", "-o", icon, "app/icon_darwin.swift").CombinedOutput(); err != nil {
		t.Fatalf("icon build: %v %s", err, out)
	}
	if out, err := exec.CommandContext(ctx, icon, filepath.Join(res, "Dibs.png"), "84").CombinedOutput(); err != nil {
		t.Fatalf("canonical icon: %v %s", err, out)
	}
	cmd := exec.CommandContext(ctx, binary, "--broker")
	postedDir := t.TempDir()
	cmd.Env = append(os.Environ(), "DIBS_BROKER_FIXTURE_DIR="+t.TempDir(), "DIBS_BROKER_POSTED_DIR="+postedDir)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = cmd.Wait() }()
	frames := make(chan map[string]any, 32)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), brokerFrameLimit+1)
		for scanner.Scan() {
			var f map[string]any
			if json.Unmarshal(scanner.Bytes(), &f) == nil {
				frames <- f
			}
		}
	}()
	read := func(event string) map[string]any {
		t.Helper()
		select {
		case f, ok := <-frames:
			if !ok || f["event"] != event {
				t.Fatalf("want %s, got %v", event, f)
			}
			return f
		case <-time.After(5 * time.Second):
			t.Fatalf("missing %s", event)
		}
		return nil
	}
	write := func(f map[string]any) {
		t.Helper()
		f["v"] = 1
		if err := json.NewEncoder(input).Encode(f); err != nil {
			t.Fatal(err)
		}
	}
	if read("ready")["reply"] != float64(1) {
		t.Fatal("typed-reply capability missing")
	}
	body := strings.Repeat("A paragraph with `code`, <data>, and a line break.\n\n", 350) + "FINAL SENTINEL"
	choices := []string{strings.Repeat("First choice wraps completely. ", 4), strings.Repeat("Second choice wraps completely. ", 4), strings.Repeat("Third choice wraps completely. ", 4), strings.Repeat("Fourth choice wraps completely. ", 4)}
	post := func(id, kind string, opts []string) {
		t.Helper()
		buttons := []string{"Later", "Reply…"}
		if kind == "request" {
			buttons = []string{"Deny", "Later", "Approve"}
		}
		frame := map[string]any{"type": "post", "id": id, "title": "Dibs · an agent with a long descriptive name asks", "excerpt": "short", "detail": map[string]string{"sender": "Claude Code · kassette · main · fixture host", "body": body, "kind": kind}, "replyKind": kind, "buttons": buttons, "until": float64(time.Now().Add(time.Minute).Unix())}
		if opts != nil {
			frame["choices"] = opts
		}
		write(frame)
		read("posted")
		write(map[string]any{"type": "fixture_response", "id": id, "action": "com.apple.UNNotificationDefaultActionIdentifier"})
	}
	post("dibs.msg.prompt.7", "question", choices)
	view := read("reading")["view"].(map[string]any)
	if view["body"] != body || view["reply"] != true || len(view["choices"].([]any)) != 4 {
		t.Fatalf("full native prompt lost data: %v", view)
	}
	if view["fontFamily"] != "Atkinson Hyperlegible Next" {
		t.Fatalf("bundled font not used by actual window: %v", view["fontFamily"])
	}
	coldPath := filepath.Join(t.TempDir(), "cold.json")
	cold := exec.CommandContext(ctx, binary)
	cold.Env = append(os.Environ(), "DIBS_READING_FIXTURE=1", "DIBS_READING_POSTED="+filepath.Join(postedDir, "dibs.msg.prompt.7.plist"), "DIBS_READING_WINDOW="+coldPath)
	if out, err := cold.Output(); err != nil || len(out) != 0 {
		t.Fatalf("safe cold reading: %v %q", err, out)
	}
	raw, err := os.ReadFile(coldPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Body, RenderedBody string
		Buttons            []string
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved.Body, body) || !strings.Contains(saved.Body, "cannot confirm") || !strings.Contains(saved.RenderedBody, "FINAL SENTINEL") || strings.Join(saved.Buttons, "|") != "Close" {
		t.Fatal("cold notification lost full text or acquired answer capability")
	}
	if view["choiceGroupRole"] != "AXRadioGroup" || view["headerRole"] != "AXHeading" {
		t.Fatalf("native AX roles missing: %v %v", view["choiceGroupRole"], view["headerRole"])
	}
	if strings.Contains(view["renderedBody"].(string), "`code`") {
		t.Fatal("inline code still has literal backticks")
	}
	write(map[string]any{"type": "fixture_end", "id": "dibs.msg.prompt.7"})
	if tail := read("end")["view"].(map[string]any); tail["endVisible"] != true || tail["endBottomInset"].(float64) < 11 {
		t.Fatal("last message glyph cannot be revealed by scrolling")
	}
	// A stray Return with neither selection nor text does not answer.
	write(map[string]any{"type": "fixture_return", "id": "dibs.msg.prompt.7"})
	read("unchanged")
	screens := os.Getenv("DIBS_PROMPT_SCREENSHOTS")
	if screens == "" {
		screens = t.TempDir()
	} else if err := os.MkdirAll(screens, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, appearance := range []string{"light", "dark"} {
		for _, scale := range []float64{1, 1.5, 1.6} {
			name := appearance + map[float64]string{1: "-100", 1.5: "-150", 1.6: "-160"}[scale] + ".png"
			path := filepath.Join(screens, name)
			write(map[string]any{"type": "fixture_snapshot", "id": "dibs.msg.prompt.7", "path": path, "appearance": appearance, "scale": scale, "screenWidth": 1280, "screenHeight": 800})
			snap := read("snapshot")["view"].(map[string]any)
			if st, err := os.Stat(path); err != nil || st.Size() < 1024 {
				t.Fatalf("snapshot missing %s: %v", path, err)
			}
			if scale == 1.6 && snap["overflow"] != true {
				t.Fatalf("large text must retain controls in accessible scroll region: %v", snap)
			}
			if snap["kindFits"] != true {
				t.Fatal("kind label glyph run clips outside its header")
			}
			if snap["choiceLabelsFit"] != true {
				t.Fatal("a choice label clips inside its native row")
			}
			if scale == 1.6 && snap["moreBelow"] != true {
				t.Fatal("overflow has no visible more-below cue")
			}
			if scale == 1.6 {
				assertPromptLaneMatchesPaper(t, path, snap)
			}
			if scale == 1.6 && snap["bodyHeight"].(float64) < 2000 {
				t.Fatalf("full long message has no scrollable layout: bodyHeight=%v docHeight=%v renderedLength=%v", snap["bodyHeight"], snap["documentHeight"], snap["renderedLength"])
			}
		}
	}
	write(map[string]any{"type": "fixture_tab_choices", "id": "dibs.msg.prompt.7"})
	if focused := read("focused")["view"].(map[string]any); focused["firstChoiceVisible"] != true {
		t.Fatal("Tab did not reveal the choice group")
	}
	write(map[string]any{"type": "fixture_edit", "id": "dibs.msg.prompt.7", "text": "a typed-only answer"})
	edited := read("editing")["view"].(map[string]any)
	if edited["overflow"] == true && edited["replyVisibleHeight"].(float64) < edited["replyHeight"].(float64)-1 {
		t.Fatalf("typed reply is hidden in overflow: %v/%v", edited["replyVisibleHeight"], edited["replyHeight"])
	}
	write(map[string]any{"type": "fixture_snapshot", "id": "dibs.msg.prompt.7", "path": filepath.Join(screens, "dark-160-reply.png"), "appearance": "dark", "preserveScroll": true})
	scrolled := read("snapshot")["view"].(map[string]any)
	assertPromptHeaderMarkVisible(t, filepath.Join(screens, "dark-160-reply.png"), scrolled["width"].(float64))
	write(map[string]any{"type": "fixture_return", "id": "dibs.msg.prompt.7"})
	reply := read("reply")
	if reply["action"] != "answer" || reply["text"] != "a typed-only answer" || reply["choice"] != "" {
		t.Fatalf("typed question: %v", reply)
	}
	post("dibs.msg.prompt.8", "request", nil)
	view = read("reading")["view"].(map[string]any)
	if view["defaultApproval"] != false {
		t.Fatal("approval is default button")
	}
	for _, appearance := range []string{"light", "dark"} {
		for _, scale := range []float64{1, 1.6} {
			name := "request-" + appearance + map[float64]string{1: "-100", 1.6: "-160"}[scale] + ".png"
			write(map[string]any{"type": "fixture_snapshot", "id": "dibs.msg.prompt.8", "path": filepath.Join(screens, name), "appearance": appearance, "scale": scale, "screenWidth": 1280, "screenHeight": 800})
			request := read("snapshot")["view"].(map[string]any)
			if request["defaultApproval"] != false || request["actionsFit"] != true {
				t.Fatal("request decision controls do not fit or an approval became default")
			}
		}
	}
	write(map[string]any{"type": "fixture_edit", "id": "dibs.msg.prompt.8", "text": "I need more detail"})
	read("editing")
	write(map[string]any{"type": "fixture_return", "id": "dibs.msg.prompt.8"})
	read("unchanged")
	write(map[string]any{"type": "fixture_key", "id": "dibs.msg.prompt.8", "keyCode": 36, "focus": "reply"})
	read("keyed")
	write(map[string]any{"type": "fixture_window", "id": "dibs.msg.prompt.8", "action": "Reply without deciding"})
	reply = read("reply")
	if reply["action"] != "comment" || reply["text"] != "I need more detail" {
		t.Fatalf("comment became verdict: %v", reply)
	}
	post("dibs.msg.prompt.9", "question", choices)
	read("reading")
	write(map[string]any{"type": "fixture_key", "id": "dibs.msg.prompt.9", "keyCode": 21, "characters": "4"})
	if selected := read("keyed")["view"].(map[string]any)["selected"]; selected != float64(3) {
		t.Fatalf("number key did not select fourth choice: %v", selected)
	}
	write(map[string]any{"type": "fixture_edit", "id": "dibs.msg.prompt.9", "text": "and a note"})
	read("editing")
	for _, modifier := range []string{"shift", "option"} {
		write(map[string]any{"type": "fixture_key", "id": "dibs.msg.prompt.9", "keyCode": 36, "focus": "reply", "modifiers": []string{modifier}})
		read("keyed")
	}
	write(map[string]any{"type": "fixture_return", "id": "dibs.msg.prompt.9"})
	reply = read("reply")
	if reply["choice"] != choices[3] || reply["text"] != "and a note\n\n" {
		t.Fatalf("choice plus note lost: %v", reply)
	}
	post("dibs.msg.prompt.10", "request", nil)
	read("reading")
	write(map[string]any{"type": "fixture_window", "id": "dibs.msg.prompt.10", "action": "Approve"})
	reply = read("reply")
	if reply["action"] != "approve" {
		t.Fatalf("explicit approval lost: %v", reply)
	}
	post("dibs.msg.prompt.11", "request", nil)
	read("reading")
	write(map[string]any{"type": "fixture_key", "id": "dibs.msg.prompt.11", "keyCode": 53})
	read("dismissed")
}

// Geometry alone misses a scrolled document painting over the pinned header.
// Check the canonical blue dot in the actual AppKit render after editing has
// moved the shared viewport to the reply field; no desktop screenshot is used.
func assertPromptHeaderMarkVisible(t *testing.T, path string, width float64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	scale := float64(img.Bounds().Dx()) / width
	blue := 0
	for y := int(20 * scale); y < int(48*scale); y++ {
		for x := int(20 * scale); x < int(48*scale); x++ {
			r, g, b, a := img.At(x, y).RGBA()
			// AppKit colour management changes the exact RGB bytes. The dot
			// stays distinctly blue; the tile and grey peers do not qualify.
			if a > 60000 && b > 210*257 && b > r+50*257 && b > g+30*257 && g > r+15*257 {
				blue++
			}
		}
	}
	if blue < 10 {
		t.Fatal("scrolled content painted over the pinned sender header")
	}
}

// Compare the shared scroll gutter with adjacent paper in the same native
// render, so colour profiles and appearance do not require fixed RGB bytes.
func assertPromptLaneMatchesPaper(t *testing.T, path string, view map[string]any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	scale := float64(img.Bounds().Dx()) / view["width"].(float64)
	x, y := int(view["overflowLaneX"].(float64)*scale), int(view["overflowLaneY"].(float64)*scale)
	r, g, b, _ := img.At(x, y).RGBA()
	pr, pg, pb, _ := img.At(int(5*scale), y).RGBA()
	for _, delta := range []int64{int64(r) - int64(pr), int64(g) - int64(pg), int64(b) - int64(pb)} {
		if delta > 3*257 || delta < -3*257 {
			t.Fatalf("shared scroll gutter does not match paper: gutter=%v paper=%v", []uint32{r, g, b}, []uint32{pr, pg, pb})
		}
	}
}
