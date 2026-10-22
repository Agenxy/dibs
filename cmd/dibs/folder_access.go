// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/agenxy/dibs/internal/folderaccess"
	"github.com/agenxy/dibs/internal/paths"
)

func checkFolderAccess(dir string, b *boardView, request bool, ok, note reportFn, warn fixFn) {
	if runtime.GOOS != "darwin" || b == nil {
		return
	}
	if request && b.Node != "" && servedFromHere(dir, b.Node) {
		requested, err := requestFolderAccess()
		if err != nil {
			warn("could not request protected-folder access", err.Error())
		} else if requested {
			note("requested protected-folder directory reads; macOS decides whether " +
				"to ask for consent. Requested does not mean granted")
			// A fresh read of the view, never a local guess about a daemon's grant.
			if latest, err := boardSnapshot(); err == nil {
				b = latest
			}
		}
	}
	for _, o := range b.FolderAccess {
		if o.State == "granted" {
			ok("Dibs can read " + o.Folder)
			continue
		}
		warn("Dibs "+o.Folder+" access is "+o.State, folderaccess.Settings+"; "+o.Reason+". Changing source/release "+
			"signing channel can require a fresh grant. Run dibs doctor --request-folder-access to request a read once")
	}
}

func requestFolderAccess() (bool, error) {
	selected, err := paths.SelectDir(false)
	if err != nil {
		return false, err
	}
	return requestFolderAccessAt(selected.Dir, origin())
}

func requestFolderAccessAt(dir, endpoint string) (bool, error) {
	secret, err := localSecretIn(dir)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint+"/api/folder-access/request", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := daemonClientIn(dir, 2*time.Second).Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("daemon refused the folder read request (HTTP %d); upgrade Dibs and "+
			"check %s", resp.StatusCode, folderaccess.Settings)
	}
	var result struct {
		Requested bool `json:"requested"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}
	return result.Requested, nil
}
