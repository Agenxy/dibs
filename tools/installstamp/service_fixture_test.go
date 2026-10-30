// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Only the external ServiceManagement execution boundary is overlaid. The real
// clean-clone Taskfile, Go version computation, stamp guard, copy and signing
// steps still run. No test-only service bypass is shipped in the product.
func isolatedServiceInstall(t *testing.T, clone, scratch, dest string) string {
	t.Helper()
	home := filepath.Join(scratch, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"GOCACHE", "GOMODCACHE"} {
		if os.Getenv(key) != "" {
			continue
		}
		out, err := exec.Command("go", "env", key).Output()
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, strings.TrimSpace(string(out)))
	}
	if runtime.GOOS != "darwin" {
		return ""
	}
	// Prove the fixture's process fence before any install can invoke a helper.
	for _, deny := range []bool{false, true} {
		policy := "(version 1)(allow default)"
		if deny {
			policy += `(deny process-exec (literal "/usr/bin/true"))`
		}
		allowed := exec.Command("/usr/bin/sandbox-exec", "-p", policy, "/bin/echo", "fixture")
		if err := allowed.Run(); err != nil {
			t.Fatalf("service fence rejected its allowed control: %v", err)
		}
		cmd := exec.Command("/usr/bin/sandbox-exec", "-p", policy, "/usr/bin/true")
		err := cmd.Run()
		if (err != nil) != deny {
			t.Fatalf("service execution fence control deny=%t: %v", deny, err)
		}
	}
	trace := filepath.Join(scratch, "service-trace")
	reads := filepath.Join(scratch, "read-trace")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/board":
			_, _ = w.Write([]byte(`{"node":"install-stamp-fixture","serial":0,"agents":[]}`))
		case "/api/folder-access/request":
			if r.Method != http.MethodPost || r.Header.Get("X-Dibs-Local") != strings.Repeat("a", 64) {
				t.Error("unverified folder request")
			}
			f, err := os.OpenFile(reads, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			_, err = f.WriteString("request\n")
			_ = f.Close()
			if err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"requested":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	original := filepath.Join(clone, "cmd", "dibs", "appservice_command.go")
	canonical, err := filepath.EvalSymlinks(original)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(original)
	if err != nil || !strings.Contains(string(source), "var executeAppServiceCommand") {
		t.Fatal("clone lacks the isolated service execution boundary", err)
	}
	fixture := strings.NewReplacer("__DEST__", strconv.Quote(dest), "__HOME__", strconv.Quote(home), "__BOARD__", strconv.Quote(filepath.Join(scratch, "board")), "__ADDR__", strconv.Quote(server.Listener.Addr().String()), "__TRACE__", strconv.Quote(trace)).Replace(installServiceFixture)
	replacement := filepath.Join(scratch, "appservice_command.go")
	if err := os.WriteFile(replacement, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{original: replacement, canonical: replacement}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(scratch, "service-overlay.json")
	if err := os.WriteFile(path, overlay, 0o600); err != nil {
		t.Fatal(err)
	}
	return strconv.Quote("-overlay=" + path)
}

func assertPrivateServiceInstall(t *testing.T, scratch string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}
	trace, err := os.ReadFile(filepath.Join(scratch, "service-trace"))
	if err != nil || strings.Count(string(trace), "--service-register\n") != 1 {
		t.Fatalf("real install did not reach private service fixture once: %s %v", trace, err)
	}
	reads, err := os.ReadFile(filepath.Join(scratch, "read-trace"))
	if err != nil || string(reads) != "request\n" {
		t.Fatalf("real install did not request private roots once: %s %v", reads, err)
	}
}

const installServiceFixture = `package main
import (
 "fmt"
 "os"
 "os/exec"
 "path/filepath"
 "strings"
 "net/http"
 "net/http/httputil"
 "net/url"
 "time"
 "github.com/agenxy/dibs/internal/localtransport"
 "github.com/agenxy/dibs/internal/paths"
)
var executeAppServiceCommand = func(cmd *exec.Cmd) ([]byte,error) {
 if os.Getenv("HOME")!=__HOME__ || os.Getenv("DIBS_DIR")!=__BOARD__ || cmd.Path!=filepath.Join(__DEST__,"Dibs.app","Contents","MacOS","dibs-notify") || len(cmd.Args)!=2 {
  return nil,fmt.Errorf("service install fixture escaped its private scope")
 }
 f,err:=os.OpenFile(__TRACE__,os.O_CREATE|os.O_APPEND|os.O_WRONLY,0o600)
 if err!=nil {return nil,err}
 _,err=f.WriteString(cmd.Args[1]+"\n");_=f.Close();if err!=nil {return nil,err}
 switch cmd.Args[1] {
 case "--service-status","--service-unregister": return []byte("{\"status\":\"not-registered\"}"),nil
 case "--service-register":
  selected,found,err:=paths.PinnedService()
  if err!=nil || !found || selected.Dir!=__BOARD__ {return nil,fmt.Errorf("private pin missing before registration")}
  if info,err:=os.Stat(selected.Dir);err!=nil || !info.IsDir() {return nil,fmt.Errorf("private board missing before registration")}
  for name,body:=range map[string]string{"local.secret":strings.Repeat("a",64),"node_id":"install-stamp-fixture","dibs.toml":"addr = '"+__ADDR__+"'\n"} {
   if err:=os.WriteFile(filepath.Join(selected.Dir,name),[]byte(body),0o600);err!=nil {return nil,err}
  }
  // The production client authenticates this actual private Unix listener.
  // Only the fixture daemon's handler is forwarded to the parent's test server.
  ln,err:=localtransport.Listen(selected.Dir);if err!=nil {return nil,err}
  target,err:=url.Parse("http://"+__ADDR__);if err!=nil {return nil,err}
  srv:=&http.Server{Handler:httputil.NewSingleHostReverseProxy(target),ConnContext:localtransport.ConnContext,ReadHeaderTimeout:time.Second}
  go func(){_=srv.Serve(ln)}()
  return []byte("{\"status\":\"enabled\"}"),nil
 default:return nil,fmt.Errorf("unexpected service mode %s",cmd.Args[1])
 }
}
`

func privateInstallTask(ctx context.Context, clone string, env []string, dest string) *exec.Cmd {
	args := []string{"install", "DEST=" + dest}
	cmd := exec.CommandContext(ctx, "task", args...)
	if runtime.GOOS == "darwin" {
		parent, err := filepath.EvalSymlinks(filepath.Dir(dest))
		if err != nil {
			panic(err)
		}
		app := filepath.Join(dest, "Dibs.app")
		canonical := filepath.Join(parent, filepath.Base(dest), "Dibs.app")
		policy := fmt.Sprintf("(version 1)(allow default)(deny process-exec (subpath %s) (subpath %s))", strconv.Quote(app), strconv.Quote(canonical))
		cmd = exec.CommandContext(ctx, "/usr/bin/sandbox-exec", append([]string{"-p", policy, "task"}, args...)...)
	}
	cmd.Dir, cmd.Env = clone, env
	return cmd
}
