// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// NativeReplySupport measures the installed image without posting or requesting
// authorisation. The old --status verb and additive environment flag are safe
// for old helpers: only a recognised legacy response establishes unsupported.
func NativeReplySupport() (bool, error) {
	h := helper()
	if goos != "darwin" || h == "" {
		return false, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// #nosec G204 -- installed helper, fixed existing non-posting status verb.
	cmd := exec.CommandContext(ctx, h, "--status")
	cmd.Env = append(os.Environ(), "DIBS_NOTIFY_PROMPT_V1=1")
	out, err := cmd.Output()
	var value struct {
		Reply   *int `json:"reply"`
		Version int  `json:"version"`
	}
	if json.Unmarshal(out, &value) == nil {
		if value.Reply != nil && *value.Reply == 1 {
			return true, nil
		}
		if value.Version == 1 {
			return false, nil
		}
	}
	switch strings.TrimSpace(string(out)) {
	case "authorized", "denied", "not-determined", "alerts-off":
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return false, errors.New("native reply capability is unknown; run dibs doctor after refreshing the Dibs service")
}
