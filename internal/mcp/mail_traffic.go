// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"

	"github.com/agenxy/dibs/internal/core"
)

func (s *Server) allMail(ctx context.Context, a *toolArgs) (core.Result, error) {
	if a.Traffic {
		return s.eng.TrafficCensus(ctx, a.Token, a.Census, a.AgentRef)
	}
	return s.eng.AllMail(ctx, a.Token, a.Census, a.AgentRef)
}
