// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/localtransport"
	"github.com/agenxy/dibs/internal/testport"
)

// This child runs as the real second account created by the explicit Linux CI
// step. Its stdout carries only fixture readiness/counts, never credentials.
func TestQOtherUIDListener(t *testing.T) {
	if os.Getenv("DIBS_Q_FOREIGN_CHILD") != "1" {
		return
	}
	unix, err := net.Listen("unix", filepath.Join(os.Getenv("DIBS_Q_SOCKET_DIR"), "dibd.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close() }()
	if err := os.Chmod(unix.Addr().String(), 0o600); err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tcp.Close() }()
	var requests atomic.Int32
	var accepts atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"agents":[]}`)
	})
	a := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	b := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = a.Serve(&qCountListener{Listener: unix, accepts: &accepts}) }()
	go func() { _ = b.Serve(tcp) }()
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": true, "addr": tcp.Addr().String(), "uid": os.Geteuid()}); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = a.Close()
	_ = b.Close()
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"requests": requests.Load(), "accepts": accepts.Load()}); err != nil {
		t.Fatal(err)
	}
}

type qCountListener struct {
	net.Listener
	accepts *atomic.Int32
}

func (l *qCountListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepts.Add(1)
	}
	return c, err
}

// A fake Stat result cannot prove this property. The extra CI invocation is
// root only so it can create and retire one real isolated OS account, launch a
// foreign-UID listener and exercise the kernel's connected-peer credentials.
func TestLocalIPCKernelRejectsOtherUID(t *testing.T) {
	if os.Getenv("DIBS_Q_CI") != "1" {
		t.Skip("real second-user fixture runs in the explicit Linux CI step")
	}
	if os.Geteuid() != 0 {
		t.Fatal("setup: second-user fixture requires the isolated root CI invocation")
	}
	name := fmt.Sprintf("dibs-q-%d", os.Getpid())
	if out, err := exec.Command("/usr/sbin/useradd", "--system", "--user-group", "--no-create-home", "--shell", "/usr/sbin/nologin", name).CombinedOutput(); err != nil {
		t.Fatalf("create second fixture user: %v %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("/usr/sbin/userdel", name).CombinedOutput(); err != nil {
			t.Errorf("retire fixture user: %v %s", err, out)
		}
	})
	ids := func(flag string) uint32 {
		t.Helper()
		out, err := exec.Command("id", flag, name).Output()
		if err != nil {
			t.Fatal(err)
		}
		id, err := strconv.ParseUint(string(bytesTrim(out)), 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		return uint32(id)
	}
	uid, gid := ids("-u"), ids("-g")
	if uid == uint32(os.Geteuid()) {
		t.Fatal("setup: foreign account has the client's UID")
	} // #nosec G115 -- euid is nonnegative.
	bin := qBuildCLI(t)
	dir := qShortDir(t)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte(qFixtureSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	// A copied test image in an explicitly public fixture directory, rather
	// than weakening the Go build tree or the runner's home to let the child in.
	execDir := qShortDir(t)
	if err := os.Chmod(execDir, 0o755); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(execDir, "listener.test")
	src, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.OpenFile(image, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		_ = src.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dst, src)
	_ = src.Close()
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy fixture image: %v %v", copyErr, closeErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, image, "-test.run=^TestQOtherUIDListener$")
	cmd.Env = []string{"DIBS_Q_FOREIGN_CHILD=1", "DIBS_Q_SOCKET_DIR=" + dir}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{gid}}}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	decoder := json.NewDecoder(bufio.NewReader(out))
	var ready struct {
		Ready bool
		Addr  string
		UID   uint32
	}
	if err := decoder.Decode(&ready); err != nil || !ready.Ready || ready.UID != uid {
		t.Fatalf("setup: foreign listener did not start under the named user: %v %+v", err, ready)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Case1: genuine foreign-owned socket, in the now owner-private profile.
	_, _ = qClient(t, bin, dir, ready.Addr, "", "board", "--json")
	// Case2: metadata says this user, but the connected listener's kernel UID
	// is still foreign. This drives the peer check beyond the filesystem guard.
	if err := os.Chown(filepath.Join(dir, "dibd.sock"), os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	_, _ = qClient(t, bin, dir, ready.Addr, "", "board", "--json")
	rpc := `{"jsonrpc":"2.0","id":7,"method":"server/discover","params":{}}` + "\n"
	_, _ = qClient(t, bin, dir, ready.Addr, rpc, "mcp-stdio")
	if err := in.Close(); err != nil {
		t.Fatal(err)
	}
	var counts struct{ Requests, Accepts int }
	if err := decoder.Decode(&counts); err != nil {
		t.Fatalf("foreign listener counts: %v", err)
	}
	if counts.Requests != 0 {
		t.Fatalf("foreign UID listener received %d HTTP requests; kernel peer check failed", counts.Requests)
	}
	if counts.Accepts < 2 {
		t.Fatalf("setup: CLI and stdio did not reach the foreign listener's kernel peer check: %d accepts", counts.Accepts)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	qCheckPOSIXACLMask(t, uid, gid, image)
	qCheckForeignDirectoryStartup(t, uid, gid, bin)
}

func qCheckPOSIXACLMask(t *testing.T, uid, gid uint32, image string) {
	t.Helper()
	dir := qShortDir(t)
	entry := "u:" + strconv.FormatUint(uint64(uid), 10) + ":rwx,m::---"
	if out, err := exec.Command("setfacl", "-m", entry, dir).CombinedOutput(); err != nil {
		t.Fatalf("setup: real named-user POSIX ACL: %v %s", err, out)
	}
	listing, err := exec.Command("getfacl", "-n", "-c", dir).Output()
	if err != nil || !strings.Contains(string(listing), "mask::---") ||
		!strings.Contains(string(listing), "user:"+strconv.FormatUint(uint64(uid), 10)+":rwx") {
		t.Fatalf("setup: ACL is not the intended named grant with a zero mask: %v %s", err, listing)
	}
	if err := localtransport.CheckDir(dir, false); err != nil {
		t.Fatalf("zero POSIX ACL mask was not private: %v", err)
	}
	qACLWriteAsOtherUser(t, uid, gid, image, dir, false)
	// On Linux the group mode bits ARE the POSIX ACL mask. The same named
	// rwx entry becomes effective when chmod exposes that mask.
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	qACLWriteAsOtherUser(t, uid, gid, image, dir, true)
	if err := localtransport.CheckDir(dir, false); err == nil {
		t.Fatal("effective named-user ACL write access passed private-profile admission")
	}
}

func qACLWriteAsOtherUser(t *testing.T, uid, gid uint32, image, dir string, allow bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, image, "-test.run=^TestQOtherUIDACLWrite$")
	cmd.Env = []string{
		"DIBS_Q_ACL_CHILD=1", "DIBS_Q_ACL_DIR=" + dir,
		"DIBS_Q_ACL_ALLOW=" + strconv.FormatBool(allow),
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{gid}},
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kernel ACL write control allow=%v: %v %s", allow, err, out)
	}
}

func TestQOtherUIDACLWrite(t *testing.T) {
	if os.Getenv("DIBS_Q_ACL_CHILD") != "1" {
		return
	}
	path := filepath.Join(os.Getenv("DIBS_Q_ACL_DIR"), "acl-write-proof")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("DIBS_Q_ACL_ALLOW") == "true" {
		if err != nil {
			t.Fatalf("effective named-user grant did not permit the real write: %v", err)
		}
	} else if !os.IsPermission(err) {
		t.Fatalf("zero mask did not deny the real other-user write: %v", err)
	}
}

func qCheckForeignDirectoryStartup(t *testing.T, uid, gid uint32, bin string) {
	t.Helper()
	dir := qShortDir(t)
	// Linux release/test targets are 64-bit; these parsed uint32 IDs fit int.
	if err := os.Chown(dir, int(uid), int(gid)); err != nil { // #nosec G115 -- bounded OS IDs on supported targets.
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQDaemonChild$")
	port := testport.Reserve(t, "tcp", "127.0.0.1:0")
	cmd.Env = append(nativeProbeEnv(t.TempDir(), "", ""),
		"DIBS_Q_DAEMON_CHILD=1", "DIBS_DIR="+dir, "DIBS_ADDR="+port.Addr)
	port.Release(t)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil || err == nil || !strings.Contains(string(out), "run without sudo") ||
		!strings.Contains(string(out), dir) {
		t.Fatalf("foreign-owned startup lost its literal ownership repair: %v %s", err, out)
	}
	client := exec.CommandContext(ctx, bin, "board", "--json")
	client.Env = append(nativeProbeEnv(t.TempDir(), "", ""),
		"DIBS_DIR="+dir, "DIBS_ADDR="+port.Addr, "DIBS_TRANSPORT=local")
	// CLI errors are stderr, including with --json. The normal qClient helper
	// intentionally returns only parseable stdout and would hide this hint.
	clientOut, clientErr := client.CombinedOutput()
	if ctx.Err() != nil || clientErr == nil || !strings.Contains(string(clientOut), "run without sudo") {
		t.Fatalf("root client lost its pre-dial sudo corrective: %v %s", clientErr, clientOut)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Sys().(*syscall.Stat_t).Uid != uid || info.Mode().Perm() != 0o700 {
		t.Fatalf("refusal changed the foreign-owned directory: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "local.secret")); !os.IsNotExist(err) {
		t.Fatalf("foreign-owned refusal created a credential: %v", err)
	}
}

func bytesTrim(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
