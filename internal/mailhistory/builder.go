// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mailhistory

import (
	"context"
	"errors"
	"net"
	"sync"
)

const liveQueueBytes = 64 << 20

func (i *Index) ensureCapture() {
	if i.active != nil && i.active.count == blockUnits {
		i.sealCapture()
	}
	if i.active == nil {
		i.active = newRawChunk()
	}
}

func (i *Index) capture(u snapshotUnit) {
	if i.failed {
		return
	}
	i.ensureCapture()
	i.active.add(u)
	i.active.head = i.head
	i.captured++
	if i.queueLimit > 0 && i.queued+i.active.charge() > i.queueLimit {
		i.failCapture() // never block the writer or silently lose a committed unit
	}
}

func (i *Index) captureUnit(u snapshotUnit) error {
	i.capture(u)
	if i.failed {
		return errors.New("history live queue capacity exceeded")
	}
	return nil
}

func (i *Index) sealCapture() {
	if i.active == nil {
		return
	}
	c := i.active
	c.seal()
	c.head = i.head // include records with no changed mail in the queued watermark
	if i.last == nil {
		i.first = c
	} else {
		i.last.next = c
	}
	i.last, i.active = c, nil
	i.queued += c.charge()
}

func (i *Index) failCapture() {
	i.failed, i.ready = true, false
	i.first, i.last, i.active = nil, nil, nil
	i.queued = 0
}

// ServingListener starts the builder when net/http enters its first Accept,
// after binding and TLS validation. Creating an index or ending Replay never
// launches it, and Accept never waits for history to finish warming.
func (i *Index) ServingListener(ctx context.Context, listener net.Listener) net.Listener {
	return &historyListener{Listener: listener, ctx: ctx, index: i}
}

type historyListener struct {
	net.Listener
	ctx   context.Context
	index *Index
	once  sync.Once
}

func (l *historyListener) Accept() (net.Conn, error) {
	l.once.Do(func() { l.index.start(l.ctx) })
	return l.Listener.Accept()
}

func (i *Index) start(ctx context.Context) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.started || i.failed {
		return
	}
	i.started = true
	i.sealCapture()
	if i.bootstrap == nil {
		i.queueLimit = i.queued + liveQueueBytes // isolated projector fixtures, not boot replay
	}
	go i.build(ctx)
}

func (i *Index) nextCapture() *rawChunk {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.failed {
		return nil
	}
	i.sealCapture()
	c := i.first
	if c != nil {
		i.first = c.next
		c.next = nil
		if i.first == nil {
			i.last = nil
		}
	}
	return c
}

func (i *Index) buildChunk(ctx context.Context, c *rawChunk) error {
	// The view lock is independent of capture. Even compression and decoding
	// a whole chunk cannot delay a coordination op waiting on the writer.
	i.viewMu.Lock()
	defer i.viewMu.Unlock()
	for _, a := range c.anchors {
		i.anchors.add(a)
	}
	var previous snapshotUnit
	move := 0
	for n := uint32(0); n < c.count; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for move < len(c.moves) && c.moves[move].before == n {
			m := c.moves[move]
			i.party(m.to).inherit(i.parties[m.from])
			move++
		}
		u := c.unit(n, previous)
		previous = u
		keys := [2]partyKey{{u.Metadata.From, u.FromCreated}, {u.Metadata.To, u.ToCreated}}
		count := 2
		if keys[0] == keys[1] {
			count = 1
		}
		if err := i.add(u, keys[:count]); err != nil {
			return err
		}
	}
	if move != len(c.moves) {
		return errors.New("history ownership transfer has no following unit")
	}
	return nil
}

func (i *Index) build(ctx context.Context) {
	defer i.Invalidate() // context cancellation never leaves a complete-looking stale view
	if err := i.buildBootstrap(ctx); err != nil {
		return
	}
	i.drain(ctx)
}

func (i *Index) buildBootstrap(ctx context.Context) error {
	i.mu.RLock()
	read := i.bootstrap
	i.mu.RUnlock()
	if read != nil {
		if err := read(ctx, i); err != nil {
			return err
		}
	}
	i.mu.Lock()
	i.bootstrap = nil // the reader's private shadow and its closure are no longer retained
	if read != nil && !i.failed {
		i.initialReady = true // the validated boot fold reached S0, once
	}
	i.mu.Unlock()
	return nil
}

func (i *Index) drain(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if c := i.nextCapture(); c != nil {
			if err := i.buildChunk(ctx, c); err != nil {
				i.mu.Lock()
				i.failCapture()
				i.mu.Unlock()
				return
			}
			charge := c.charge()
			i.mu.Lock()
			if i.failed {
				i.mu.Unlock()
				return
			}
			i.queued -= charge
			i.queueLimit = max(liveQueueBytes, i.queueLimit-charge)
			i.built += uint64(c.count)
			i.builtHead = c.head
			i.mu.Unlock()
			continue // release this raw chunk before draining its successor
		}
		i.viewMu.Lock()
		err := i.codec.flush()
		if err == nil {
			// A drained flush has sealed every unit in immutable blocks. Keep
			// the compressor, but release the empty raw tail and encode scratch.
			i.codec.tail, i.codec.scratch = nil, nil
		}
		i.viewMu.Unlock()
		if err != nil {
			i.Invalidate()
			return
		}
		if !i.finishDrain() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-i.signal:
		}
	}
}

// Publishing an idle watermark is a scalar-only decision. It never takes
// viewMu, and queries can inspect its coherent status during compression.
func (i *Index) finishDrain() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.failed {
		return false
	}
	if i.active == nil && i.first == nil && i.built == i.captured {
		i.builtHead = i.head
		i.ready = i.ended
		i.initialReady = i.initialReady || i.ready
	}
	return true
}
