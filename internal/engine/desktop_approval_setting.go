// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"strconv"

	"github.com/agenxy/dibs/internal/core"
)

// DesktopApprovalSettings is the single human preference, without minting a
// human identity merely to read it. The board already exposes this metadata.
func (e *Engine) DesktopApprovalSettings(ctx context.Context) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		s := e.state.DesktopApproval
		return core.Result{"setting": core.DesktopApprovalSettingKey, "value": strconv.FormatBool(s.Enabled),
			"by": s.By, "at": s.At, "saved": true, "serial": e.state.Serial}
	})
}

func desktopApprovalSettingError() error {
	return &core.Error{Code: "E_HUMAN_SETTING", Msg: "only the human may turn off privileged desktop approval confirmation",
		Hint: "open dibs web and use Settings, or run dibs desktop-approvals off " +
			"from your terminal with human authentication"}
}
