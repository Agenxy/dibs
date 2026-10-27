// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/signal"
	"strings"
)

func asDetailHelper() {
	if len(os.Args) == 2 && strings.HasPrefix(os.Args[1], "--remove-messages=") {
		if os.Args[1] != "--remove-messages=dibs.msg.detail-fixture.7" {
			os.Exit(3)
		}
		_, _ = os.Stdout.WriteString(`{"cleanup":"requested"}`)
		return
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(3)
	}
	capture := struct {
		Args                   []string
		Stdin, Mode, Until, ID string
	}{os.Args[1:], string(body), os.Getenv("DIBS_NOTIFY_DETAIL"), os.Getenv("DIBS_NOTIFY_UNTIL"), os.Getenv("DIBS_NOTIFY_ID")}
	b, _ := json.Marshal(capture)
	if os.WriteFile(os.Getenv("DIBS_TEST_DETAIL_CAPTURE"), b, 0o600) != nil {
		os.Exit(3)
	}
	if path := os.Getenv("DIBS_NOTIFY_RECEIPT"); path != "" {
		if os.WriteFile(path, []byte(`{"state":"posted"}`), 0o600) != nil {
			os.Exit(3)
		}
	}
	if os.Getenv("DIBS_TEST_DETAIL_MODE") == "withdraw" && capture.Mode == "stdin" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		<-ctx.Done()
		stop()
	}
	_, _ = os.Stdout.WriteString("Approve\n")
}
