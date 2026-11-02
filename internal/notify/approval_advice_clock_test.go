// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestApprovalAdviceExpiryAndReceiptUseTheObservationClock(t *testing.T) {
	oldOS, oldReach := goos, approvalAdviceReach
	lastNativeSettings.Lock()
	oldValue, oldAdvice, oldAt := lastNativeSettings.value, lastNativeSettings.advice, lastNativeSettings.measuredAt
	lastNativeSettings.value, lastNativeSettings.advice, lastNativeSettings.measuredAt = nil, "", time.Time{}
	lastNativeSettings.Unlock()
	goos = "darwin"
	t.Cleanup(func() {
		goos, approvalAdviceReach = oldOS, oldReach
		lastNativeSettings.Lock()
		lastNativeSettings.value, lastNativeSettings.advice, lastNativeSettings.measuredAt = oldValue, oldAdvice, oldAt
		lastNativeSettings.Unlock()
	})
	// No external helper or I/O in the bubble. The real CLI board guard proves
	// the default probe and its rememberNativeSettings side effect separately.
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		settings := &Settings{Version: 1, AlertStyle: "banner"}
		approvalAdviceReach = func() (bool, string, *Settings) {
			calls++
			rememberNativeSettings(settings)
			return true, "", settings
		}
		if got := CurrentApprovalAdvice(); !strings.Contains(got, "appear as banners") || calls != 1 {
			t.Fatalf("cold advice did not measure: calls=%d advice=%q", calls, got)
		}
		_ = CurrentApprovalAdvice()
		if calls != 1 {
			t.Fatal("fresh advice repeated the native measurement")
		}
		synctest.Sleep(time.Minute + time.Second)
		synctest.Wait()
		if calls != 1 {
			t.Fatal("native measurement ran on a timer without a request")
		}
		settings.AlertStyle = "alert"
		if got := CurrentApprovalAdvice(); got != "" || calls != 2 {
			t.Fatalf("expired advice did not measure on request: calls=%d advice=%q", calls, got)
		}
		synctest.Sleep(10 * time.Second)
		if nativeReceiptSettings([]byte(`{"version":1,"alert_style":"banner"}`)) == nil {
			t.Fatal("setup: posting receipt was not decoded")
		}
		synctest.Sleep(time.Minute - time.Nanosecond)
		if got := CurrentApprovalAdvice(); !strings.Contains(got, "appear as banners") || calls != 2 {
			t.Fatalf("fresh posting receipt did not reset cache age: calls=%d advice=%q", calls, got)
		}
		synctest.Sleep(time.Nanosecond)
		if got := CurrentApprovalAdvice(); got != "" || calls != 3 {
			t.Fatalf("receipt-aged cache did not expire: calls=%d advice=%q", calls, got)
		}
	})
}
