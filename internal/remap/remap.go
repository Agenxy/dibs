// Package remap is how Dibs gives a board a name a person can type.
//
// Remap (github.com/Agenxy/remap) is the Agenxy name plane: it maps any
// hostname a user chooses to an address or an HTTP service on their own
// machines, through a per-user daemon that owns the registry and a local
// gateway that routes by name. Dibs does none of that itself. What a board
// needs from it is one thing: `http://<name>/` reaching the board, so that
// the link `dibs web` prints and the address a person remembers are a word
// rather than a port. This package is the whole of Dibs's dependency on it:
// the `remap` binary, spoken to over argv with `--json`, whose envelope is
// versioned (`remap.cli/v1`) and checked here.
//
// Nothing here is required. A board without Remap is reached by its address,
// as it always was; a board with a name in dibs.toml and Remap present is
// also reached by the name, and doctor says when the two disagree.
package remap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/sibling"
)

// Command is the executable consulted; a test points it at a stand-in.
var Command = "remap"

const (
	callTimeout = 5 * time.Second
	maxOutput   = 1 << 20
	schema      = "remap.cli/v1"
)

// Mapping is one name Remap routes.
type Mapping struct {
	Pattern    string `json:"pattern"`
	Target     string `json:"target"`
	TargetKind string `json:"target_kind"`
	Enabled    bool   `json:"enabled"`
}

// ErrNotInstalled is the answer when no `remap` is on this machine.
var ErrNotInstalled = errors.New("remap is not installed on this machine")

// Error is what Remap itself said went wrong: its stable code, its sentence,
// and the hint it gives, verbatim, because its words are better than a
// paraphrase and its codes are what its documentation indexes.
type Error struct {
	Code, Message, Hint string
}

func (e *Error) Error() string {
	if e.Hint != "" {
		return "remap: " + e.Message + " (" + e.Hint + ")"
	}
	return "remap: " + e.Message
}

// Available reports whether the binary is on this machine at all; Status
// answers whether its daemon is.
func Available() bool { return executable() != "" }

// executable is where the binary is: Command when it names a path or is on
// PATH, else where the Agenxy installers put it (internal/sibling), which a
// service's PATH does not list.
func executable() string {
	if strings.ContainsRune(Command, os.PathSeparator) {
		if _, err := os.Stat(Command); err == nil {
			return Command
		}
		return ""
	}
	return sibling.Find(Command)
}

// Status is the registry's revision, which is also the proof that Remap's
// daemon is answering.
func Status(ctx context.Context) (revision uint64, err error) {
	var out struct {
		Revision      uint64 `json:"revision"`
		SchemaVersion uint64 `json:"schema_version"`
	}
	if err := call(ctx, "status", &out, "status"); err != nil {
		return 0, err
	}
	if out.SchemaVersion == 0 {
		return 0, errors.New("remap status answered without a registry: not a status")
	}
	return out.Revision, nil
}

// Get is the mapping for one exact name, or nil when Remap has none.
func Get(ctx context.Context, name string) (*Mapping, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	var m *Mapping
	if err := call(ctx, "mapping", &m, "get", name); err != nil {
		if errors.Is(err, errNoPayload) {
			return nil, nil // the one command whose answer may be empty
		}
		return nil, err
	}
	return m, nil
}

// Set maps name to target, creating or retargeting it in one revision-checked
// transaction on Remap's side.
func Set(ctx context.Context, name, target string) error {
	if err := checkName(name); err != nil {
		return err
	}
	if err := checkTarget(target); err != nil {
		return err
	}
	// Success is the mapping Remap says it now holds, and it must be the one
	// asked for: an ok envelope around nothing, or around some other
	// mapping, is not a registration.
	var m Mapping
	if err := call(ctx, "mapping", &m, "set", name, target); err != nil {
		return err
	}
	if !strings.EqualFold(m.Pattern, name) || strings.TrimSuffix(m.Target, "/") != strings.TrimSuffix(target, "/") {
		return fmt.Errorf("remap set answered with a mapping for %q -> %q, not the %q -> %q asked for",
			m.Pattern, m.Target, name, target)
	}
	if !m.Routes() {
		return fmt.Errorf("remap set answered with a mapping that does not route: enabled=%v kind=%q",
			m.Enabled, m.TargetKind)
	}
	return nil
}

// Routes reports a mapping Remap's gateway serves: enabled, and an HTTP
// upstream, which is what a board is. A disabled mapping or one of another
// kind is on the books and reaches nothing.
func (m *Mapping) Routes() bool {
	return m != nil && m.Enabled && m.TargetKind == "http"
}

// errNoPayload is an ok envelope carrying no result: Get's "no such mapping",
// and a refusal for every other command.
var errNoPayload = errors.New("remap answered ok with no result")

// maxNameBytes and maxTargetBytes bound what reaches argv: a hostname is at
// most 253 bytes, and an upstream URL for a board is a scheme, a host and a
// port. Remap's own validation is the authority on the rest.
const (
	maxNameBytes   = 253
	maxTargetBytes = 512
)

// checkName refuses what could read as a flag or is not a hostname shape.
func checkName(name string) error {
	if name == "" || len(name) > maxNameBytes || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " /:") {
		return fmt.Errorf("remap: %q is not a name", name)
	}
	return nil
}

// checkTarget refuses what could read as a flag or is not a URL-sized value.
func checkTarget(target string) error {
	flagLike := strings.HasPrefix(target, "-") || strings.ContainsAny(target, " \n")
	if target == "" || len(target) > maxTargetBytes || flagLike {
		return fmt.Errorf("remap: %q is not a target", target)
	}
	return nil
}

// envelope is what every Remap answer carries.
type envelope struct {
	Schema string `json:"schema"`
	OK     bool   `json:"ok"`
	Result struct {
		Kind  string          `json:"kind"`
		Value json.RawMessage `json:"value"`
	} `json:"result"`
	Err *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	} `json:"error"`
}

func call(ctx context.Context, kind string, into any, args ...string) error {
	exe := executable()
	if exe == "" {
		return ErrNotInstalled
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	// #nosec G204 -- argv, no shell: a fixed executable, Remap's own
	// subcommands, and a name and target refused above when they could
	// read as flags.
	cmd := exec.CommandContext(ctx, exe, append([]string{"--json"}, args...)...)
	out, errOut := &limitedWriter{left: maxOutput}, &limitedWriter{left: 4096}
	cmd.Stdout, cmd.Stderr = out, errOut
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil || errors.Is(err, exec.ErrWaitDelay) {
		return fmt.Errorf("remap %s did not finish within %s: %w", args[0], callTimeout, err)
	}
	if out.truncated {
		return fmt.Errorf("remap %s answered with more than %d bytes, which is not an answer", args[0], maxOutput)
	}
	var e envelope
	if !json.Valid(out.Bytes()) || json.Unmarshal(out.Bytes(), &e) != nil {
		if err != nil {
			return fmt.Errorf("remap %s: %w: %s", args[0], err, strings.TrimSpace(errOut.String()))
		}
		return fmt.Errorf("remap %s did not answer in JSON", args[0])
	}
	return readEnvelope(args[0], kind, into, e, err)
}

// readEnvelope turns Remap's answer into this command's result: its own
// refusal verbatim, a wrong kind or a run that failed after answering
// refused, and the payload decoded into what the command expects.
func readEnvelope(command, kind string, into any, e envelope, runErr error) error {
	if e.Schema != schema {
		return fmt.Errorf("remap answered with schema %q, and this build of dibs reads %q: update whichever is older",
			e.Schema, schema)
	}
	if !e.OK {
		if e.Err == nil {
			return fmt.Errorf("remap %s refused without saying why", command)
		}
		return &Error{Code: e.Err.Code, Message: e.Err.Message, Hint: e.Err.Hint}
	}
	if runErr != nil {
		return fmt.Errorf("remap %s answered ok and then failed: %w", command, runErr)
	}
	if e.Result.Kind != kind {
		return fmt.Errorf("remap %s answered with a %q result, and this command's answer is a %q",
			command, e.Result.Kind, kind)
	}
	if len(e.Result.Value) == 0 || string(e.Result.Value) == "null" {
		return errNoPayload
	}
	if jerr := json.Unmarshal(e.Result.Value, into); jerr != nil {
		return fmt.Errorf("remap %s answered in a shape this build does not read: %w", command, jerr)
	}
	return nil
}

type limitedWriter struct {
	bytes.Buffer
	left      int
	truncated bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if n > l.left {
		l.truncated = true
		p = p[:l.left]
	}
	l.left -= len(p)
	l.Buffer.Write(p)
	return n, nil
}
