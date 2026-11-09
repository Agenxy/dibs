// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"slices"
	"testing"
)

func TestPublishedTagIsTheLatestReleaseOrARefusal(t *testing.T) {
	var asked []string
	gh := func(out string, err error) func(...string) (string, error) {
		return func(args ...string) (string, error) { asked = args; return out, err }
	}
	tag, err := publishedTag("Agenxy/dibs", gh("v0.0.18\n", nil))
	if err != nil || tag != "v0.0.18" || !slices.Contains(asked, "Agenxy/dibs") {
		t.Fatalf("resolved %q %v from %v", tag, err, asked)
	}
	for name, c := range map[string]struct {
		repo, out string
		err       error
	}{
		"no repository": {"", "v0.0.18", nil},
		"gh failed":     {"Agenxy/dibs", "", errors.New("exit status 1")},
		"empty output":  {"Agenxy/dibs", "\n", nil},
		"not a tag":     {"Agenxy/dibs", "latest", nil},
	} {
		if tag, err := publishedTag(c.repo, gh(c.out, c.err)); err == nil {
			t.Errorf("%s: resolved %q instead of refusing", name, tag)
		}
	}
}
