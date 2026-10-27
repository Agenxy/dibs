// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// TrafficTotals exposes no participant identity or contents, like the board.
func (e *Engine) TrafficTotals(ctx context.Context) (core.Result, error) {
	return e.query(ctx, func() core.Result {
		return core.Result{"traffic": e.state.MailTraffic(time.Now(), "", false), "serial": e.state.Serial}
	})
}

// TrafficCensus is observation, never delivery confirmation or model contact.
// Members can select themselves only; historical aliases are resolved solely
// after coordinator authorization, so refusals do not reveal other identities.
func (e *Engine) TrafficCensus(ctx context.Context, token string, census bool, agent string) (core.Result, error) {
	res, err := e.query(ctx, func() core.Result {
		l, refused := e.authObserve(token, time.Now())
		if refused != nil {
			return refused
		}
		if !census {
			return core.Result{"error": &core.Error{
				Code: "E_BAD_ARG", Msg: "traffic is counts only",
				Hint: "call all_mail(census:true, traffic:true); members get their own incarnation only",
			}}
		}
		if !l.IsCoordinator() {
			if agent != "" && agent != l.ID && agent != l.Name {
				return core.Result{"error": core.ErrNotCoordinator}
			}
			return core.Result{"traffic": e.state.MailTrafficSelf(time.Now(), l.ID, l.CreatedSerial), "serial": e.state.Serial}
		} else {
			var refused core.Result
			agent, refused = e.mailboxSelector(agent)
			if refused != nil {
				return refused
			}
		}
		return core.Result{"traffic": e.state.MailTraffic(time.Now(), agent, true), "serial": e.state.Serial}
	})
	if err != nil {
		return nil, err
	}
	if refused, ok := res["error"].(error); ok {
		return nil, refused
	}
	return res, nil
}
