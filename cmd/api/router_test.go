package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/celikbros/kapsora/internal/platform/config"
)

// Construct the complete production route tree without a database or listener.
// Module-level handler tests cannot detect overlapping router mounts at startup.
func TestRouterStartupAndManagementRoutes(t *testing.T) {
	router := newRouter(routerDeps{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg: config.Config{Invitations: config.InvitationConfig{
			LinkBase: "http://127.0.0.1:5181",
		}},
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("liveness status = %d, want 200", response.Code)
	}

	routes := make(map[string]bool)
	if err := chi.Walk(router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		"GET /api/v1/admin/tenant-settings/accommodation",
		"PUT /api/v1/admin/tenant-settings/accommodation",
		"GET /api/v1/admin/users/",
		"GET /api/v1/admin/role-assignment-options",
		"GET /api/v1/admin/role-change-requests",
		"GET /api/v1/admin/invitations/",
	} {
		if !routes[route] {
			t.Errorf("missing management route %s", route)
		}
	}
}
