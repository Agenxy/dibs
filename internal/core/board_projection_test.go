// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// This is a static architecture property, alongside the behavioral allocation
// guards at both real replay doors: core defines the pure Board view but never
// requests one itself. Only the live engine projects a caller's reply.
func TestCoreDoesNotProjectItsOwnBoard(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal("setup: core sources", err)
	}
	files := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files++
		positions := token.NewFileSet()
		source, err := parser.ParseFile(positions, name, nil, 0)
		if err != nil {
			t.Fatal("setup: parse core source", err)
		}
		ast.Inspect(source, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok && selector.Sel.Name == "Board" {
				t.Errorf("%s: core projects a board that replay discards", positions.Position(selector.Pos()))
			}
			return true
		})
	}
	if files == 0 {
		t.Fatal("setup: no production core sources were checked")
	}
}
