package flowcmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New}

const (
	flowID = "0b1c2d3e-1111-4222-8333-944455556666"
	execID = "7a8b9c0d-1111-4222-8333-944455556666"
)

func TestGetByAliasResolvesID(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/authentication/flows": {Body: []any{
			map[string]any{"id": "other", "alias": "browser"},
			map[string]any{"id": flowID, "alias": "my-browser", "providerId": "basic-flow", "topLevel": true},
		}},
		"GET /admin/realms/test/authentication/flows/" + flowID: {Body: map[string]any{"id": flowID, "alias": "my-browser"}},
	}, apptest.Options{}, "flow", "get", "my-browser", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if got := r.JSON(t).(map[string]any)["id"]; got != flowID {
		t.Errorf("id = %v", got)
	}
}

func TestCreateDefaults(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST /admin/realms/test/authentication/flows": {Status: 201, Header: map[string]string{"Location": "{server}/admin/realms/test/authentication/flows/" + flowID}},
	}, apptest.Options{}, "flow", "create", "my-browser", "--description", "x")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["alias"] != "my-browser" || b["providerId"] != "basic-flow" || b["topLevel"] != true || b["builtIn"] != false {
		t.Errorf("body = %v", b)
	}
}

func TestExecutionsByFlowIDUseAlias(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/authentication/flows/" + flowID:               {Body: map[string]any{"id": flowID, "alias": "my browser"}},
		"GET /admin/realms/test/authentication/flows/my%20browser/executions": {Body: []any{}},
	}, apptest.Options{}, "flow", "execution", "list", flowID)
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestSetRequirementPutsExecutionRep(t *testing.T) {
	exec := map[string]any{"id": execID, "providerId": "auth-otp-form", "displayName": "OTP Form",
		"requirement": "DISABLED", "level": 0, "index": 1, "priority": 20, "requirementChoices": []any{"REQUIRED", "ALTERNATIVE", "DISABLED"}}
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/authentication/flows/my-browser/executions": {Body: []any{
			map[string]any{"id": "e0", "providerId": "auth-cookie", "requirement": "ALTERNATIVE"}, exec,
		}},
		"PUT /admin/realms/test/authentication/flows/my-browser/executions": {},
	}, apptest.Options{}, "flow", "execution", "set-requirement", "my-browser", "auth-otp-form", "required")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["id"] != execID || b["requirement"] != "REQUIRED" || b["priority"] != float64(20) {
		t.Errorf("body = %v", b)
	}
}

func TestSetRequirementRejectsUnknown(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{}, "flow", "execution", "set-requirement", "f", "e", "OPTIONAL")
	if r.Code != 1 || !strings.Contains(r.Stderr, "CONDITIONAL") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestCopyPostsNewName(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST /admin/realms/test/authentication/flows/browser/copy": {Status: 201, Header: map[string]string{"Location": "{server}/admin/realms/test/authentication/flows/" + flowID}},
	}, apptest.Options{}, "flow", "copy", "browser", "my-browser")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if b := r.Last(t).Body.(map[string]any); b["newName"] != "my-browser" {
		t.Errorf("body = %v", b)
	}
}

func TestExecutionNameNeedsFlow(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{}, "flow", "execution", "raise", "auth-otp-form")
	if r.Code != 1 || !strings.Contains(r.Stderr, "--flow") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestConfigSetCreatesWhenMissing(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/authentication/executions/" + execID:              {Body: map[string]any{"id": execID, "authenticator": "auth-otp-form"}},
		"POST /admin/realms/test/authentication/executions/" + execID + "/config": {Status: 201, Header: map[string]string{"Location": "{server}/x/cfg1"}},
	}, apptest.Options{}, "flow", "execution", "config", "set", execID, "--alias", "otp", "--config", "k=v")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["alias"] != "otp" || b["config"].(map[string]any)["k"] != "v" {
		t.Errorf("body = %v", b)
	}
}

func TestRequiredActionDisableSendsFullRep(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/authentication/required-actions/CONFIGURE_TOTP": {Body: map[string]any{"alias": "CONFIGURE_TOTP", "name": "Configure OTP", "enabled": true, "priority": 10}},
		"PUT /admin/realms/test/authentication/required-actions/CONFIGURE_TOTP": {},
	}, apptest.Options{}, "flow", "required-action", "disable", "CONFIGURE_TOTP")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if b["enabled"] != false || b["priority"] != float64(10) || b["name"] != "Configure OTP" {
		t.Errorf("body = %v", b)
	}
}
