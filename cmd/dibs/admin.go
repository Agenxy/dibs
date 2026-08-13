package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/adminpw"
	"github.com/agenxy/dibs/internal/paths"
)

func adminHashPath() string { return filepath.Join(paths.DataDir(), "admin.hash") }

// readPassword reads a line from the terminal with echo disabled (via stty,
// no dependency). Falls back to a visible read when stdin is not a TTY.
func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	setEcho := func(on string) {
		// #nosec G204 -- no shell: exec.Command passes argv directly, so a path
		// cannot inject arguments. The value is an operator-supplied directory,
		// never agent input.
		c := exec.Command("stty", on)
		c.Stdin = os.Stdin
		_ = c.Run()
	}
	setEcho("-echo")
	defer func() { setEcho("echo"); fmt.Fprintln(os.Stderr) }()
	// Read byte-by-byte so a shared/piped stdin isn't over-buffered across
	// successive prompts (a bufio.Reader would swallow the next line).
	var b []byte
	one := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				break
			}
			if one[0] != '\r' {
				b = append(b, one[0])
			}
		}
		if err != nil {
			if len(b) > 0 {
				break
			}
			return "", err
		}
	}
	return string(b), nil
}

// adminCmd handles `dibs admin <sub>`.
func adminCmd(args []string) error {
	if len(args) == 0 {
		fmt.Println(adminUsage)
		return nil
	}
	switch args[0] {
	case "set-password":
		return adminOnly("admin set-password", setAdminPassword)
	case "prune":
		agent := ""
		if len(args) > 1 {
			agent = args[1]
		}
		return adminOnly("admin prune", func() error { return pruneAgents(agent) })
	case "repair-ledger":
		// Not adminOnly's password gate: the daemon cannot start, so there is no
		// board to authenticate against. The file is the operator's own, this
		// runs on their machine, and it archives before it changes anything.
		return repairLedger(args[1:])
	case "coordinator", "member", "admin":
		if len(args) < 2 {
			// Exit non-zero, not 0. This printed usage and reported success, so
			// `dibs admin coordinator $LANE && echo granted` with $LANE unset
			// printed "granted" and granted nothing, and the next
			// coordinator-gated call failed with no reason to suspect the grant.
			// A misspelt agent NAME already fails loudly with E_NO_AGENT, so a
			// missing one must too.
			return fmt.Errorf("`dibs admin %s` needs an agent name: dibs admin %s <agent>\n"+
				"  `dibs board` lists the agents on this board", args[0], args[0])
		}
		role, agent := args[0], args[1]
		return adminOnly("admin "+role, func() error { return setAgentRole(agent, role) })
	default:
		// A mistyped verb granted nothing and exited 0, which reads as done.
		// Bare `dibs admin` is a genuine request for the list and still prints
		// to stdout at exit 0; only a WRONG word is an error.
		fmt.Println(adminUsage)
		return fmt.Errorf("unknown admin command %q", args[0])
	}
}

const adminUsage = `usage:
  dibs admin set-password         set the password gating the board and decrypted mail
  dibs admin repair-ledger        recover a board whose ledger will not replay
  dibs admin coordinator <agent>   promote: may broadcast and force-release claims
  dibs admin admin <agent>         promote: everything you can do, INCLUDING reading all mail
  dibs admin member <agent>        demote back to a plain member
  dibs admin prune [agent]         close finished agents; no argument clears every
                                   agent that is not live (a crashed agent cannot
                                   close itself, so only you can clear it)

A coordinator gets breadth, not intrusion: it can address the whole fleet and
unstick shared resources, but it still cannot read another agent's mail.
An admin gets the god view, mail included: grant it only to an agent you trust
as you trust yourself. Either way, only a human can grant it: agents can never
promote themselves.`

// setAgentRole calls the god-view admin endpoint, which requires the local
// secret AND the admin password. That is the whole point: promotion is a human
// decision, so it travels the human's path.
func pruneAgents(agent string) error {
	pass, err := promptAdminForGodView()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"agent": agent})
	req, err := http.NewRequest(http.MethodPost, "http://"+addr()+"/api/admin/prune", bytes.NewReader(body))
	if err != nil {
		return err
	}
	if s, serr := localSecret(); serr == nil {
		req.Header.Set("X-Dibs-Local", s)
	}
	req.Header.Set("X-Dibs-Admin", pass)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w (is dibd running?)", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Pruned []string `json:"pruned"`
		Count  int      `json:"count"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Count == 0 {
		fmt.Println("nothing to prune: every agent is live.")
		return nil
	}
	fmt.Printf("closed %d agent(s): %s\n", out.Count, strings.Join(out.Pruned, ", "))
	return nil
}

func setAgentRole(agent, role string) error {
	pass, err := promptAdminForGodView()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"agent": agent, "role": role})
	req, err := http.NewRequest(http.MethodPost, "http://"+addr()+"/api/admin/role", bytes.NewReader(body))
	if err != nil {
		return err
	}
	if s, serr := localSecret(); serr == nil {
		req.Header.Set("X-Dibs-Local", s)
	}
	req.Header.Set("X-Dibs-Admin", pass)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w (is dibd running?)", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	fmt.Printf("%s is now a %s\n", agent, role)
	return nil
}

// confirmDirIsTheAddressedDaemon refuses to write install state into a data
// directory that does not belong to the daemon this command is talking to.
//
// `dibs admin prune` and `dibs admin role` act on the daemon at DIBS_ADDR.
// `set-password` writes a FILE, and resolved that file from DIBS_DIR: so
// setting only the address, which is the natural thing to do when you are
// pointed at a second daemon, silently rewrote the credentials of the first.
// Two halves of one command family acting on two different installs, with
// nothing on screen to say so. It printed the path it wrote, and the path was
// correct, and it was still the wrong install.
//
// The local secret is the install's identity, so the check is exact: offer the
// directory's secret to the addressed daemon. If it says no, they are not the
// same install. If nothing is listening we proceed: setting a password before
// the first start is legitimate, and is how installs begin.
func confirmDirIsTheAddressedDaemon() error {
	secret, err := localSecret()
	if err != nil {
		return nil // no directory yet; nothing to disagree with
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+addr()+"/healthz", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return nil // nothing listening; an offline set-password is fine
	}
	defer func() { _ = resp.Body.Close() }()
	// Anything that is not a refusal means the secret was accepted, so the
	// directory is this daemon's. The real endpoint answers 404: authenticated,
	// no such route, and reading that as a rejection would block every
	// legitimate run.
	if resp.StatusCode != http.StatusUnauthorized {
		return nil
	}
	// Printed rather than wrapped into the error: the operator needs the whole
	// explanation, and an error string is conventionally one line.
	fmt.Fprintf(os.Stderr,
		"\nThis command writes a file into the data directory, while `admin prune` and\n"+
			"`admin role` act on the address, so with only DIBS_ADDR set you would be\n"+
			"changing the password of a DIFFERENT install than the one you are pointed at.\n"+
			"Set DIBS_DIR to %s's data directory as well, and run it again.\n\n", addr())
	return fmt.Errorf("refusing to write: %s does not belong to the daemon at %s (set DIBS_DIR too)",
		paths.DataDir(), addr())
}

func setAdminPassword() error {
	if err := confirmDirIsTheAddressedDaemon(); err != nil {
		return err
	}
	p1, err := readPassword("Set Dibs admin password (gates the board / decrypted mail): ")
	if err != nil {
		return err
	}
	if len(p1) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	p2, err := readPassword("Confirm: ")
	if err != nil {
		return err
	}
	if p1 != p2 {
		return fmt.Errorf("passwords do not match")
	}
	hash, err := adminpw.Hash(p1)
	if err != nil {
		return err
	}
	path := adminHashPath()
	if err := os.WriteFile(path, []byte(hash+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Printf("admin password set (%s). The board now requires it; agents' coordination access is unaffected.\n", path)
	return nil
}

// promptAdminForGodView returns the admin password to attach to a god-view
// request, erroring early with guidance if none is configured.
func promptAdminForGodView() (string, error) {
	if _, err := os.Stat(adminHashPath()); err != nil {
		return "", fmt.Errorf("no admin password set: run `dibs admin set-password` first")
	}
	return readPassword("Dibs admin password: ")
}
