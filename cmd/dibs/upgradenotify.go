package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/agenxy/dibs/internal/notify"
)

// Off the daemon writer, and permission-free. Success moving the board says
// nothing about the helper retaining its grant across signing identities.
func reportUpgradeNotifications() {
	if runtime.GOOS != "darwin" {
		return
	}
	ok, why, settings := notify.ReachWithSettings()
	if settings == nil || settings.AuthorizationStatus == "unknown" {
		fmt.Println("warning: notification authorization is unknown after upgrade. Run `dibs doctor` to inspect the installed notifier; posting and daemon version equality do not prove permission.")
		return
	}
	if !ok {
		fmt.Printf("warning: notification authorization after upgrade: %s. %s An install can change the helper's signing identity and lose a prior grant; this does not mean you denied a prompt. Run `dibs doctor` before relying on human approval notifications.\n", settings.AuthorizationStatus, why)
		return
	}
	if hint := settings.Hints(true); hint != "" {
		fmt.Println("note: " + hint)
	}
}

func registerInstalledNotifier(into string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	app := filepath.Join(into, "Dibs.app")
	if _, err := os.Stat(app); err != nil {
		if os.IsNotExist(err) {
			// Old archives carried no helper. The subsequent settings probe
			// reports unknown; do not make that legacy payload un-installable.
			return nil
		}
		return fmt.Errorf("installed notifier %s: %w", app, err)
	}
	const registrar = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, registrar, "-f", app).CombinedOutput() // #nosec G204 -- fixed registrar and verified installed bundle, no shell
	if err != nil {
		return fmt.Errorf("registering %s: %w: %s", app, err, out)
	}
	return nil
}
