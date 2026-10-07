// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
)

// unstring repairs arguments a host sent as strings because its copy of the
// schema did not know the parameter.
//
// A long-running Claude Code session holds the tool schema it read at start,
// and a parameter added since is unknown to it, so it serialises the value
// as a string: `milestones` arrived as "[\"a\",\"b\"]" and was refused with
// "must be an array of strings, got string". The agent had no way to fix
// that from inside the session, and the feature was unusable until a
// restart. Reported by agenxy-supply (Dibs #7634).
//
// Only an unambiguous repair: a string whose target field is a string array
// and which parses as one, a string whose target is a number and parses as
// one, "true" or "false" for a boolean. Anything else is left as it was, so
// a real mistake still gets the error it deserves.
func unstring(raw json.RawMessage) json.RawMessage {
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) != nil {
		return raw
	}
	changed := false
	for key, val := range args {
		var s string
		if json.Unmarshal(val, &s) != nil {
			continue // not a string: nothing to repair
		}
		kind, ok := argKinds[key]
		if !ok {
			continue
		}
		if fixed, ok := repair(kind, strings.TrimSpace(s)); ok {
			args[key], changed = fixed, true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(args)
	if err != nil {
		return raw
	}
	return out
}

func repair(kind reflect.Type, s string) (json.RawMessage, bool) {
	switch {
	case kind.Kind() == reflect.Slice && kind.Elem().Kind() == reflect.String:
		var arr []string
		if strings.HasPrefix(s, "[") && json.Unmarshal([]byte(s), &arr) == nil {
			b, _ := json.Marshal(arr)
			return b, true
		}
	case kind.Kind() >= reflect.Int && kind.Kind() <= reflect.Int64:
		if n, err := strconv.ParseInt(s, 10, kind.Bits()); err == nil {
			return json.RawMessage(strconv.FormatInt(n, 10)), true
		}
	case kind.Kind() >= reflect.Uint && kind.Kind() <= reflect.Uint64:
		if n, err := strconv.ParseUint(s, 10, kind.Bits()); err == nil {
			return json.RawMessage(strconv.FormatUint(n, 10)), true
		}
	case kind.Kind() == reflect.Bool:
		if s == "true" || s == "false" {
			return json.RawMessage(s), true
		}
	}
	return nil, false
}

// argKinds maps each toolArgs json tag to its Go type, read once.
var argKinds = func() map[string]reflect.Type {
	out := map[string]reflect.Type{}
	t := reflect.TypeOf(toolArgs{})
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = f.Type
		}
	}
	return out
}()
