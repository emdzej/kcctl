package orgcmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/apptest"
)

var build = []func(*app.App) *cobra.Command{New}

const (
	orgID  = "1a2b3c4d-1111-4222-8333-944455556666"
	userID = "6b9f3c2e-1111-4222-8333-944455556666"
)

var orgsOn = apptest.Options{Features: map[string]bool{"ORGANIZATION": true}}

func TestCreateBuildsDomainObjects(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST /admin/realms/test/organizations": {Status: 201, Header: map[string]string{"Location": "{server}/admin/realms/test/organizations/" + orgID}},
	}, orgsOn, "org", "create", "acme", "--domain", "acme.com,acme.io", "--attr", "tier=gold")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	b := r.Last(t).Body.(map[string]any)
	if _, leaked := b[domainsField]; leaked || b["name"] != "acme" {
		t.Errorf("body = %v", b)
	}
	d := b["domains"].([]any)
	if len(d) != 2 || d[0].(map[string]any)["name"] != "acme.com" || d[1].(map[string]any)["name"] != "acme.io" {
		t.Errorf("domains = %v", d)
	}
}

func TestUpdateKeepsVerifiedDomain(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/organizations/" + orgID: {Body: map[string]any{"id": orgID, "name": "acme",
			"domains": []any{map[string]any{"name": "acme.com", "verified": true}, map[string]any{"name": "old.com"}}}},
		"PUT /admin/realms/test/organizations/" + orgID: {},
	}, orgsOn, "org", "update", orgID, "--domain", "acme.com", "--domain", "new.com")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	d := r.Last(t).Body.(map[string]any)["domains"].([]any)
	if len(d) != 2 || d[0].(map[string]any)["verified"] != true || d[1].(map[string]any)["name"] != "new.com" {
		t.Errorf("domains = %v", d)
	}
}

func TestFeatureDisabledIsClearError(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{Features: map[string]bool{"ORGANIZATION": false}},
		"org", "list")
	if r.Code != 1 || !strings.Contains(r.Stderr, "ORGANIZATION feature") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if len(r.Sent) != 0 {
		t.Errorf("sent %v", r.Sent)
	}
}

func TestMembershipTypeNeeds268(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{Version: "26.6.3"},
		"org", "member", "membership-type", orgID, userID, "MANAGED")
	if r.Code != 1 || !strings.Contains(r.Stderr, "26.8.0 or newer") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestMembershipTypeOn268(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"PUT /admin/realms/test/organizations/" + orgID + "/members/" + userID + "/membership-type": {},
	}, apptest.Options{Version: "26.8.1"}, "org", "member", "membership-type", orgID, userID, "unmanaged")
	if r.Code != 0 || r.Last(t).Body != "UNMANAGED" {
		t.Fatalf("exit %d: %s body=%v", r.Code, r.Stderr, r.Last(t).Body)
	}
}

func TestMemberAddSendsBareID(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/organizations":                        {Body: []any{map[string]any{"id": orgID, "name": "acme"}}},
		"GET /admin/realms/test/users":                                {Body: []any{map[string]any{"id": userID, "username": "alice"}}},
		"POST /admin/realms/test/organizations/" + orgID + "/members": {Status: 201},
	}, apptest.Options{}, "org", "member", "add", "acme", "alice")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if !strings.Contains(r.Sent[0].Query, "exact=true") || r.Last(t).Body != userID {
		t.Errorf("sent = %v", r.Sent)
	}
}

func TestRemoveManagedMemberNeedsYes(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/organizations/" + orgID + "/members/" + userID: {Body: map[string]any{"id": userID, "membershipType": "MANAGED"}},
	}, apptest.Options{}, "org", "member", "remove", orgID, userID)
	if r.Code != 1 || !strings.Contains(r.Stderr, "deleted from realm") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestInviteIsFormEncoded(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"POST /admin/realms/test/organizations/" + orgID + "/members/invite-user": {},
	}, apptest.Options{}, "org", "member", "invite", orgID, "--email", "bob@acme.com", "--first-name", "Bob")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if b, _ := r.Last(t).Body.(string); !strings.Contains(b, "email=bob%40acme.com") || !strings.Contains(b, "firstName=Bob") {
		t.Errorf("body = %v", r.Last(t).Body)
	}
}

func TestDeleteNeedsYes(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{}, apptest.Options{}, "org", "delete", orgID)
	if r.Code != 1 || !strings.Contains(r.Stderr, "--yes") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
}

func TestIdPListShowsDomain(t *testing.T) {
	r := apptest.Run(t, build, apptest.Replies{
		"GET /admin/realms/test/organizations/" + orgID + "/identity-providers": {Body: []any{map[string]any{
			"alias": "corp", "providerId": "oidc", "enabled": true, "config": map[string]any{"kc.org.domain": "acme.com"}}}},
	}, apptest.Options{}, "org", "idp", "list", orgID)
	if r.Code != 0 || !strings.Contains(r.Stdout, "acme.com") {
		t.Fatalf("exit %d: %s\n%s", r.Code, r.Stderr, r.Stdout)
	}
}
