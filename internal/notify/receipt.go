package notify

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

// Receipt reports OS acceptance (posted) or an explicit dismissal. Posted
// does not establish that a banner was visible or that a person saw it.
type Receipt func(state string)

// A private 0700 per-invocation directory below the data directory makes
// the receipt protocol additive: an older helper
// ignores the environment variable and supplies no receipt. It also survives
// launchctl asuser, which does not carry the helper's stdout back to us.
func outputWithReceipt(cmd *exec.Cmd, receipt Receipt) ([]byte, error) {
	if receipt == nil {
		return cmd.Output()
	}
	base := paths.DataDir()
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(base, "notification-receipt-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// OpenRoot confines even symlinks to this private 0700 invocation directory.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile("receipt", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	name := filepath.Join(dir, "receipt")
	_ = f.Close()
	defer func() { _ = os.Remove(name) }()
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, "DIBS_NOTIFY_RECEIPT="+name)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		last := ""
		read := func() {
			b, readErr := root.ReadFile("receipt") // confined to the private directory
			var r struct {
				State string `json:"state"`
			}
			if readErr == nil && json.Unmarshal(b, &r) == nil &&
				(r.State == "posted" || r.State == "dismissed") && r.State != last {
				last = r.State
				receipt(r.State)
			}
		}
		for {
			select {
			case <-tick.C:
				read()
			case <-stop:
				read()
				return
			}
		}
	}()
	out, err := cmd.Output()
	close(stop)
	<-done
	return out, err
}
