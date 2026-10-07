// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package build

import (
	"fmt"
	"runtime/debug"
	"strings"
)

const versionSymbol = "github.com/agenxy/dibs/internal/build.Version"

// CheckInstallStamp prevents a normal source install from silently changing
// version policy through GOFLAGS. Release packaging may deliberately link a
// tag; task install must preserve what this artifact's module/VCS data says.
// Use resolve, not Version: the checking tool inherits GOFLAGS too, so its own
// linked variable can be exactly the override it is supposed to catch.
func CheckInstallStamp(info *debug.BuildInfo) error {
	for _, setting := range info.Settings {
		if setting.Key != "-ldflags" {
			continue
		}
		if err := checkLinkerStamp(setting.Value, resolve(info)); err != nil {
			return err
		}
	}
	return nil
}

func checkLinkerStamp(text, computed string) error {
	args, err := linkerArgs(text)
	if err != nil {
		return err
	}
	for i := 0; i < len(args); i++ {
		assignment := ""
		if args[i] == "-X" {
			i++
			if i == len(args) {
				return fmt.Errorf("linker -X has no assignment")
			}
			assignment = args[i]
		} else if strings.HasPrefix(args[i], "-X=") {
			assignment = strings.TrimPrefix(args[i], "-X=")
		}
		symbol, value, ok := strings.Cut(assignment, "=")
		if !ok || symbol != versionSymbol {
			continue
		}
		if value != computed {
			return fmt.Errorf("linked Version %q differs from computed stamp %q; "+
				"remove the build.Version -X override from GOFLAGS and rebuild; "+
				"use a clean clone with reachable tags for normal source installs", value, computed)
		}
	}
	return nil
}

// Go's -ldflags grammar accepts quotes only around whole fields and does no
// unescaping inside them. A shell lexer would parse a different language.
func linkerArgs(text string) ([]string, error) {
	const spaces = " \t\r\n"
	var args []string
	for text = strings.TrimLeft(text, spaces); text != ""; text = strings.TrimLeft(text, spaces) {
		if text[0] == '\'' || text[0] == '"' {
			end := strings.IndexByte(text[1:], text[0])
			if end < 0 {
				return nil, fmt.Errorf("unterminated quoted linker field")
			}
			args = append(args, text[1:end+1])
			text = text[end+2:]
			continue
		}
		end := strings.IndexAny(text, spaces)
		if end < 0 {
			args = append(args, text)
			break
		}
		args = append(args, text[:end])
		text = text[end:]
	}
	return args, nil
}
