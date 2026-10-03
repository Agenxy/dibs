package main

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/engine"
)

func TestDoctorNamesPreAwayBridgeAndPrintsItsRemedy(t *testing.T) {
	hosts := hubHosts{bridges: []engine.HostBridgeInfo{
		{Host: "older-host"}, {Host: "updated-host", AwayOpen: 1},
	}}
	var warnings []string
	reportBridgeOpenPolicy(hosts, func(what, fix string) {
		warnings = append(warnings, what+": "+fix)
	})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "older-host") ||
		!strings.Contains(warnings[0], "restart dibs host-bridge") {
		t.Fatalf("diagnostic = %v", warnings)
	}
}
