package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// requiredParams is the `required` list every tool already declares in its own
// inputSchema, indexed for enforcement. Derived from toolDefs rather than
// written out again, so a tool cannot declare one contract and be held to
// another.
var requiredParams = func() map[string][]string {
	out := map[string][]string{}
	for _, t := range toolDefs {
		name, _ := t["name"].(string)
		schema, _ := t["inputSchema"].(map[string]any)
		if name == "" || schema == nil {
			continue
		}
		if req, ok := schema["required"].([]string); ok {
			out[name] = req
		}
	}
	return out
}()

// knownParams is every property each tool accepts, for suggesting the right
// name when a caller supplies a near-miss.
var knownParams = func() map[string][]string {
	out := map[string][]string{}
	for _, t := range toolDefs {
		name, _ := t["name"].(string)
		schema, _ := t["inputSchema"].(map[string]any)
		if name == "" || schema == nil {
			continue
		}
		props, _ := schema["properties"].(map[string]any)
		for p := range props {
			out[name] = append(out[name], p)
		}
		sort.Strings(out[name])
	}
	return out
}()

// checkRequired rejects a call that omits a parameter the tool declares
// required, and says which one.
//
// This existed nowhere, and the schemas were decorative: arguments unmarshal
// into one struct, so an omitted parameter became that field's ZERO VALUE and
// the handler ran on it. Getting a name wrong therefore produced a confident,
// specific and false error: asking for an announcement with `serial` instead
// of `msg_serial` answered "no announcement at serial 0", a serial the caller
// never sent and does not appear anywhere in its request. The caller's
// reasonable conclusion is that the announcement is gone.
//
// That is the worst shape an error can take: it names a cause, the cause is
// fiction, and the real fault (a parameter name) is not mentioned. Found by
// making the mistake while using the tools, not by reading them.
//
// bearerToken is passed because `token` may legitimately arrive in the HTTP
// Authorization header instead of the arguments object.
func checkRequired(tool string, raw json.RawMessage, bearerToken, agentNonce string) error {
	req := requiredParams[tool]
	if len(req) == 0 {
		return nil
	}
	present := map[string]bool{}
	if len(raw) > 0 {
		var got map[string]json.RawMessage
		// Malformed JSON is not this function's business: the caller's own
		// decode reports it with a better message than "you are missing X",
		// which would be a second, misleading complaint about the same fault.
		//nolint:nilerr // deliberate: one fault, one error, raised where it reads best
		if err := json.Unmarshal(raw, &got); err != nil {
			return nil
		}
		for k, v := range got {
			// An explicit null is not an answer.
			if string(v) != "null" {
				present[k] = true
			}
		}
	}
	if bearerToken != "" {
		present["token"] = true
	}
	// A nonce carried by the transport counts as supplied, exactly like the
	// bearer token above. It is the same fact in both cases: a credential the
	// harness holds, presented where the harness can actually put it, rather
	// than where only the model could. See identityFromTransport.
	if agentNonce != "" {
		present["nonce"] = true
	}

	var missing []string
	for _, r := range req {
		if !present[r] {
			missing = append(missing, r)
		}
	}
	extra := unknownGiven(tool, present)

	// An argument the tool does not take is an error on its OWN, not merely a
	// hint about a missing one. This check used to live behind `len(missing)
	// == 0`, so a well-formed call carrying a misnamed field was answered
	// `{"ok": true}` and quietly did nothing: `update` accepts only
	// "description", and update(pid: 1234) reported success while
	// changing nothing at all.
	//
	// That is the worst failure this server can produce. An agent cannot see
	// the board it is not looking at; its only evidence that an action
	// happened is what this returns. Answering "ok" for work not done is
	// worse than any error, because the agent proceeds on it, and the whole
	// product is other agents trusting that.
	//
	// Safe to be strict: knownParams is derived from the tools' own declared
	// schemas above, not hand-maintained, so it cannot fall behind them.
	if len(missing) == 0 {
		if len(extra) == 0 {
			return nil
		}
		return fmt.Errorf("%s does not take %s%s: check the tool's schema; "+
			"nothing was changed", tool, quoteList(extra), synonymHint(tool, extra))
	}

	msg := fmt.Sprintf("%s needs %s", tool, quoteList(missing))
	// If the caller supplied something this tool does not accept, that is
	// almost always the misnamed parameter: say so, rather than leaving them
	// to diff two lists by eye.
	if len(extra) > 0 {
		msg += fmt.Sprintf(": you sent %s, which %s does not take%s",
			quoteList(extra), tool, synonymHint(tool, extra))
	}
	return fmt.Errorf("%s", msg)
}

func unknownGiven(tool string, present map[string]bool) []string {
	known := map[string]bool{}
	for _, p := range knownParams[tool] {
		known[p] = true
	}
	// "token" is authentication, not domain: it may arrive in the arguments or
	// as a bearer header, for any tool, and it is orthogonal to what that tool
	// is about. The session-addressed hook tools do not declare it: they are
	// identified by session_id, and our own shipped hooks send it anyway.
	// Strictness is here to catch a MISNAMED field, not to relitigate where
	// credentials are allowed to appear.
	known["token"] = true
	var extra []string
	for p := range present {
		if !known[p] {
			extra = append(extra, p)
		}
	}
	sort.Strings(extra)
	return extra
}

func quoteList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = `"` + x + `"`
	}
	if len(q) == 1 {
		return q[0]
	}
	return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}

// synonyms are the words agents reach for that this surface spells differently.
//
// Not aliases. Accepting both spellings would put a second name for one thing
// into a schema that is the only documentation an agent has, and the schema
// being the whole documentation is exactly why it must stay one word per
// concept. What was missing is not tolerance, it is a sentence: k7-b called
// close_space with `reason`, which records why a space was closed, and was told
// only that `reason` is not accepted. The word they wanted was `note`, the tool
// takes it, and nothing said so.
var synonyms = map[string]string{
	"reason":  "note",
	"why":     "note",
	"message": "body",
	"content": "body",
	"text":    "body",
	"id":      "space",
	"lane":    "space",
	"channel": "space",
	"target":  "agent",
	"to":      "agent",
	"name":    "agent",
}

// wrongTool maps a (tool, argument) that means the caller wanted a DIFFERENT
// tool, not a different parameter.
//
// k7-b reached for read_mail(agent:…) because read_mail reads like a lister,
// and for check_in(view:…) because `view` is real on `board`. Both refusals were
// correct and neither said where to go. Renaming read_mail would be the deeper
// fix and is a breaking change for every installed plugin, so the surface stays
// and the error carries the map.
var wrongTool = map[string]map[string]string{
	"read_mail": {
		"agent": "inbox lists your mail; read_mail fetches ONE message by msg_serial",
		"from":  "inbox lists your mail; read_mail fetches ONE message by msg_serial",
	},
	"check_in": {
		"view":   "`view` belongs to board(view: board|mail|activity); check_in always returns the whole checkpoint",
		"detail": "`detail` belongs to board(detail: true); check_in always returns the whole checkpoint",
	},
	"inbox": {
		"msg_serial": "read_mail(msg_serial) fetches one message; inbox lists them",
	},
}

// synonymHint names the parameter the caller probably meant, when this tool
// actually has one, or the tool they probably wanted.
func synonymHint(tool string, extra []string) string {
	known := map[string]bool{}
	for _, p := range knownParams[tool] {
		known[p] = true
	}
	var found []string
	for _, e := range extra {
		if where, ok := wrongTool[tool][e]; ok {
			found = append(found, where)
			continue
		}
		if want, ok := synonyms[e]; ok && known[want] {
			found = append(found, fmt.Sprintf("%q is %q here", e, want))
		}
	}
	if len(found) == 0 {
		return ""
	}
	return " (" + strings.Join(found, "; ") + ")"
}
