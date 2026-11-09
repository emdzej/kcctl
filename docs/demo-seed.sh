#!/usr/bin/env bash
# Seeds the demo realm that docs/interactive.tape records against.
# Expects a throwaway Keycloak on http://localhost:8080 with the bootstrap
# admin user admin/admin, e.g.:
#   KC_BOOTSTRAP_ADMIN_USERNAME=admin KC_BOOTSTRAP_ADMIN_PASSWORD=admin bin/kc.sh start-dev
set -euo pipefail

# Keep the demo's contexts, session and history away from your own.
export XDG_CONFIG_HOME=/tmp/kcdemo/cfg XDG_STATE_HOME=/tmp/kcdemo/state KCCTL_NO_KEYRING=1
rm -rf /tmp/kcdemo

kcctl config set-context demo --server http://localhost:8080 --username admin -q
printf admin | kcctl auth login -q
# A long token keeps the status line from counting down a 60 s lifetime.
kcctl -r master realm update master -s accessTokenLifespan=1800 -q
printf admin | kcctl auth login -q

kcctl realm create acme --display-name "ACME Corp" -q
kcctl realm use acme -q
for u in "alice Alice Smith" "bob Bob Jones" "carol Carol White" "dave Dave Brown" "erin Erin Green"; do
  set -- $u
  kcctl user create "$1" --email "$1@acme.io" --first-name "$2" --last-name "$3" -q
done
kcctl role create admin --description "Full access" -q
kcctl role create auditor --description "Read-only access" -q
kcctl role create support --description "Customer support" -q
kcctl group create staff -q
kcctl group create devs --parent staff -q
kcctl group create ops --parent staff -q
kcctl user group add alice /staff/devs -q
kcctl user group add bob /staff/ops -q
kcctl client create web --public=true --redirect-uri 'https://app.acme.io/*' -q
kcctl client create api --public=false --service-accounts=true -q
kcctl server info >/dev/null   # caches the version for the status line
echo "demo realm ready"
