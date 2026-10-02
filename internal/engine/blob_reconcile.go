package engine

import (
	"log/slog"

	"github.com/agenxy/dibs/internal/core"
)

// Writer-only. The caller still holds its stage until the registration reply,
// so an extra hold acquired here bridges every older snapshot's stale view.
func (e *Engine) protectBlobRegistration(id string) {
	if e.blobs == nil || e.blobReconciles == 0 || e.blobReconcileHeld[id] {
		return
	}
	if e.blobReconcileHeld == nil {
		e.blobReconcileHeld = make(map[string]bool)
	}
	e.blobs.Hold(id)
	e.blobReconcileHeld[id] = true
}

func (e *Engine) releaseBlobReconcileHolds() {
	for id := range e.blobReconcileHeld {
		e.blobs.Release(id)
	}
	e.blobReconcileHeld = nil
}

func (e *Engine) pruneBlobSnapshot(live map[string]bool) {
	defer e.blobReconcileWorkers.Done()
	defer func() {
		if fault := recover(); fault != nil {
			slog.Error("blob reconciliation panicked", "panic", fault,
				"hint", "inspect the blob-store adapter; the next periodic reconcile retries cleanup")
		}
		// No timeout while Run is serving: losing this receipt would retain
		// every extra hold. Cancellation ends the send, then Run waits for
		// this worker before releasing the remaining holds itself.
		_, _ = e.query(e.blobReconcileContext, func() core.Result {
			e.blobReconciles--
			if e.blobReconciles == 0 {
				e.releaseBlobReconcileHolds()
			}
			return nil
		})
	}()
	if _, err := e.blobs.Reconcile(live); err != nil {
		slog.Error("blob reconciliation failed", "error", err,
			"hint", "check blob-store access; the next periodic reconcile retries cleanup")
	}
}

// Run's shutdown path, still owning the writer state. Do not release holds
// while an old snapshot can still unlink a newer committed file. Workers use
// Run's exit-canceled context for their receipt, including a writer panic,
// so waiting cannot deadlock them.
func (e *Engine) finishBlobReconciles() {
	e.blobReconcileWorkers.Wait()
	e.releaseBlobReconcileHolds()
	e.blobReconciles = 0
}
