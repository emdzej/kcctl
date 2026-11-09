package eventcmd

import (
	"net/url"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New}

const aliceID = "6b9f3c2e-1111-4222-8333-944455556666"

func TestListQueryParams(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/users":  {Body: []any{map[string]any{"id": aliceID, "username": "alice"}}},
		"GET /admin/realms/test/events": {Body: []any{map[string]any{"type": "LOGIN", "time": 1}}},
	}, apptest.Options{}, "event", "list", "--type", "LOGIN,LOGIN_ERROR", "--type", "LOGOUT", "--user", "alice",
		"--client", "web", "--ip", "10.0.0.1", "--date-from", "2026-01-01", "--date-to", "2026-01-31", "--max", "5", "--direction", "asc")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	q, _ := url.ParseQuery(r.Last(t).Query)
	want := url.Values{
		"type": {"LOGIN", "LOGIN_ERROR", "LOGOUT"}, "user": {aliceID}, "client": {"web"}, "ipAddress": {"10.0.0.1"},
		"dateFrom": {"2026-01-01"}, "dateTo": {"2026-01-31"}, "max": {"5"}, "direction": {"asc"},
	}
	if q.Encode() != want.Encode() {
		t.Errorf("query = %s\nwant    %s", q.Encode(), want.Encode())
	}
}

func TestAdminListQueryParams(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/master":            {Body: map[string]any{"id": "m-id", "realm": "master"}},
		"GET /admin/realms/master/users":      {Body: []any{map[string]any{"id": aliceID, "username": "admin"}}},
		"GET /admin/realms/test/admin-events": {Body: []any{}},
	}, apptest.Options{}, "event", "admin", "list", "--operation", "DELETE", "--resource-type", "USER",
		"--resource-path", "users/*", "--admin-realm", "master", "--auth-user", "admin")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	q, _ := url.ParseQuery(r.Last(t).Query)
	want := url.Values{"operationTypes": {"DELETE"}, "resourceTypes": {"USER"}, "resourcePath": {"users/*"}, "authRealm": {"m-id"}, "authUser": {aliceID}}
	if q.Encode() != want.Encode() {
		t.Errorf("query = %s\nwant    %s", q.Encode(), want.Encode())
	}
}

func TestClearNeedsYesWithoutTTY(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{}, "event", "clear")
	if r.Code != 1 || !strings.Contains(r.Stderr, "--yes") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	r = apptest.Run(t, build, apptest.Replies{"DELETE /admin/realms/test/admin-events": {}}, apptest.Options{}, "event", "admin", "clear", "--yes")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestConfigUpdateOverlays(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/events/config": {Body: map[string]any{"eventsEnabled": false, "eventsListeners": []any{"jboss-logging"}, "enabledEventTypes": []any{"LOGIN"}}},
		"PUT /admin/realms/test/events/config": {},
	}, apptest.Options{}, "event", "config", "update", "--enabled=true", "--admin-events=true", "--expiration", "3600")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["eventsEnabled"] != true || b["adminEventsEnabled"] != true || b["eventsExpiration"] != float64(3600) || b["eventsListeners"].([]any)[0] != "jboss-logging" {
		t.Errorf("body = %v", b)
	}
}
