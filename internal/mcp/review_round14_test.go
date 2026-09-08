package mcp

import (
	"context"
	"strings"
	"testing"
)

// R14-3: a send addressed to the coordinator by role carries the same
// pull-only warning a send to that agent by id does.
func TestSendingToTheCoordinatorByRoleCarriesTheWakeNote(t *testing.T) {
	srv, _ := newServer(t)
	eng := srv.Config.Handler.(*Server).eng
	lead := toolCall(t, srv, "register", map[string]any{"name": "lead", "cwd": t.TempDir()})
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	if _, err := eng.GrantRole(context.Background(), "lead", "coordinator"); err != nil {
		t.Fatal("setup:", err)
	}
	byID := toolCall(t, srv, "send", map[string]any{
		"token": asker["token"], "to": "lead", "type": "question", "body": "by id",
	})
	note, _ := byID["note"].(string)
	if !strings.Contains(note, "pull-only") {
		t.Fatalf("setup: a send to the unwakeable coordinator by id drew no pull-only note (%v), "+
			"so the role address below has nothing to match", byID)
	}
	byRole := toolCall(t, srv, "send", map[string]any{
		"token": asker["token"], "to": "coordinator", "type": "question", "body": "by role",
	})
	if n, _ := byRole["note"].(string); !strings.Contains(n, "pull-only") {
		t.Errorf("a send to the same agent addressed as \"coordinator\" carried no pull-only note: %v\n"+
			"  the sender is told nothing about a recipient that will not be woken", byRole)
	}
	_ = lead
}
