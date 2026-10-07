package engine

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/mailhistory"
)

// Reserve room for MCP's escaped text envelope and the ordinary waiting hint;
// even a maximally escaped payload must stay within the 128 KiB wire bound.
const historyPageBytes = 60 << 10

// ReadMailHistory observes committed evidence without the authRead side
// effects. Work, decompression, native seeks and serialization stay off writer.
func (e *Engine) ReadMailHistory(ctx context.Context, token string, since uint64, limit int,
	includeBodies bool, cursor string,
) (core.Result, error) {
	deadline := time.Now().Add(250 * time.Millisecond)
	var reader core.Agent
	var index *mailhistory.Index
	var upper, fence uint64
	admitted, err := e.query(ctx, func() core.Result {
		a, refused := e.authObserve(token, time.Now())
		if refused != nil {
			return refused
		}
		if limit == 0 {
			limit = 25
		}
		if !historyArgumentsValid(limit, since, cursor) {
			return historyError("E_HISTORY_ARGS", "invalid history page arguments",
				"call mail_history with limit 1–100; use cursor alone on later pages")
		}
		if e.mailSource == nil {
			return historyFailure(mailhistory.ErrUnavailable, mailhistory.Status{}, e.state.Serial)
		}
		reader = core.Agent{ID: a.ID, CreatedSerial: a.CreatedSerial}
		index = e.mailSource.MailHistory()
		upper, fence = e.state.Serial, index.Status().OwnershipChange
		return core.Result{}
	})
	if err != nil || admitted["error"] != nil {
		return admitted, err
	}
	page, err := index.ReadPage(ctx, mailhistory.PageRequest{
		Reader: reader, Upper: upper, Since: since, OwnershipChange: fence, Limit: limit, Cursor: cursor, Deadline: deadline,
	})
	if err != nil {
		return historyRefusal(err, index.Status(), upper)
	}
	checked, err := e.historyReauthorize(ctx, token, reader, index, page, upper, fence)
	if err != nil || checked["error"] != nil {
		return checked, err
	}
	result, err := e.historyRows(ctx, index, page, includeBodies, deadline)
	if err != nil {
		return historyRefusal(err, index.Status(), upper)
	}
	checked, err = e.historyReauthorize(ctx, token, reader, index, page, upper, fence)
	if err != nil || checked["error"] != nil {
		return checked, err
	}
	return result, nil
}

func historyArgumentsValid(limit int, since uint64, cursor string) bool {
	return limit >= 1 && limit <= 100 && (cursor == "" || since == 0)
}

func (e *Engine) historyReauthorize(ctx context.Context, token string, reader core.Agent,
	index *mailhistory.Index, page mailhistory.Page, upper, fence uint64,
) (core.Result, error) {
	res, err := e.query(ctx, func() core.Result {
		a := e.state.AgentByToken(token)
		if a == nil || a.ID != reader.ID || a.CreatedSerial != reader.CreatedSerial {
			return core.Result{"error": core.ErrBadToken}
		}
		status := index.Status()
		if status.Failed {
			return historyFailure(mailhistory.ErrUnavailable, status, upper)
		}
		if status.OwnershipChange != fence {
			return historyFailure(mailhistory.ErrSettling, status, upper)
		}
		for _, unit := range page.Units {
			if m := e.state.Messages[unit.Position.Msg]; m != nil {
				if allowed, _ := core.MessageAccess(unit.Position.Msg, m, a); !allowed {
					return historyFailure(mailhistory.ErrSettling, status, upper)
				}
			}
		}
		return core.Result{}
	})
	var ce *core.Error
	if errors.As(err, &ce) {
		if ce.Code == "E_HISTORY_SETTLING" {
			return historyRefusal(mailhistory.ErrSettling, index.Status(), upper)
		}
		if ce.Code == "E_HISTORY_UNAVAILABLE" {
			return historyRefusal(mailhistory.ErrUnavailable, index.Status(), upper)
		}
	}
	return res, err
}

func historyError(code, msg, hint string) core.Result {
	return core.Result{"error": &core.Error{Code: code, Msg: msg, Hint: hint}}
}

func historyRefusal(err error, status mailhistory.Status, upper uint64) (core.Result, error) {
	res := historyFailure(err, status, upper)
	return res, res["error"].(*core.Error)
}

func historyFailure(err error, status mailhistory.Status, upper uint64) core.Result {
	code, hint := "E_HISTORY_UNAVAILABLE",
		"retry mail_history shortly; if it persists, ask the coordinator to inspect the ledger-derived history view"
	switch {
	case errors.Is(err, mailhistory.ErrWarming):
		code, hint = "E_HISTORY_WARMING", "initial history build is still warming; retry mail_history shortly"
	case errors.Is(err, mailhistory.ErrSettling):
		code, hint = "E_HISTORY_SETTLING", "mail ownership changed while history was catching up; retry mail_history shortly"
	case errors.Is(err, mailhistory.ErrCursor):
		code, hint = "E_HISTORY_CURSOR", "call mail_history without cursor to start a fresh fixed-prefix page"
	}
	// Native I/O and parse errors can name local paths. The wire carries the
	// corrective diagnosis, never implementation details from an untrusted file.
	msg := "history view is unavailable"
	if code != "E_HISTORY_UNAVAILABLE" {
		msg = err.Error()
	}
	res := historyError(code, msg, hint)
	res["as_of_serial"] = status.Drained.Serial
	res["behind_by"] = uint64(0)
	if status.OwnershipChange > upper {
		upper = status.OwnershipChange
	}
	if upper > status.Drained.Serial {
		res["behind_by"] = upper - status.Drained.Serial
	}
	return res
}

func (e *Engine) historyRows(ctx context.Context, index *mailhistory.Index, page mailhistory.Page,
	includeBodies bool, deadline time.Time,
) (core.Result, error) {
	rows := make([]core.Result, 0, len(page.Units))
	res := core.Result{"units": rows, "as_of_serial": page.AsOf, "behind_by": page.Behind}
	if page.Next != "" {
		res["cursor"] = page.Next
	}
	for n, unit := range page.Units {
		if n > 0 && time.Now().After(deadline) {
			res["cursor"] = page.Cursors[n-1]
			break
		}
		row, err := e.historyRow(ctx, index, unit, includeBodies, deadline)
		if err != nil {
			if n > 0 && errors.Is(err, context.DeadlineExceeded) {
				res["cursor"] = page.Cursors[n-1]
				break
			}
			return nil, err
		}

		var fits bool
		rows, fits, err = appendHistoryRow(res, rows, row)
		if err != nil {
			return nil, err
		}
		if !fits {
			res["units"], res["cursor"] = rows, page.Cursors[n-1]
			break
		}
	}
	return res, nil
}

// Keep the serialization bound independent of the page/time traversal.
func appendHistoryRow(res core.Result, rows []core.Result, row core.Result) ([]core.Result, bool, error) {
	rows = append(rows, row)
	res["units"] = rows
	raw, err := json.Marshal(res)
	if err != nil {
		return nil, false, err
	}
	if len(raw) <= historyPageBytes {
		return rows, true, nil
	}
	if len(rows) > 1 {
		return rows[:len(rows)-1], false, nil
	}
	// Content is quoted data, never silently claimed complete.
	row["content"] = stripHistoryText(row["content"])
	row["content_unavailable"] = "encoded page bound"
	raw, err = json.Marshal(res)
	if err != nil || len(raw) > historyPageBytes {
		return nil, false, mailhistory.ErrUnavailable
	}
	return rows, true, nil
}

func (e *Engine) historyRow(ctx context.Context, index *mailhistory.Index, unit mailhistory.Unit,
	includeBodies bool, deadline time.Time,
) (core.Result, error) {
	row := core.Result{"unit": unit}
	if includeBodies && unit.Content {
		if time.Now().After(deadline) {
			row["content"] = core.Result{
				"quoted_data": true,
				"unavailable": "work budget exhausted; retry this conversation from its preceding serial",
			}
		} else {
			content, err := e.historyContent(ctx, index, unit, deadline)
			if err != nil {
				return nil, err
			}
			row["content"] = content
		}
	}
	return row, nil
}
