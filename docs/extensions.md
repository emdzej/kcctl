# Extensions

Keycloak can be extended with SPIs that add their own admin REST endpoints:
API keys, custom audit stores, tenant tooling. kcctl can't know about these
ahead of time, so it lets an extension describe its commands, and builds them
like its own: name lookups instead of UUIDs, completion in the shell and the
interactive prompt, every output format, confirmation, and secrets read only
from files or prompts.

There are two kinds:

- **Manifests**: a YAML (or JSON) file describing commands as HTTP calls.
  This is data, not code: kcctl validates it and can only send requests to the
  same server's admin API. Use a manifest when your commands map onto REST
  calls, which is almost always.
- **Plugins**: an executable named `kcctl-<name>` on `PATH`. `kcctl <name> ...`
  runs it. Use a plugin when a command needs logic (several calls, local
  files, a different protocol).

## Using extensions

```sh
kcctl auth login              # also discovers extensions on the server
kcctl extension list          # what's loaded, and from where
kcctl api-key list --user alice
```

| Command | What |
|---|---|
| `kcctl extension list` | manifests and plugins for the current server, with status |
| `kcctl extension discover` | ask the server's admin REST extensions for their manifests again |
| `kcctl extension install <file>` | install a manifest from a file (`-` for stdin); it applies to every server |
| `kcctl extension remove <name>` | remove an installed manifest |
| `kcctl extension show <name>` | print a loaded manifest |

Where commands come from:

- **Discovered** manifests are fetched from the server and stored per server.
  You only see them when the current context points at that server.
- **Installed** manifests are in `~/.config/kcctl/extensions/` and load for
  every server. An installed manifest replaces a discovered one with the same
  name, so you can pin or patch one.

Discovery runs after `kcctl auth login`, on `kcctl server info --refresh` and
on `kcctl extension discover`. It reads `/admin/serverinfo`, which most
admins can read. An account that can't list providers can still install a
manifest by hand.

Built-in commands always win: an extension can't replace or change `user`,
`client` or any other built-in command. A clashing command is skipped and
`extension list` shows it as `shadowed`.

When a command runs, kcctl checks the manifest's `requires` against the
server's cached profile. If a required provider or feature is missing, you get
a clear error instead of a 404.

## Adding kcctl support to your SPI

If your SPI has an `AdminRealmResourceProvider` (SPI
`admin-realm-restapi-extension`), you need two things:

1. **Write a manifest** for your endpoints (format below). Start from
   [`examples/api-keys.yaml`](examples/api-keys.yaml).
2. **Serve it** at a fixed sub-path of your provider:

   ```
   GET /admin/realms/{realm}/{provider-id}/kcctl-extension
   ```

   Return the file as `application/yaml` (or the same document as JSON).
   Allow any authenticated admin: the manifest describes commands and holds
   no data. kcctl treats `404`, `405` and `406` as "no manifest".

   With JAX-RS, in the resource your provider returns:

   ```java
   @GET
   @Path("kcctl-extension")
   @Produces("application/yaml")
   public Response kcctlExtension() {
       auth.realm().requireViewRealm();
       InputStream in = getClass().getResourceAsStream("/kcctl-extension.yaml");
       return Response.ok(in).build();
   }
   ```

Check it while you work:

```sh
kcctl extension install kcctl-extension.yaml   # validates and reports the first problem
kcctl api-key --help
kcctl -vv api-key list                         # shows the exact requests
```

Then deploy the SPI and run `kcctl extension discover`.

Version the manifest with your SPI. It's read from the running server, so a
user always gets the commands that match the deployed version.

## Manifest format

```yaml
apiVersion: kcctl/v1              # required; the only version so far
name: api-keys                    # lowercase letters, digits, dashes
description: API keys issued by the keycloak-api-keys extension
requires:                         # checked when a command runs
  providers: [admin-realm-restapi-extension/api-keys]   # spi/provider-id
  features: [ORGANIZATION]        # Keycloak feature flags
  minVersion: 26.6.3
commands:                         # top-level groups: kcctl <group> <verb>
  - name: api-key
    aliases: [api-keys]
    short: Manage API keys
    base: api-keys                # relative to /admin/realms/{realm}/
    columns:                      # table columns for this group's output
      - {header: id, path: .id}
      - {header: name, path: .name}
    complete: {value: .id, desc: .name}   # how to list items, for `complete: self`
    verbs:
      - name: create
        method: POST
        path: users/{user}/api-keys
        args:
          - {name: user, lookup: user}
        fields:
          - {flag: name, required: true}
          - {flag: client, path: clientId, complete: client}
        secret: key
```

The file is strict: unknown keys are errors, so typos don't fail silently.

### Groups

| Key | Meaning |
|---|---|
| `name`, `aliases`, `short` | the command name and its help line |
| `base` | path prefix for the group's verbs. It is relative to `/admin/realms/{realm}/`, or absolute when it starts with `/admin/`. A nested group's `base` adds to its parent's |
| `columns` | default table columns: `{header, path}` with a dotted JSON path |
| `complete` | how to list the group's items for completion: `path` (relative to `base`), `query`, `value` and `desc` (dotted paths) |
| `verbs` | the commands |
| `groups` | nested groups (sub-nouns) |

### Verbs

| Key | Meaning |
|---|---|
| `name`, `aliases`, `short`, `long`, `example` | help text |
| `method` | `GET` (default), `POST`, `PUT`, `PATCH`, `DELETE` |
| `path` | relative to the group's `base`. `{arg}` placeholders are filled from args and `{realm}` is the target realm. Values are URL-escaped, so a name never adds path segments |
| `args` | positional arguments |
| `query` | flags that set query parameters |
| `fields` | flags that set request body fields |
| `body` | `true` adds `-f/--file` and `-s/--set` for a JSON body |
| `paged` | `true` adds `--first`, `--max`, `--all` (Keycloak's `first`/`max` paging) |
| `columns` | table columns for this verb (default: the group's) |
| `print` | show the response. Default: `true` for `GET`, `false` otherwise, which prints a success line instead |
| `secret` | names a response field the server shows only once, such as a generated key. The response is printed and kcctl warns the user to store it |
| `message` | success line for non-`GET` verbs. `{name}` is filled from args, then the request body, then the response |
| `confirm` | ask before running, or require `--yes` without a terminal. Use it for verbs that affect more than the one thing named |

### Args, query parameters and fields

| Key | On | Meaning |
|---|---|---|
| `name` | args | placeholder name, shown as `<name>` in usage |
| `optional` | args | may be omitted; only at the end |
| `flag` | query, fields | flag name, without dashes |
| `param` | query | query parameter name (default: the flag) |
| `path` | fields | dotted body path (default: the flag) |
| `kind` | query, fields | `string` (default), `bool`, `int`, `strings` (repeatable/comma-separated). Fields also take `kv` (`--attr k=v` to an object of string lists), `kvstring` (object of strings) and `secret` |
| `required` | fields | fail before the request if it's missing |
| `usage` | all | help text |
| `lookup` | all | resolve a human name to the id the API expects, and complete it (see below) |
| `complete` | all | complete only; the value is sent as typed |
| `values` | all | complete from a fixed list |

`lookup` kinds turn a name into an id. A UUID passes through unchanged.

| Kind | Accepts | Sends |
|---|---|---|
| `user` | username or id | user id |
| `client` | clientId or UUID | client UUID |
| `group` | `/path`, unique name or id | group id |
| `client-scope` | name or id | scope id |
| `organization` | name or id | organization id |

`complete` takes those kinds plus `realm`, `realm-role` and `self` (the
group's own items, from its `complete` section).

A `secret` field never becomes a flag value, where it would end up in shell
history. kcctl adds `--<flag>-file` instead and otherwise reads piped stdin or
prompts without echo.

### What's not allowed

A discovered manifest comes from a server, so kcctl only accepts manifests
that can't misuse the user's token:

- paths are relative, or absolute under `/admin/`. No URLs, no `//host`, no
  `..`, no `?` or `#` (use `query`);
- flags can't reuse kcctl's global flags (`--realm`, `--server`, `--output`,
  `--token`, ...) or `--file`, `--set`, `--yes`; a paged verb owns `--first`,
  `--max` and `--all`;
- placeholders must name an arg (or be `{realm}`).

## Plugins

Any executable named `kcctl-<name>` (lowercase, dashes) on `PATH` becomes
`kcctl <name>`. On Windows, `.exe`, `.bat` and `.cmd` count. Plugins are listed
under *Extensions* in `kcctl --help`, in `kcctl extension list`, and in the
interactive prompt.

Global flags go **before** the plugin name and are kcctl's. Everything after
the name goes to the plugin unchanged:

```sh
kcctl --context prod -r acme audit-export --since 7d
#     └─ kcctl ──────────────┘ └─ plugin + its args ─┘
```

The plugin gets the resolved target in environment variables. The connection
ones are the variables kcctl itself reads, so calling kcctl from the plugin
hits the same server with the same session:

| Variable | Value |
|---|---|
| `KCCTL_BIN` | path of the kcctl that ran the plugin |
| `KCCTL_CONTEXT`, `KCCTL_SERVER`, `KCCTL_AUTH_REALM`, `KCCTL_REALM`, `KCCTL_CLIENT_ID` | the resolved target |
| `KCCTL_TOKEN` | only if the user passed `--token`; otherwise use `$KCCTL_BIN token` |
| `KCCTL_OUTPUT` | the `-o` value, if any |
| `KCCTL_NO_INPUT` | `1` with `--no-input` |
| `KCCTL_VERBOSE` | `1` or `2` with `-v`/`-vv` |

```sh
#!/bin/sh
# kcctl-me: show my own user record in the target realm
set -e
me=$("$KCCTL_BIN" auth whoami -o jsonpath='$.username')
"$KCCTL_BIN" api GET users -Q username="$me" -Q exact=true -o json
```

kcctl exits with the plugin's exit code. A plugin can't shadow a built-in
command or a manifest command.
