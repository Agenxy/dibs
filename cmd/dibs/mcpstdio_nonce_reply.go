package main

import "encoding/json"

// Persist selection only after the production reply proves register succeeded.
// A refusal from an old daemon or a lost/damaged reply never mints a fallback.
func rememberRegistrationNonce(line, reply []byte) {
	if toolNameOf(line) != "register" || agentTokenIn(reply) == "" || agentIDIn(reply) == "" {
		return
	}
	var call struct {
		Params struct {
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &call) != nil {
		return
	}
	args := call.Params.Arguments
	name, _ := args["name"].(string)
	if name == "" {
		return
	}
	nonce, _ := args["nonce"].(string)
	if group, ok := args["recovery_nonces"].([]any); ok {
		var result struct {
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if json.Unmarshal(reply, &result) != nil || len(result.Result.Content) != 1 {
			return
		}
		var chosen struct {
			Index *int `json:"recovery_nonce_index"`
		}
		if json.Unmarshal([]byte(result.Result.Content[0].Text), &chosen) != nil || chosen.Index == nil ||
			*chosen.Index < 0 || *chosen.Index >= len(group) {
			return
		}
		nonce, _ = group[*chosen.Index].(string)
	}
	if nonce != "" {
		rememberNonce(projectKey(args), name, nonce)
	}
}
