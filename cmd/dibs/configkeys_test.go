package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

// knownConfigKeys is a copy of every configuration key dibd accepts, kept
// because cmd/dibd is package main and cannot be imported.
//
// A copy is a liability the moment it drifts, in both directions: a key added
// to the daemon would be reported here as one it does not know, so the CLI
// would refuse a perfectly valid configuration; a key removed would go on being
// accepted. This reads the structs out of the daemon's own source, and the
// nested tables out of theirs, so the list cannot go stale without failing.
func TestConfigKeysMatchTheDaemon(t *testing.T) {
	structs := map[string]map[string]string{} // type -> tomlKey -> fieldType
	for _, src := range []string{"../dibd/config.go", "../dibd/roles.go", "../../internal/liveness/settings.go"} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, src, nil, 0)
		if err != nil {
			t.Fatalf("cannot read %s: %v", src, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			keys := map[string]string{}
			for _, fld := range st.Fields.List {
				if fld.Tag == nil {
					continue
				}
				tag := reflect.StructTag(strings.Trim(fld.Tag.Value, "`"))
				k := strings.Split(tag.Get("toml"), ",")[0]
				if k == "" || k == "-" {
					continue
				}
				keys[k] = typeName(fld.Type)
			}
			if len(keys) > 0 {
				structs[ts.Name.Name] = keys
			}
			return true
		})
	}

	top, ok := structs["Config"]
	if !ok {
		t.Fatal("no dibd Config found: this probe is reading nothing")
	}

	want := map[string]bool{}
	for k, ty := range top {
		want[k] = true
		for nested := range structs[ty] {
			want[k+"."+nested] = true
		}
	}

	for k := range want {
		if !knownConfigKeys[k] {
			t.Errorf("dibd accepts %q and this CLI would refuse a config that sets it", k)
		}
	}
	for k := range knownConfigKeys {
		if !want[k] {
			t.Errorf("%q is no longer a dibd config key, so this list is stale", k)
		}
	}
}

// typeName is the bare name of a field's type, so `liveness.Settings` and
// `MatchConfig` both resolve to something the struct table is keyed by.
func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.StarExpr:
		return typeName(t.X)
	}
	return ""
}
