package ledger

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/mailhistory"
)

const historySeekBytes = 16 << 20

// ReadHistoryOp authenticates a bounded native sparse interval, then decrypts
// only the requested operation. It never changes the append seek position or
// reads attachment bytes. The caller must authorize before and after this I/O.
func (l *Ledger) ReadHistoryOp(ctx context.Context, serial uint64, seek mailhistory.SeekRange) (*core.Op, error) {
	length := seek.End - seek.Start.Offset
	if length > historySeekBytes {
		return nil, mailhistory.ErrContentBudget
	}
	if length <= 0 {
		return nil, mailhistory.ErrUnavailable
	}
	r := bufio.NewReaderSize(io.NewSectionReader(l.f, seek.Start.Offset, length), 64<<10)
	previous := seek.Start.Prev
	var found *core.Op
	var consumed int64
	var priorSerial uint64
	for n := 0; consumed < length; n++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if n >= 4096 {
			return nil, mailhistory.ErrContentBudget
		}
		raw, err := readHistoryLine(r)
		if err != nil {
			return nil, err
		}
		consumed += int64(len(raw))
		line, err := historyContentLine(raw, previous, seek.Start.Serial, priorSerial, n == 0)
		if err != nil {
			return nil, err
		}
		previous = sha256.Sum256(raw[:len(raw)-1])
		priorSerial = line.S
		if line.S == serial {
			found = line.Op
		}
	}
	if consumed != length || previous != seek.Hash || found == nil {
		return nil, mailhistory.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := l.box.DecryptOp(found); err != nil {
		return nil, err
	}
	return found, nil
}

func historyContentLine(raw []byte, previous [32]byte, start, prior uint64, first bool) (Line, error) {
	var line Line
	if err := json.Unmarshal(raw[:len(raw)-1], &line); err != nil {
		return line, err
	}
	prev := ""
	if previous != ([32]byte{}) {
		prev = hex.EncodeToString(previous[:])
	}
	if line.Op == nil || line.Prev != prev || (first && line.S != start) || (!first && line.S <= prior) {
		return line, errors.New("history content interval chain or serial is invalid")
	}
	return line, nil
}
