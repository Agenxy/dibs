package main

import "strings"

type configuredNameAddress struct {
	Name      string   `json:"name"`
	ID        string   `json:"id,omitempty"`
	Via       string   `json:"via"`
	Shadowed  []string `json:"shadowed_aliases,omitempty"`
	Ambiguous []string `json:"ambiguous,omitempty"`
}

func checkConfiguredNameAliases(b *boardView, ok reportFn, warn fixFn) {
	for _, address := range b.ConfiguredNames {
		switch {
		case len(address.Shadowed) > 0:
			warn("configured role name "+address.Name+" now resolves to "+address.ID+
				" and shadows former names of "+strings.Join(address.Shadowed, ", "),
				"use the intended immutable agent id in dibs.toml; keep its identity fingerprint pinned")
		case len(address.Ambiguous) > 0:
			warn("configured role name "+address.Name+" is an ambiguous former name of "+strings.Join(address.Ambiguous, ", "),
				"choose the intended immutable agent id in dibs.toml, with its identity fingerprint")
		case address.Via == "alias":
			ok("configured role name " + address.Name + " reaches " + address.ID + " through its former-name alias")
		}
	}
}
