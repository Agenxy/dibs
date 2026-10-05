package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
)

// A path identifies a repository on THIS host, never on another machine.
// The daemon only recommends against locally mined indexes. Hashes keep raw
// repository names out of the derived suppression file beside the ledger.
func scorerAdviceKey(host, root string) string {
	sum := sha256.Sum256([]byte(host + "\x00" + root))
	return hex.EncodeToString(sum[:])
}

// Fingerprint operator configuration. A calibrated bar is corpus-derived, so
// its mode, "auto", participates rather than the bar's changing value.
// Corpus/index versions and daemon revisions deliberately do not participate:
// ordinary commits and installs do not make unchanged advice relevant again.
// Authentication secrets are not scorer configuration and never enter it.
func (f *scorerFlags) scorerAdviceRevision(scorer string) string {
	notify := "auto"
	if f.notify != 0 { // the same choice as notifyFor: zero invokes calibration
		notify = strconv.FormatFloat(f.notify, 'g', -1, 64)
	}
	config := struct {
		Scorer, EmbedURL, EmbedModel, QueryPrefix, DocPrefix, AutoJoin, Notify string
		Join                                                                   float64
		History                                                                int
		Deadline                                                               int64
		Director                                                               bool
	}{
		Scorer: scorer, EmbedURL: f.embedURL, EmbedModel: f.embedModel,
		QueryPrefix: f.embedQueryPrefix, DocPrefix: f.embedDocPrefix, AutoJoin: f.autoJoin,
		Join: f.join, Notify: notify, History: f.history, Deadline: int64(f.deadline), Director: f.director,
	}
	// Quoted strings and fixed-order fields keep the encoding unambiguous, and
	// preserve even an invalid non-finite operator threshold rather than hashing
	// every failed JSON encoding to the same revision.
	raw := fmt.Appendf(nil, "%q/%q/%q/%q/%q/%q/%g/%q/%d/%d/%t",
		config.Scorer, config.EmbedURL, config.EmbedModel, config.QueryPrefix, config.DocPrefix, config.AutoJoin,
		config.Join, config.Notify, config.History, config.Deadline, config.Director)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
