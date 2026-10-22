// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package releasecodesign

import "testing"

func TestOfflineAdhocIsNeverAProductionIdentity(t *testing.T) {
	const adhoc = "Signature=adhoc\n# designated => cdhash H\"1234\"\n"
	if err := Check(adhoc, "org.agenxy.dibs", true); err != nil {
		t.Fatal("offline fixture refused:", err)
	}
	if err := Check(adhoc, "org.agenxy.dibs", false); err == nil {
		t.Fatal("production accepted an ad-hoc identity")
	}
	if err := Check("", "org.agenxy.dibs", false); err == nil {
		t.Fatal("missing requirement accepted")
	}
}
