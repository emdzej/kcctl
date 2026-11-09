package sessioncmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New}

const aliceID = "6b9f3c2e-1111-4222-8333-944455556666"

func TestBruteForceClearAllNeedsYesWithoutTTY(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{}, "session", "brute-force", "clear-all")
	if r.Code != 1 || !strings.Contains(r.Stderr, "--yes") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	r = apptest.Run(t, build, apptest.Replies{
		"DELETE /admin/realms/test/attack-detection/brute-force/users": {},
	}, apptest.Options{}, "session", "brute-force", "clear-all", "--yes")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestBruteForceClearUserNoConfirm(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/users":                                            {Body: []any{map[string]any{"id": aliceID, "username": "alice"}}},
		"DELETE /admin/realms/test/attack-detection/brute-force/users/" + aliceID: {},
	}, apptest.Options{}, "session", "brute-force", "clear", "alice")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestBruteForceTopLevel(t *testing.T) {
	r := apptest.Run(t, []func(*app.App) *cobra.Command{BruteForce}, apptest.Replies{
		"GET /admin/realms/test/attack-detection/brute-force/users/" + aliceID: {Body: map[string]any{"disabled": true, "numFailures": 3}},
	}, apptest.Options{}, "brute-force", "status", aliceID, "-o", "json")
	if r.Code != 0 || r.JSON(t).(map[string]any)["disabled"] != true {
		t.Fatalf("exit %d: %s %s", r.Code, r.Stderr, r.Stdout)
	}
}

func TestLogoutAllConfirmsAndReports(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{}, "session", "logout-all")
	if r.Code != 1 || !strings.Contains(r.Stderr, "--yes") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	r = apptest.Run(t, build, apptest.Replies{
		"POST /admin/realms/test/logout-all": {Body: map[string]any{"successRequests": []any{"http://app/admin"}, "failedRequests": []any{}}},
	}, apptest.Options{}, "session", "logout-all", "-y")
	if r.Code != 0 || !strings.Contains(r.Stderr, "1 client(s)") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestDeleteOfflineSession(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"DELETE /admin/realms/test/sessions/s1": {},
	}, apptest.Options{}, "session", "delete", "s1", "--offline")
	if r.Code != 0 || r.Last(t).Query != "isOffline=true" {
		t.Fatalf("exit %d: %s query=%q", r.Code, r.Stderr, r.Last(t).Query)
	}
}
