package engine

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/mailhistory"
)

func historyText(value string) core.Result {
	n := min(len(value), 32<<10)
	for n > 0 && !utf8.ValidString(value[:n]) {
		n--
	}
	return core.Result{"text": value[:n], "recorded_bytes": len(value), "truncated": n < len(value)}
}

func (e *Engine) historyContent(ctx context.Context, index *mailhistory.Index, unit mailhistory.Unit,
	deadline time.Time,
) (core.Result, error) {
	source, ok := e.led.(HistoryContentSource)
	if !ok {
		return nil, mailhistory.ErrUnavailable
	}
	seek, err := index.ContentRange(unit.Position.Op, index.Status().Drained)
	if err != nil {
		return nil, err
	}
	work, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	op, err := source.ReadHistoryOp(work, unit.Position.Op, seek)
	if err != nil {
		return nil, err
	}
	if op.Kind != unit.Kind {
		return nil, mailhistory.ErrUnavailable
	}
	content := core.Result{
		"quoted_data": true, "body": historyText(op.Body),
		"disposition": op.Disposition, "milestone": op.Milestone, "deliverable": historyText(op.Deliverable),
	}
	texts := func(values []string) core.Result {
		out := make([]core.Result, 0, min(len(values), 8))
		for _, value := range values[:min(len(values), 8)] {
			out = append(out, historyText(value))
		}
		return core.Result{"values": out, "recorded_count": len(values), "truncated": len(values) > len(out)}
	}
	content["choices"], content["milestones"] = texts(op.Choices), texts(op.Milestones)
	attachments := make([]core.Result, 0, min(len(op.Attachments), 8))
	for _, a := range op.Attachments[:min(len(op.Attachments), 8)] {
		attachments = append(attachments, core.Result{
			"blob": a.Blob, "path": historyText(a.Path),
			"size": a.Size, "hash": a.Hash, "mime": a.Mime, "recorded_host": unit.Author.Host,
		})
	}
	content["attachments"] = core.Result{
		"values": attachments, "recorded_count": len(op.Attachments),
		"truncated": len(op.Attachments) > len(attachments),
	}
	return content, nil
}
