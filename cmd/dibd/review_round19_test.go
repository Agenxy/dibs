package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

// A demotion made through the admin API is a person's decision, and the
// reconciler's next tick leaves it alone. The handler has to say so; a plain
// grant from it would be indistinguishable from the reconciler's own.
func TestADemotionThroughTheAdminAPIStandsAgainstTheReconciler(t *testing.T) {
	eng, ctx := testEngine(t)
	registerAgent(t, eng, "fleet-lead")
	cfg := RolesConfig{
		Admin:    []string{"fleet-lead"},
		Identity: map[string]string{"fleet-lead": engine.RolePinFingerprint("nonce-fleet-lead")},
	}
	pins := loadRolePins(t.TempDir())
	applyDeclaredRoles(ctx, eng, cfg, pins)
	if !holdsRole(t, eng, "fleet-lead", core.RoleAdmin) {
		t.Fatal("setup: the declared admin was not granted")
	}
	id, err := eng.ResolveConfiguredAgent(ctx, "fleet-lead")
	if err != nil {
		t.Fatal("setup:", err)
	}
	mux := http.NewServeMux()
	registerAdminAPI(mux, eng)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/role",
		strings.NewReader(`{"agent":"`+id+`","role":"member"}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup: the demotion was refused: %d %s", rec.Code, rec.Body.String())
	}
	applyDeclaredRoles(ctx, eng, cfg, pins) // the next tick, before the restart
	if holdsRole(t, eng, "fleet-lead", core.RoleAdmin) {
		t.Fatal("the reconciler re-granted a role a person removed through the admin API")
	}
}
