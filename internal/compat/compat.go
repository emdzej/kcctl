// Package compat knows which admin API shapes each supported Keycloak release
// exposes. Commands ask for a capability, never for a version number, so the
// version knowledge stays in this one table.
package compat

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type Version struct {
	Major, Minor, Patch int
	// Snapshot marks nightly/main builds (999.0.0-SNAPSHOT), which are newer
	// than any release.
	Snapshot bool
	Raw      string
}

var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?`)

// Parse accepts upstream ("26.6.3"), nightly ("999.0.0-SNAPSHOT") and
// downstream ("26.6.3.redhat-00001") version strings.
func Parse(s string) (Version, error) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, fmt.Errorf("unrecognised Keycloak version %q", s)
	}
	v := Version{Raw: s}
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		v.Patch, _ = strconv.Atoi(m[3])
	}
	v.Snapshot = v.Major == 999 || strings.Contains(s, "SNAPSHOT")
	return v, nil
}

func MustParse(s string) Version {
	v, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

func (v Version) Compare(o Version) int {
	if v.Snapshot != o.Snapshot {
		if v.Snapshot {
			return 1
		}
		return -1
	}
	for _, d := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func (v Version) AtLeast(o Version) bool { return v.Compare(o) >= 0 }

func (v Version) String() string {
	if v.Raw != "" {
		return v.Raw
	}
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Minimum is the oldest release this CLI is written against.
var Minimum = MustParse("26.6.3")

type Capability string

const (
	// ServiceAccountUserEndpoint is GET clients/{id}/service-account-user,
	// removed in 27 in favour of ClientRepresentation.serviceAccountUserId.
	ServiceAccountUserEndpoint Capability = "service-account-user-endpoint"
	ServiceAccountUserIDField  Capability = "service-account-user-id-field"
	AvailableClientScopes      Capability = "available-client-scopes"
	OrgMembershipType          Capability = "organization-membership-type"
	UserVerifiableCredentials  Capability = "user-verifiable-credentials"
	// IdPSecretRebindCheck: IdP reads return clientSecret masked on every
	// supported version, and PUT swaps the mask back for the stored secret.
	// From 26.8 the server refuses that swap when the IdP's endpoints or
	// client id change, so the secret must be re-entered.
	IdPSecretRebindCheck Capability = "idp-secret-rebind-check"
	AdminAPIv2Clients    Capability = "admin-api-v2-clients"
	Organizations        Capability = "organizations"
	Workflows            Capability = "workflows"
	SCIM                 Capability = "scim"
)

type rule struct {
	since, until *Version // until is exclusive
	feature      string   // server feature flag that must be enabled
	summary      string
}

func v(s string) *Version { x := MustParse(s); return &x }

var rules = map[Capability]rule{
	ServiceAccountUserEndpoint: {until: v("27.0.0"), summary: "GET clients/{id}/service-account-user"},
	ServiceAccountUserIDField:  {since: v("27.0.0"), summary: "ClientRepresentation.serviceAccountUserId"},
	AvailableClientScopes:      {since: v("27.0.0"), summary: "GET clients/{id}/available-client-scopes"},
	OrgMembershipType:          {since: v("26.8.0"), feature: "ORGANIZATION", summary: "organization member membership-type"},
	UserVerifiableCredentials:  {since: v("26.8.0"), feature: "OID4VC_VCI", summary: "users/{id}/vc"},
	IdPSecretRebindCheck:       {since: v("26.8.0"), summary: "re-entering IdP secrets when endpoints change"},
	AdminAPIv2Clients:          {feature: "CLIENT_ADMIN_API_V2", summary: "/admin/api/{realm}/clients/v2"},
	Organizations:              {feature: "ORGANIZATION", summary: "organizations"},
	Workflows:                  {feature: "WORKFLOWS", summary: "workflows"},
	SCIM:                       {feature: "SCIM_API", summary: "SCIM v2 API"},
}

// Profile is what the CLI knows about one server.
type Profile struct {
	Version Version
	// Features maps feature flag name to enabled. Nil means the caller could
	// not read features (non-master admins), so flag checks are optimistic.
	Features map[string]bool
	// Providers maps an SPI name to its installed provider ids, as listed by
	// /admin/serverinfo. Nil means unknown (an older cache entry).
	Providers map[string][]string
}

// HasProvider reports whether spi/id is installed. known is false when the
// profile has no provider list, so callers can stay optimistic.
func (p *Profile) HasProvider(spi, id string) (has, known bool) {
	if p.Providers == nil {
		return false, false
	}
	for _, x := range p.Providers[spi] {
		if x == id {
			return true, true
		}
	}
	return false, true
}

func (p *Profile) Has(c Capability) bool {
	r, ok := rules[c]
	if !ok {
		return false
	}
	if r.since != nil && !p.Version.AtLeast(*r.since) {
		return false
	}
	if r.until != nil && p.Version.AtLeast(*r.until) {
		return false
	}
	if r.feature != "" && p.Features != nil {
		return p.Features[r.feature]
	}
	return true
}

// Unsupported explains why a capability is missing, in user terms.
func (p *Profile) Unsupported(c Capability) string {
	r := rules[c]
	if r.feature != "" && p.Features != nil && !p.Features[r.feature] {
		return fmt.Sprintf("%s needs the %s feature, which is disabled on this server", r.summary, r.feature)
	}
	switch {
	case r.since != nil && !p.Version.AtLeast(*r.since):
		return fmt.Sprintf("%s needs Keycloak %s or newer (server is %s)", r.summary, r.since, p.Version)
	case r.until != nil && p.Version.AtLeast(*r.until):
		return fmt.Sprintf("%s was removed in Keycloak %s (server is %s)", r.summary, r.until, p.Version)
	}
	return r.summary + " is not available"
}

// Capabilities lists every capability with its state, for `server info`.
func (p *Profile) Capabilities() map[Capability]bool {
	out := make(map[Capability]bool, len(rules))
	for c := range rules {
		out[c] = p.Has(c)
	}
	return out
}
