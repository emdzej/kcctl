# kcctl

A fast, single-binary command-line client for the Keycloak admin REST API,
for Keycloak 26.6.3 and newer.

```console
$ kcctl user list --search ali
USERNAME   EMAIL            FIRST NAME   LAST NAME   ENABLED   ID
alice      alice@acme.io    Alice        Smith       true      6b9f3c2e-…
$ kcctl user role add alice realm-admin --client realm-management
Added 1 client 'realm-management' role(s) to user 'alice'.
```

## Why

Keycloak already ships an admin CLI, `kcadm.sh`. kcctl exists because:

- **kcadm is heavy.** It is a Java program, so every invocation starts a
  JVM. A script that runs a few dozen commands spends most of its time
  starting Java. kcctl is a native Go binary that starts in milliseconds.
- **kcadm is only shipped inside the Keycloak distribution.** To get it on a
  laptop or a CI runner you download the whole server archive (and need a
  JRE). kcctl is one self-contained file for Linux, macOS and Windows
  (amd64/arm64) with no runtime dependencies.
- **kcadm's UX could be better.** It is a thin wrapper over raw URIs
  (`kcadm.sh get users/<uuid>/role-mappings/realm -r acme`): you have to know
  endpoint paths, look up internal UUIDs yourself, and read raw JSON. kcctl
  aims for a much better experience:
  - resource-oriented commands (`kcctl user role add alice admin`) that accept
    names wherever the API wants UUIDs;
  - readable tables by default, plus `json`, `yaml`, `jsonpath` and
    `custom-columns` output for scripts;
  - shell completion that fetches real realms, users, clients and roles from
    the server;
  - an interactive mode with live, server-aware suggestions;
  - stable exit codes, clear errors with hints, and `--no-input` for scripts;
  - one tool across Keycloak versions: kcctl detects the server version and
    picks the right endpoint shape where the API changed between releases.

## Install

Download from the [releases page](https://github.com/emdzej/kcctl/releases)
either the bare binary (`kcctl_<version>_<os>_<arch>`, `.exe` on Windows) or
the archive with the README, verify it against `checksums.txt`, and put it on
your `PATH`:

```sh
curl -fsSLo kcctl https://github.com/emdzej/kcctl/releases/download/0.1.0/kcctl_0.1.0_linux_amd64
chmod +x kcctl && sudo mv kcctl /usr/local/bin/
```

Or build from source (Go 1.27+):

```sh
go install github.com/emdzej/kcctl/cmd/kcctl@latest
# or
make build        # -> bin/kcctl
make dist         # -> dist/kcctl_<os>_<arch> for all six platforms
```

Shell completion:

```sh
kcctl completion zsh > "${fpath[1]}/_kcctl"      # zsh
kcctl completion bash > /etc/bash_completion.d/kcctl
kcctl completion fish > ~/.config/fish/completions/kcctl.fish
kcctl completion powershell | Out-String | Invoke-Expression
```

## Quick guide

### 1. Connect and sign in

A **context** remembers a server, the realm you sign in to, and the realm
your commands act on.

```sh
# Admin with a password (stock admin-cli client):
kcctl config set-context local --server http://localhost:8080 --username admin
kcctl auth login                       # prompts for the password (hidden)

# Admin who signs in through an identity provider or passkeys:
# create the 'kcctl' client once (see "Browser sign-in" below), then
kcctl config set-context sso --server https://sso.example.com --grant browser --client-id kcctl
kcctl auth login                       # opens your browser

kcctl auth status                      # who am I, which realm, when does it expire
kcctl server info                      # Keycloak version and what it supports
```

### 2. Pick the realm to work on

```sh
kcctl realm list
kcctl realm create acme --display-name "ACME"
kcctl realm use acme                   # default target realm for this context
kcctl -r other-realm user list         # or override per command
```

### 3. Everyday tasks

```sh
# Users
kcctl user create alice --email alice@acme.io --first-name Alice --group /staff
kcctl user set-password alice --temporary          # hidden prompt, or pipe it in
kcctl user list --search ali
kcctl user update alice --enabled=false
kcctl user send-actions-email alice UPDATE_PASSWORD

# Groups and roles (names work everywhere; no UUIDs needed)
kcctl group create staff
kcctl group create devs --parent staff
kcctl group member add /staff/devs alice
kcctl role create auditor --description "Read-only auditors"
kcctl user role add alice auditor
kcctl group role add staff auditor

# Clients
kcctl client create web --public=true --redirect-uri 'https://app.acme.io/*'
kcctl client create api --public=false --service-accounts=true
kcctl client secret get api
kcctl client role create reader --client api
kcctl user role add alice reader --client api

# Identity providers, organizations, flows, events
kcctl idp create corp --provider-id oidc --client-secret-file ./secret \
  --config clientId=kc --config authorizationUrl=https://corp/auth --config tokenUrl=https://corp/token
kcctl org create acme-inc --domain acme.io
kcctl flow copy browser my-browser
kcctl event list --type LOGIN_ERROR

# Backup
kcctl realm export acme --clients --groups-and-roles --out acme.json
```

Every command has `--help` with examples, and Tab completes command names,
flags and live server data (`kcctl completion --help` shows how to set it up).

### 4. Scripting

```sh
kcctl user list --all -o json                                  # {"data": [...], "error": null}
kcctl user list -o jsonpath='$[*].username'
kcctl user list -o custom-columns=USER:.username,DEPT:.attributes.dept[0]
id=$(kcctl user create bob -o jsonpath='$.id')                 # capture new ids
kcctl --no-input user delete alice bob --yes                   # never prompt; confirm explicitly
kcctl api GET users -Q briefRepresentation=true                # any endpoint, same session
curl -H "$(kcctl token --header)" "$KC/admin/realms/acme/users"
```

Check `$?` for the failure class: 2 not signed in, 3 rejected by validation,
4 not found, 5 forbidden (full table under [Exit codes](#exit-codes)).

### 5. Interactive mode

```sh
kcctl interactive --context sso -r acme
```

A prompt with suggestions as you type, including users, clients and roles
fetched from the server. See [Interactive mode](#interactive-mode).

## Concepts

### Contexts

Contexts live in `~/.config/kcctl/config.yaml` (`$XDG_CONFIG_HOME`,
`%AppData%` on Windows; override with `KCCTL_CONFIG`). Each has a server URL,
the **auth realm** you sign in to (default `master`), the **target realm**
commands act on (default: the auth realm), the client (default `admin-cli`)
and the grant.

Every value resolves as **flag > environment > context > default**:

| Flag | Environment | Meaning |
|---|---|---|
| `--context` | `KCCTL_CONTEXT` | context to use |
| `--server` | `KCCTL_SERVER` | Keycloak base URL |
| `-r, --realm` | `KCCTL_REALM` | realm to act on |
| `--auth-realm` | `KCCTL_AUTH_REALM` | realm to sign in to |
| `--client-id` | `KCCTL_CLIENT_ID` | sign-in client |
| `--token` | `KCCTL_TOKEN` | use this access token, skip the stored session |

```sh
kcctl config get-contexts
kcctl config use-context prod
kcctl --context prod -r tenant-a user list
```

### Signing in

```sh
kcctl auth login --grant browser --client-id kcctl # sign in in your browser
kcctl auth login                                   # password grant (prompt)
kcctl auth login --grant device                    # approve with a code (SSH)
kcctl auth login --grant client_credentials \
  --client-id ci --client-secret-file ./secret     # service account, for CI
kcctl auth status        # no network: who, which realm, when it expires
kcctl auth whoami        # token claims and admin roles
kcctl auth logout        # ends the session on the server too
```

#### Browser sign-in (accounts without a password)

If your admin account signs in through an identity provider (Google, Entra
ID, another Keycloak, …) or with passkeys, it has no password the password
grant could use. Use `--grant browser`: kcctl opens the Keycloak login page
in your browser, you sign in as usual, and Keycloak redirects back to a
temporary listener on `127.0.0.1` (authorization code flow with PKCE).

The stock `admin-cli` client doesn't allow browser sign-in (its standard
flow is off), so create a client for kcctl once. You only do this once per
Keycloak, and you can do it in the admin console with your normal browser
sign-in.

##### Create the client in the admin console

Pick the realm your **admin account** lives in (top-left realm selector,
usually `master`), then **Clients → Create client** and fill in the three
screens of the wizard:

1. **General settings**
   - Client type: `OpenID Connect`
   - Client ID: `kcctl` (any name works; you pass it as `--client-id`)
2. **Capability config**
   - Client authentication: **Off** (a public client; a CLI can't keep a secret)
   - Authentication flow: **Standard flow** on; untick *Direct access grants*
     and the others, kcctl doesn't need them
   - Require PKCE: **On** (kcctl always uses PKCE with S256)
3. **Login settings**
   - Valid redirect URIs: `http://127.0.0.1/*`
   - leave Root URL, Home URL and Web origins empty

Save. Keycloak treats loopback redirect URIs specially (RFC 8252): the
registered `http://127.0.0.1/*` matches **any port**, which is what kcctl
needs, because it listens on a random free port for each sign-in.

Leave *Full scope allowed* (client **Client scopes** tab → `kcctl-dedicated`
→ **Scope**) at its default **On**: kcctl's tokens must carry your admin
roles, and with full scope off they won't.

##### Point a context at it and sign in

```sh
kcctl config set-context sso --server https://sso.example.com --grant browser --client-id kcctl
kcctl auth login          # opens the browser; sign in with your IdP as usual
kcctl auth status         # state: signed in, grant: browser
```

If your admin account lives in a realm other than `master` (a realm
administrator rather than a server-wide one), create the client in that
realm and add `--auth-realm <that-realm>` to `set-context`.

##### Or create the client with kcctl

If you can already sign in some other way (the bootstrap `admin` password,
another context), kcctl can create the same client:

```sh
kcctl -r master client create kcctl --public=true --standard-flow=true \
  --direct-access=false --redirect-uri 'http://127.0.0.1/*' \
  --attr pkce.code.challenge.method=S256
```

##### When it doesn't work

| What you see | Cause | Fix |
|---|---|---|
| Browser: *Invalid parameter: redirect_uri* (kcctl keeps waiting) | the client has no `http://127.0.0.1/*` redirect URI (`localhost` doesn't match, kcctl redirects to `127.0.0.1`) | add exactly `http://127.0.0.1/*`, press Ctrl-C and log in again |
| Browser: *Client not found* | wrong `--client-id`, or the client is in another realm than `--auth-realm` | check the client id and the realm |
| `sign-in rejected: unauthorized_client: … Standard flow is disabled for the client.` | Standard flow is off on the client | turn on Standard flow |
| `sign-in rejected (401): unauthorized_client: Invalid client or Invalid client credentials` | Client authentication is on (confidential client) | turn it off, or pass `--client-secret-file` |
| signed in, but commands fail with 403 | your account lacks admin roles, or Full scope allowed is off | check `kcctl auth whoami`; give the user `admin` (master) or `realm-management` roles |
| `sign-in was not completed within 5m0s` | the browser never came back to kcctl | see the first row; on a remote machine, see below |

On a machine without a browser (SSH), add `--no-browser` and open the
printed link on the same machine, or enable "OAuth 2.0 Device Authorization
Grant" on the client and use `--grant device` instead.

Sessions are stored per context in the OS keychain, or in a 0600 file under
`~/.local/state/kcctl/` when no keychain is available (`KCCTL_NO_KEYRING=1`
forces the file). Tokens refresh automatically.

Secrets (passwords, client secrets) are only ever read from a
`--*-file` option, piped stdin, a hidden prompt, or `KCCTL_PASSWORD` /
`KCCTL_CLIENT_SECRET` — never from a flag value, so they don't end up in
shell history or process listings.

### Names instead of UUIDs

Wherever the API wants an internal id, kcctl accepts the human name:
usernames, `clientId`s, role names, group names or paths (`/staff/devs`),
client scope names, IdP aliases, flow aliases. A UUID is used as-is without
an extra lookup.

### Output

Results go to stdout, messages and errors to stderr.

| `-o` | |
|---|---|
| *(default)* | tables for lists, field/value view for one resource |
| `vtable` | field/value view for every item |
| `json`, `yaml` | `{"data": …, "error": null}` envelope, also on failure |
| `jsonpath=<expr>` | e.g. `-o jsonpath='$[*].id'` (kubectl's `{.id}` form works too) |
| `custom-columns=H:path,…` | e.g. `-o custom-columns=USER:.username,DEPT:.attributes.dept[0]` |

Colors follow `--color auto|always|never`, `NO_COLOR`, `FORCE_COLOR` and
`CLICOLOR`, decided separately for stdout and stderr.

### Request bodies

`create` and `update` take typed flags for common fields, `-s path=value`
for any field (dotted paths, JSON values: `-s attributes.dept='["sales"]'`),
and `-f file` with a full JSON/YAML representation (`-f -` for stdin). They
combine: the file is read first, then `-s`, then typed flags.

`update` reads the current representation, applies your changes and writes
the whole thing back, so fields kcctl doesn't know about (from newer
servers) are preserved.

### Confirmation

Operations that remove or replace more than the one thing named on the line
(deleting several users, deleting a realm, `realm import --if-exists
OVERWRITE`, `session logout-all`, …) ask first. Without a terminal they
refuse unless you pass `--yes`. Deleting one named resource does not ask.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | usage error (bad flags, missing arguments, not confirmed) |
| 2 | authentication (not signed in, session expired, 401) |
| 3 | validation (400, 409, 422) |
| 4 | not found |
| 5 | permission denied (403) |
| 6 | server error (5xx) |
| 7 | network error |

`-v` traces each request (method, URL, status, duration, request id);
`-vv` adds headers and bodies with secrets masked.

## Interactive mode

![kcctl interactive mode: commands, users, roles and group paths completing as you type](docs/interactive.gif)

```sh
kcctl interactive                    # or: kcctl i
kcctl interactive --context prod -r acme
```

A prompt with suggestions as you type: commands, flags and live server data
(realms, users, clients, roles, groups, …), shown as dim ghost text plus a
dropdown. Unknown commands and flags are marked red as you type. A status
line before each prompt shows the context, server, realm, Keycloak version,
signed-in user and token expiry; production-looking contexts are red.

Global flags given when starting the session apply to every line; the same
flag typed on a line wins. Each line runs as its own `kcctl` process, so it
behaves exactly as it would in a shell.

| Key | |
|---|---|
| Tab, → | accept the suggestion |
| ↑/↓, Shift-Tab | choose a suggestion, or walk history when none is shown |
| Ctrl-R | search history |
| Ctrl-P / Ctrl-N | previous / next history entry |
| Enter | run (an open quote or trailing `\` continues the line) |
| Alt-Enter | insert a newline |
| Esc | hide suggestions |
| Ctrl-C | clear the line; on an empty line, exit |
| Ctrl-D, `exit` | leave |

History is kept in `~/.local/state/kcctl/history`; lines that look like they
carry a credential are never written to disk.

## Commands

| Group | What |
|---|---|
| `realm` | list/get/create/update/delete, `use`, `export`, `import`, `default-scope`, `default-group` |
| `user` | CRUD, `count`, `enable`/`disable`, `set-password`, `send-actions-email`, `send-verify-email`, `logout`, `sessions`, `credential`, `group`, `role`, `idp-link`, `impersonate`, `vc` |
| `client` | CRUD, `enable`/`disable`, `secret`, `service-account`, `role`, `mapper`, `scope`, `scope-mapping`, `installation`, `sessions`, `evaluate` |
| `role` | realm roles: CRUD, `composite`, `users`, `groups` |
| `group` | CRUD (by id, path or name), `--tree`, `count`, `children`, `member`, `role` |
| `client-scope` | CRUD, `mapper`, `scope-mapping` |
| `idp` | identity providers: CRUD, `mapper`, `import-config` |
| `component` / `user-storage` | components (LDAP, keys, …), user-storage `sync` and friends |
| `org` | organizations: CRUD, `count`, `member`, `idp`, `invitation` |
| `flow` | authentication flows, `execution`, `required-action`, `authenticators` |
| `event` | login events and admin events: list, clear, config |
| `session` | `stats`, `delete`, `logout-all`, `push-revocation`, `brute-force` |
| `key` | realm keys and key providers |
| `client-policy` | client policies and profiles |
| `workflow` | workflows (when the feature is enabled) |
| `server` | `info`, `features`, `serverinfo` |
| `api` | call any endpoint: `kcctl api GET users -Q search=al`, `kcctl api POST groups -d @g.yaml` |
| `token` | print a valid access token: `curl -H "$(kcctl token --header)" …` |
| `extension` | `list`, `discover`, `install`, `remove`, `show`: commands added by SPIs ([Extensions](#extensions)) |

Every command has `--help` with examples.

## Extensions

If your Keycloak runs SPIs that add admin endpoints, such as
[keycloak-api-keys](https://github.com/emdzej/keycloak-api-keys), kcctl can add
commands for them. An SPI describes its commands in a manifest it serves itself,
and kcctl picks the manifest up when you sign in:

```sh
kcctl auth login                  # discovers extensions on the server
kcctl extension list
kcctl api-key create alice --name ci --client my-api
```

These commands work like the built-in ones: names instead of UUIDs,
completion, `-o json`, and secrets read only from files or prompts. A manifest
is data, not code, and can only call the same server's admin API.

You can also install a manifest by hand (`kcctl extension install file.yaml`).
Any `kcctl-<name>` executable on `PATH` runs as `kcctl <name>`.

[docs/extensions.md](docs/extensions.md) explains how this works, how to add
kcctl support to your SPI, and the manifest format.

## Keycloak versions

kcctl supports Keycloak **26.6.3 and newer**. On first use per server it
reads `/admin/serverinfo` (cached for 6 hours; `kcctl server info --refresh`
re-reads it) and adapts where the admin API changed, for example:

| Difference | Handling |
|---|---|
| `clients/{id}/service-account-user` removed in 27 | `client service-account` uses `serviceAccountUserId` on 27+, the old endpoint before |
| `clients/{id}/available-client-scopes` added in 27 | `client scope available` computes the list itself on older servers |
| organization member `membership-type` (26.8+), user verifiable credentials (26.8+) | commands explain the minimum version instead of failing with a bare 404 |
| identity-provider secrets are masked on read; from 26.8 the server refuses the mask when the IdP's endpoints change | `idp update` keeps the stored secret; pass `--client-secret-file` when changing endpoints |
| feature flags (`ORGANIZATION`, `WORKFLOWS`, `CLIENT_ADMIN_API_V2`, …) | gated commands say which feature is disabled |

`kcctl server info` lists every version-dependent capability and whether
the server has it. Older servers get a warning; most commands still work.

## Development

See [AGENTS.md](AGENTS.md) for layout and conventions.

```sh
make test     # vet, tests, gofmt check
make build
make dist     # cross-compile all platforms
make snapshot # goreleaser dry run
```

CI (`.github/workflows/ci.yml`) runs gofmt, `go mod tidy`, vet and race
tests on Linux, macOS and Windows, plus a cross-compile of all six targets,
for every push to `main` and every pull request.

To release, push a tag **without** a `v` prefix:

```sh
git tag 1.2.0 && git push origin 1.2.0        # release
git tag 1.3.0-rc.1 && git push origin 1.3.0-rc.1   # marked as pre-release
```

`.github/workflows/release.yml` re-runs the tests, then goreleaser builds
linux/darwin/windows × amd64/arm64 and attaches to the GitHub release the bare
binaries, `.tar.gz`/`.zip` archives and `checksums.txt`. The tag becomes the
version reported by `kcctl --version`. Tags like `v1.2.0` are ignored.

## License

[MIT](LICENSE)
