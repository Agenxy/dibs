package ledger

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/mailhistory"
)

// Frozen boot inputs and a derived scheduling hint cross the serving boundary.
// There is no live State here: the background fold constructs its own objects.
type historyReplay struct {
	file    io.ReaderAt
	box     *Box
	node    string
	limits  core.Limits
	head    mailhistory.Record
	records uint64
	writer  *historyWriterActivity
}

func (l *Ledger) configureHistory(node string, limits core.Limits, serial, records uint64, end int64) {
	source := &historyReplay{
		file: l.f, box: l.box, node: node, limits: limits, records: records,
		head:   mailhistory.Record{Serial: serial, End: end, Hash: l.headSum},
		writer: &l.historyWriter,
	}
	l.mail.ConfigureBootstrap(source.head, records, source.run)
}

func (s *historyReplay) run(ctx context.Context, index *mailhistory.Index) error {
	// SectionReader uses ReadAt: the writer's append offset is never changed,
	// and bytes appended after the frozen boot boundary cannot enter this fold.
	input := &historyReader{source: s.file, writer: s.writer, ctx: ctx}
	reader := bufio.NewReaderSize(io.NewSectionReader(input, 0, s.head.End), 1<<20)
	shadow := core.NewState(s.node, s.limits)
	projector := index.ReplayProjector()
	var scratch mailhistory.Snapshot
	var offset int64
	var previous [32]byte
	var last mailhistory.Record
	for n := uint64(0); n < s.records; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n%256 == 0 {
			if err := s.writer.wait(ctx); err != nil {
				return err
			}
		}
		raw, err := readHistoryLine(reader)
		if err != nil {
			return fmt.Errorf("history bootstrap at record %d: %w", n, err)
		}
		record, err := s.foldRecord(raw, previous, shadow, &scratch, projector, ctx, n, offset)
		if err != nil {
			return err
		}
		last, previous, offset = record, record.Hash, record.End
		if (n+1)%256 == 0 {
			projector.Progress(last)
			// Bound runnable work between yields; elapsed time and shadow heap are
			// measured alongside actual writer p99, never assumed harmless.
			runtime.Gosched()
		}
	}
	if offset != s.head.End || previous != s.head.Hash || shadow.Serial != s.head.Serial {
		return errors.New("history bootstrap disagrees with the validated boot boundary")
	}
	hash, err := historyStateHash(shadow)
	if err != nil {
		return err
	}
	projector.Finish(last, hash)
	// Nothing returning from this function contains shadow or scratch. Live
	// commits are now drained through the real writer observer, without a fold.
	return nil
}

func (s *historyReplay) foldRecord(raw []byte, previous [32]byte, shadow *core.State,
	scratch *mailhistory.Snapshot, projector *mailhistory.ReplayProjector,
	ctx context.Context, ordinal uint64, offset int64,
) (mailhistory.Record, error) {
	var rec Line
	line := raw[:len(raw)-1]
	if err := json.Unmarshal(line, &rec); err != nil {
		return mailhistory.Record{}, err
	}
	prev := ""
	if ordinal > 0 {
		prev = hex.EncodeToString(previous[:])
	}
	if rec.Op == nil || rec.Prev != prev {
		return mailhistory.Record{}, errors.New("history bootstrap record or chain is invalid")
	}
	if err := s.box.DecryptOp(rec.Op); err != nil {
		return mailhistory.Record{}, err
	}
	before := mailhistory.Capture(shadow, rec.Op, scratch)
	_, events, err := shadow.Apply(rec.Op, rec.T)
	if err != nil {
		return mailhistory.Record{}, err
	}
	if rec.S < shadow.Serial {
		return mailhistory.Record{}, errors.New("history bootstrap serial went backwards")
	}
	shadow.Serial = rec.S // same survivable forward-gap rule as boot Replay
	sum := sha256.Sum256(line)
	record := mailhistory.Record{
		Serial: rec.S, At: rec.T, Offset: offset, End: offset + int64(len(raw)), Prev: previous, Hash: sum,
	}
	if err := projector.Observe(ctx, ordinal, record, before, shadow, rec.Op, events); err != nil {
		return mailhistory.Record{}, err
	}
	return record, nil
}

func historyStateHash(st *core.State) ([32]byte, error) {
	// encoding/json orders map keys and follows core's existing credential
	// redactions. The canary covers that canonical board encoding without a
	// separately maintained projection of its fields.
	raw, err := json.Marshal(st)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

// The next read may reuse the buffer: the fold, hash and projector finish
// before that read. Large historical lines keep Replay's behavior, with one
// owned line only when the fixed reader buffer cannot contain it.
func readHistoryLine(reader *bufio.Reader) ([]byte, error) {
	raw, err := reader.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return raw, err
	}
	owned := append([]byte(nil), raw...)
	for errors.Is(err, bufio.ErrBufferFull) {
		raw, err = reader.ReadSlice('\n')
		owned = append(owned, raw...)
	}
	return owned, err
}
