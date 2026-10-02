package engine

import (
	"context"
	"errors"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// inlineThreshold is the A8 context-hygiene boundary: blobs at or below this
// inline as base64; larger ones materialize to a file path the agent opens.
const inlineThreshold = 256 * 1024

// SetBlobs attaches the side blob store (must be called before Run).
func (e *Engine) SetBlobs(bs Store) { e.blobs = bs }

// PutBlob stages bytes durably OFF the event loop, then registers the resulting
// content id ON the loop: the object-before-ref ordering of A4.1. Exactly one
// rate token is spent, in the pre-auth: a throttled or unauthenticated caller
// is rejected BEFORE any bytes are hashed/sealed/written, and a caller that
// passes pre-auth will not then be re-throttled at registration (the register
// step reuses the same admission, not a second rate charge). The staged id is
// held in-flight until registration commits, so a concurrent reconcile cannot
// delete the just-written bytes.
func (e *Engine) PutBlob(ctx context.Context, token string, data []byte, path, mime string) (core.Result, error) {
	if e.blobs == nil {
		return nil, errors.New("blob store not configured")
	}
	reserve := int64(len(data))
	if path != "" {
		reserve = int64(e.state.Limits.MaxBlobSize) // file size is rechecked by the store; never trust a path hint
	}
	identity, err := e.AuthorizeTransfer(ctx, token, "", &reserve)
	if err != nil {
		return nil, err // auth + rate + wake + bounded staging, all before bytes
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = e.ReleaseTransfer(cleanup, identity.Reservation)
	}()
	maxSize := e.state.Limits.MaxBlobSize // immutable after init; safe off-loop
	var (
		id   string
		size int64
	)
	if path != "" {
		id, size, err = e.blobs.PutFile(path, maxSize)
	} else {
		id, size, err = e.blobs.Put(data, maxSize)
	}
	if err != nil {
		if errors.Is(err, core.ErrBlobTooLarge) {
			return nil, core.ErrTooLargeBlob(maxSize)
		}
		return nil, err
	}
	defer e.blobs.Release(id) // end in-flight protection once registration settles
	return e.CommitTransfer(ctx, identity, id, size, mime)
}

// GetBlob returns a blob's content for an authorized caller (A6, A8). Access is
// checked ON the loop; the decrypt/read/materialize runs OFF it. `as` is
// auto|inline|path: auto inlines only small media and materializes the rest.
func (e *Engine) GetBlob(ctx context.Context, token, id, as string) (core.Result, error) {
	if e.blobs == nil {
		return nil, errors.New("blob store not configured")
	}
	meta, err := e.query(ctx, func() core.Result {
		now := time.Now()
		l, errRes := e.authRead(token, now)
		if errRes != nil {
			return errRes
		}
		if id == "" {
			return core.Result{"error": core.ErrNoID}
		}
		if !core.ValidBlobID(id) {
			return core.Result{"error": core.ErrBadID}
		}
		if !e.state.BlobAccessible(id, l.ID) {
			// "You may not have it" and "it no longer exists" are different
			// problems, and answering the second with the first sends the agent
			// to debug an access rule it had already satisfied.
			if e.state.BlobWasEvicted(id, l.ID) {
				return core.Result{"error": core.ErrBlobEvicted}
			}
			return core.Result{"error": core.ErrNoBlob} // does not reveal existence
		}
		b := e.state.Blobs[id]
		return core.Result{"size": b.Size, "mime": b.Mime}
	})
	if err != nil {
		return nil, err
	}
	if e2, ok := meta["error"].(error); ok {
		return nil, e2
	}
	size, _ := meta["size"].(int64)
	mime, _ := meta["mime"].(string)

	materialize := as == "path" || (as != "inline" && size > inlineThreshold)
	if materialize {
		p, merr := e.blobs.Materialize(id)
		if merr != nil {
			return nil, mapBlobErr(merr)
		}
		return core.Result{"blob": id, "delivery": "path", "path": p, "size": size, "mime": mime}, nil
	}
	plain, rerr := e.blobs.Read(id)
	if rerr != nil {
		return nil, mapBlobErr(rerr)
	}
	return core.Result{"blob": id, "delivery": "inline", "bytes": plain, "size": size, "mime": mime}, nil
}

func mapBlobErr(err error) error {
	if errors.Is(err, core.ErrBlobMissing) {
		return core.ErrBlobUnavailable
	}
	return err
}

// reconcileBlobs deletes on-disk blob/out files whose ids are no longer live in
// the registry: evicted blobs and crash orphans (A4.1/A5). The live-id
// snapshot is taken ON the loop; the filesystem sweep runs OFF it.
func (e *Engine) reconcileBlobs() {
	if e.blobs == nil {
		return
	}
	live := make(map[string]bool, len(e.state.Blobs))
	for id := range e.state.Blobs {
		live[id] = true
	}
	go func() { _, _ = e.blobs.Reconcile(live) }()
}
