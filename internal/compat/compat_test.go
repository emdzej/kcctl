package compat

import "testing"

func TestParseAndCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"26.6.3", "26.6.3", 0},
		{"26.6.3", "26.6.4", -1},
		{"26.8.1", "26.6.3", 1},
		{"27.0.0", "26.8.1", 1},
		{"999.0.0-SNAPSHOT", "27.5.0", 1},
		{"26.6.3.redhat-00001", "26.6.3", 0},
		{"26.6", "26.6.0", 0},
	}
	for _, c := range cases {
		if got := MustParse(c.a).Compare(MustParse(c.b)); got != c.want {
			t.Errorf("%s vs %s = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if _, err := Parse("nightly"); err == nil {
		t.Error("expected an error for a non-version")
	}
}

func TestCapabilitiesFollowVersion(t *testing.T) {
	old := &Profile{Version: MustParse("26.6.3")}
	mid := &Profile{Version: MustParse("26.8.1")}
	next := &Profile{Version: MustParse("27.0.0")}
	nightly := &Profile{Version: MustParse("999.0.0-SNAPSHOT")}
	check := func(p *Profile, c Capability, want bool) {
		t.Helper()
		if p.Has(c) != want {
			t.Errorf("%s Has(%s) = %v, want %v", p.Version, c, !want, want)
		}
	}
	check(old, ServiceAccountUserEndpoint, true)
	check(old, ServiceAccountUserIDField, false)
	check(mid, ServiceAccountUserEndpoint, true)
	check(next, ServiceAccountUserEndpoint, false)
	check(nightly, ServiceAccountUserIDField, true)
	check(old, UserVerifiableCredentials, false)
	check(mid, UserVerifiableCredentials, true)
	check(old, OrgMembershipType, false)
	check(mid, OrgMembershipType, true)
}

func TestFeatureFlags(t *testing.T) {
	p := &Profile{Version: MustParse("26.8.1"), Features: map[string]bool{"ORGANIZATION": false, "CLIENT_ADMIN_API_V2": true}}
	if p.Has(Organizations) || p.Has(OrgMembershipType) {
		t.Error("disabled ORGANIZATION feature should hide organization capabilities")
	}
	if !p.Has(AdminAPIv2Clients) {
		t.Error("enabled feature should be available")
	}
	if msg := p.Unsupported(Organizations); msg == "" || msg[:13] != "organizations" {
		t.Errorf("message = %q", msg)
	}
	// Without feature visibility, flag checks are optimistic.
	if !(&Profile{Version: MustParse("26.6.3")}).Has(Organizations) {
		t.Error("unknown features should not block")
	}
}
