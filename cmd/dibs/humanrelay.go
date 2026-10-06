package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/humankey"
	"github.com/agenxy/dibs/internal/notify"
	"github.com/agenxy/dibs/internal/paths"
)

// `dibs human-relay`: the person's own Mac, attached to a board that runs
// somewhere else. Mail to the person shows up HERE, as the same banners,
// alerts and questions a board raises on its own screen, and the answers go
// back signed by a key in this Mac's Secure Enclave. docs/NETWORK.md §8.
//
// It never holds the board's secret and never needs it: every agent holds
// that, so it proves nothing about a person.

const (
	relayKeyFile   = "human-relay.key"
	relayStateFile = "human-relay.json"
	keyHelperName  = "dibs-presence" // its --key commands hold the Secure Enclave key
)

// relayState is what enrolment leaves behind for the relay to run with.
type relayState struct {
	Key   string `json:"key"`
	Node  string `json:"node"`
	Board string `json:"board"`
}

// signer is the Secure Enclave key, behind an interface so a test can use a
// software key and no finger.
type signer interface {
	Create() (publicKey string, err error)
	Sign(msg []byte, reason string) (signature string, err error)
}

// enclaveKey drives `dibs-presence --key`, which sits beside this binary.
type enclaveKey struct{ helper, file string }

func newEnclaveKey(dir string) (*enclaveKey, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = resolved
	}
	helper := filepath.Join(filepath.Dir(self), keyHelperName)
	if _, err := os.Stat(helper); err != nil {
		return nil, fmt.Errorf("%s is not installed beside %s: the human relay needs it, and it "+
			"is built on macOS only, because the Secure Enclave is", keyHelperName, self)
	}
	return &enclaveKey{helper: helper, file: filepath.Join(dir, relayKeyFile)}, nil
}

func (k *enclaveKey) run(stdin []byte, args ...string) (string, error) {
	// #nosec G204 -- no shell; the helper path is beside this binary and the
	// arguments are this program's own.
	cmd := exec.Command(k.helper, append([]string{"--key"}, args...)...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		why := strings.TrimSpace(errb.String())
		if ee.ExitCode() == 1 {
			return "", fmt.Errorf("%w: %s", errNotSigned, why)
		}
		return "", fmt.Errorf("%s --key %s: %s", keyHelperName, args[0], why)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// errNotSigned is a person declining, or not being there: never retried
// without them.
var errNotSigned = errors.New("not signed")

func (k *enclaveKey) Create() (string, error) { return k.run(nil, "create", k.file) }

func (k *enclaveKey) Sign(msg []byte, reason string) (string, error) {
	return k.run(msg, "sign", k.file, reason)
}

func humanRelay(args []string) error {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Println("usage: dibs human-relay [enroll [--label NAME]]")
		fmt.Println()
		fmt.Println("  Shows mail addressed to you on THIS Mac, from a board that runs")
		fmt.Println("  somewhere else, and sends your answers back signed by a key in this")
		fmt.Println("  Mac's Secure Enclave. Set DIBS_ADDR (and DIBS_DIR for its trust) as")
		fmt.Println("  `dibs mcp-config --board` printed.")
		fmt.Println()
		fmt.Println("  enroll   makes the key and registers it with the board, which asks for")
		fmt.Println("           the board's admin password once. Run it before the relay.")
		return nil
	}
	dir := paths.DataDir()
	key, err := newEnclaveKey(dir)
	if err != nil {
		return err
	}
	origin := boardOrigin()
	if len(args) > 0 && args[0] == "enroll" {
		label := hostID()
		if len(args) > 2 && args[1] == "--label" {
			label = args[2]
		}
		pw, err := readPassword("Admin password for the board at " + origin + ": ")
		if err != nil {
			return err
		}
		st, err := enrollRelay(daemonClient(30*time.Second), origin, key, pw, label)
		if err != nil {
			return err
		}
		if err := writeRelayState(dir, st); err != nil {
			return err
		}
		fmt.Printf("enrolled key %s with %s. Now run: dibs human-relay\n", st.Key, origin)
		return nil
	}
	if len(args) > 0 {
		return fmt.Errorf("`dibs human-relay` takes enroll or nothing, and %q is not either", args[0])
	}
	st, err := readRelayState(dir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	r := newRelay(origin, st, key, humanask.Ask)
	slog.Info("human relay starting", "board", origin, "key", st.Key)
	return r.follow(ctx)
}

func writeRelayState(dir string, st relayState) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(filepath.Join(dir, relayStateFile), b, 0o600)
}

func readRelayState(dir string) (relayState, error) {
	var st relayState
	b, err := os.ReadFile(filepath.Join(dir, relayStateFile)) // #nosec G304 -- this data directory's own file
	if err != nil {
		return st, fmt.Errorf("this Mac is not enrolled with a board: run `dibs human-relay enroll` first (%w)", err)
	}
	if err := json.Unmarshal(b, &st); err != nil || st.Key == "" {
		return st, fmt.Errorf("%s does not parse: run `dibs human-relay enroll` again", relayStateFile)
	}
	return st, nil
}

// postJSON sends v and decodes the answer into out, turning a refusal into an
// error that carries the board's own sentence and hint.
func postJSON(c *http.Client, url string, hdr map[string]string, v, out any) (int, error) {
	body, _ := json.Marshal(v)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, val := range hdr {
		req.Header.Set(k, val)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		var refusal struct{ Error, Hint string }
		_ = json.Unmarshal(raw, &refusal)
		msg := refusal.Error
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		if refusal.Hint != "" {
			msg += " (" + refusal.Hint + ")"
		}
		return resp.StatusCode, fmt.Errorf("the board answered HTTP %d: %s", resp.StatusCode, msg)
	}
	if out != nil {
		return resp.StatusCode, json.Unmarshal(raw, out)
	}
	return resp.StatusCode, nil
}

func enrollRelay(c *http.Client, origin string, key signer, password, label string) (relayState, error) {
	pub, err := key.Create()
	if err != nil {
		return relayState{}, err
	}
	var out struct{ Key, Node string }
	if _, err := postJSON(c, origin+"/api/human/enroll", map[string]string{"X-Dibs-Admin": password},
		map[string]string{"public_key": pub, "label": label}, &out); err != nil {
		return relayState{}, err
	}
	return relayState{Key: out.Key, Node: out.Node, Board: origin}, nil
}

type relay struct {
	origin string
	st     relayState
	key    signer
	ask    func(humanask.Message) (humanask.Answer, error)
	client *http.Client
	stream *http.Client

	mu      sync.Mutex
	session string
	busy    map[uint64]bool // being shown now, so a re-sent notice is not shown twice
	retry   time.Duration
}

func newRelay(origin string, st relayState, key signer, ask func(humanask.Message) (humanask.Answer, error)) *relay {
	return &relay{
		origin: origin, st: st, key: key, ask: ask,
		client: daemonClient(30 * time.Second), stream: daemonClient(0),
		busy: map[uint64]bool{}, retry: 5 * time.Second,
	}
}

// challenge asks the board for a nonce and signs msgFor(node, nonce) after a
// finger, returning the nonce, the signature and the node.
func (r *relay) challenge(msgFor func(node, nonce string) []byte, reason string) (nonce, sig string, err error) {
	var ch struct{ Nonce, Node string }
	if _, err := postJSON(r.client, r.origin+"/api/human/challenge", nil,
		map[string]string{"key": r.st.Key}, &ch); err != nil {
		return "", "", err
	}
	// The node the board names must be the one this Mac enrolled with: a
	// different board behind the same address is not the one this key trusts.
	if r.st.Node != "" && ch.Node != r.st.Node {
		return "", "", fmt.Errorf("the board at %s is node %s, but this Mac enrolled with node %s",
			r.origin, ch.Node, r.st.Node)
	}
	sig, err = r.key.Sign(msgFor(ch.Node, ch.Nonce), reason)
	return ch.Nonce, sig, err
}

// open starts a session: one Touch ID.
func (r *relay) open() error {
	nonce, sig, err := r.challenge(humankey.SessionMessage, "receive your Dibs messages on this Mac")
	if err != nil {
		return err
	}
	var out struct{ Session string }
	if _, err := postJSON(r.client, r.origin+"/api/human/session", nil, map[string]string{
		"key": r.st.Key, "nonce": nonce, "signature": sig,
	}, &out); err != nil {
		return err
	}
	r.mu.Lock()
	r.session = out.Session
	r.mu.Unlock()
	return nil
}

func (r *relay) bearer() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return map[string]string{"Authorization": "Bearer " + r.session}
}

// errSessionGone is the board no longer honouring this session: expired,
// revoked, or the board restarted.
var errSessionGone = errors.New("the relay session ended")

// follow keeps the relay attached until ctx ends. A dropped stream reuses the
// session; only a session the board refuses costs another Touch ID.
func (r *relay) follow(ctx context.Context) error {
	for ctx.Err() == nil {
		if r.bearer()["Authorization"] == "Bearer " {
			if err := r.open(); err != nil {
				if errors.Is(err, errNotSigned) {
					return fmt.Errorf("no session: %w. Run `dibs human-relay` again when you are at this Mac", err)
				}
				slog.Warn("could not open a relay session; trying again", "err", err)
				sleepCtx(ctx, r.retry)
				continue
			}
			slog.Info("attached: mail to you on this board will show on this Mac", "board", r.origin)
		}
		err := r.attach(ctx)
		if errors.Is(err, errSessionGone) {
			r.mu.Lock()
			r.session = ""
			r.mu.Unlock()
			continue
		}
		if ctx.Err() == nil {
			slog.Warn("the relay stream dropped; reattaching", "err", err)
			sleepCtx(ctx, r.retry)
		}
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (r *relay) attach(ctx context.Context) error {
	cleanupJobs, stopCleanup := r.startCleanupWorker(ctx)
	defer stopCleanup()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.origin+"/api/human/stream", nil)
	if err != nil {
		return err
	}
	for k, v := range r.bearer() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := r.stream.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return errSessionGone
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("the board answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if ok {
			r.receiveNotice(data, cleanupJobs)
		}
	}
	return sc.Err()
}

func (r *relay) startCleanupWorker(ctx context.Context) (chan<- *engine.NotificationCleanup, context.CancelFunc) {
	cleanupCtx, stopCleanup := context.WithCancel(ctx)
	cleanupJobs := make(chan *engine.NotificationCleanup, notify.CleanupBatch)
	go func() {
		for {
			select {
			case <-cleanupCtx.Done():
				return
			case batch := <-cleanupJobs:
				r.cleanupNotifications(batch)
			}
		}
	}()
	return cleanupJobs, stopCleanup
}

func (r *relay) receiveNotice(data string, cleanupJobs chan<- *engine.NotificationCleanup) {
	var n engine.HumanNotice
	if json.Unmarshal([]byte(data), &n) != nil {
		return
	}
	if n.Cleanup != nil {
		select {
		case cleanupJobs <- n.Cleanup:
		default:
			slog.Warn("notification cleanup queue full; removal is best effort")
		}
		return
	}
	if n.Serial == 0 {
		return
	}
	r.mu.Lock()
	showing := r.busy[n.Serial]
	r.busy[n.Serial] = true
	r.mu.Unlock()
	if !showing {
		go r.handle(n)
	}
}

// handle shows one notice and sends back whatever the person said.
func (r *relay) handle(n engine.HumanNotice) {
	defer func() {
		r.mu.Lock()
		delete(r.busy, n.Serial)
		r.mu.Unlock()
	}()
	// A link for the daemon's own Mac is not authority to open a thread on
	// another Mac. The relay compares machine identity before showing Open.
	if n.Contact != nil && n.Contact.OpenHost != hostID() {
		n.Contact.OpenURL = ""
	}
	a, err := r.ask(humanask.Message{
		Type: n.Type, Priority: n.Priority, From: n.From, FromName: n.FromName, Who: n.Who, Body: n.Body,
		Contact: n.Contact,
		Choices: n.Choices, Grant: n.Grant, Adopt: n.Adopt, AdoptName: n.AdoptName,
		Serial: n.Serial, Node: r.st.Node,
		Receipt: func(state string) {
			r.delivery(n.Serial, state, "")
		},
		DeliveryReceipt: func(data notify.ReceiptData) {
			r.deliveryReceipt(n.Serial, data, "")
		},
	})
	if err != nil {
		r.delivery(n.Serial, "failed", err.Error())
		slog.Warn("could not show a message on this Mac; it is still on the board", "msg", n.Serial, "err", err)
		return
	}
	if a.Disposition == "" {
		return
	}
	if err := r.answer(n, a); err != nil {
		slog.Warn("your answer did not reach the board; the message is still open there",
			"msg", n.Serial, "err", err)
	}
}

func (r *relay) delivery(serial uint64, state, failure string) {
	r.deliveryReceipt(serial, notify.ReceiptData{State: state}, failure)
}

func (r *relay) deliveryReceipt(serial uint64, data notify.ReceiptData, failure string) {
	data = data.Normalized()
	state := data.State
	client := *r.client
	client.Timeout = 3 * time.Second
	if len(failure) > 4096 {
		failure = failure[:4096]
	}
	_, err := postJSON(&client, r.origin+"/api/human/delivery", r.bearer(),
		map[string]any{
			"serial": serial, "state": state, "error": failure,
			"settings": data.Settings, "interruption_level": data.InterruptionLevel,
		}, nil)
	if err != nil {
		slog.Warn("notification receipt did not reach the board", "msg", serial, "state", state, "err", err)
	}
}

func (r *relay) cleanupNotifications(batch *engine.NotificationCleanup) {
	// The authenticated board may request cleanup only in the enrolled node's
	// namespace. RemoveMessages validates all IDs and caps the batch again.
	if batch.Node == "" || batch.Node != r.st.Node {
		return
	}
	result := notify.RemoveMessages(batch.Node, batch.Serials)
	for _, serial := range batch.Serials {
		r.delivery(serial, "cleanup_"+result.State, result.Error)
	}
}

func (r *relay) answer(n engine.HumanNotice, a humanask.Answer) error {
	body := map[string]any{"serial": n.Serial, "disposition": a.Disposition, "body": a.Body}
	// A grant needs a finger on THIS answer: the board refuses it otherwise.
	if n.Privileged() && a.Disposition == "approve" {
		from := n.From
		if n.FromName != "" {
			from = humanask.OneLine(n.FromName)
		}
		adopt := n.Adopt
		if n.AdoptName != "" && n.AdoptName != n.Adopt {
			adopt = humanask.OneLine(n.AdoptName) + " (formerly " + n.Adopt + ")"
		}
		reason := "Approve: " + strings.TrimPrefix(humanask.RequestTitle(from, n.Grant, adopt), "Dibs · ")
		nonce, sig, err := r.challenge(func(node, nonce string) []byte {
			return humankey.AnswerMessage(node, n.Serial, a.Disposition, nonce)
		}, reason)
		if err != nil {
			return err
		}
		body["nonce"], body["signature"] = nonce, sig
	}
	status, err := postJSON(r.client, r.origin+"/api/human/answer", r.bearer(), body, nil)
	if status == http.StatusUnauthorized && !n.Privileged() {
		// The session lapsed under a pending answer: one more Touch ID, once.
		if oerr := r.open(); oerr == nil {
			_, err = postJSON(r.client, r.origin+"/api/human/answer", r.bearer(), body, nil)
		}
	}
	return err
}
