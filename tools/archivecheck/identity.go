package main

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const notifierExecutable = "Dibs.app/Contents/MacOS/dibs-notify"

// Check relations on actual archived bytes, independently of the expected ID
// table. Updating both tables to the same wrong values must not restore green.
func coherentArchiveIdentity(archive string, have map[string]entry) error {
	helper, present := have[notifierExecutable]
	if !present {
		return nil // partial legacy fixtures; required runtime members are checked by carries
	}
	plist, present := have["Dibs.app/Contents/Info.plist"]
	if !present {
		return fmt.Errorf("%s: notifier Info.plist is missing; bundle/signature identity is unchecked", archive)
	}
	bundleID, err := archivedBundleID(plist.body)
	if err != nil {
		return fmt.Errorf("%s: notifier bundle identity: %w", archive, err)
	}
	helperID, err := signedIdentifier(archive, notifierExecutable, helper.body)
	if err != nil {
		return err
	}
	if helperID != bundleID {
		return fmt.Errorf("%s: actual notifier signature identifier %q differs from bundle identifier %q; "+
			"align them before packaging", archive, helperID, bundleID)
	}
	if daemon, present := have["dibd"]; present {
		daemonID, err := signedIdentifier(archive, "dibd", daemon.body)
		if err != nil {
			return err
		}
		if daemonID == helperID {
			return fmt.Errorf("%s: actual daemon and notifier share signed identifier %q; "+
				"give them distinct identities before packaging", archive, daemonID)
		}
	}
	return nil
}

func archivedBundleID(body []byte) (string, error) {
	// #nosec G204 -- fixed plutil argv, archived plist supplied on stdin, no shell
	cmd := exec.Command("plutil", "-extract", "CFBundleIdentifier", "raw", "-o", "-", "-")
	cmd.Stdin = bytes.NewReader(body)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("reading actual plist: %w: %s", err, out)
	}
	id := strings.TrimSpace(string(out))
	if id == "" || strings.ContainsAny(id, "\r\n") {
		return "", errors.New("actual plist has no single bundle identifier; refuse packaging")
	}
	return id, nil
}
