# Working on kcctl

kcctl is a Go CLI for the Keycloak admin REST API (Keycloak 26.6.3+). Its
conventions follow the keycard CLI: kubectl-style `<noun> [sub-noun] <verb>`
commands, one shared runtime, typed errors with stable exit codes, and an
interactive mode driven by the same completion data as the shell.

## Layout

```
cmd/kcctl              main: calls root.Main
internal/errs          error taxonomy -> exit codes (no deps)
internal/config        contexts file, XDG paths, flag > env > context resolution
internal/httpx         shared HTTP client + -v/-vv tracing with secret masking
internal/auth          token grants (password, client_credentials, device), store, refresh
internal/kc            untyped admin API client, error mapping, serverinfo/profile cache
internal/compat        version parsing + capability table (the ONLY place versions live)
internal/output        table/vtable/json/yaml/jsonpath/custom-columns, colors, error report
internal/app           CLI runtime: globals, App, Resource CRUD factory, lookups, input, completion
internal/apptest       stub-server test harness
internal/commands/*    one package per resource group, each exports New(a *app.App) *cobra.Command
internal/repl          `kcctl interactive`
internal/root          assembles the tree (commands.go lists every group)
```

`internal/{errs,config,httpx,auth,kc,compat,output}` must not import cobra.

## Rules

- **Commands** read `<noun> [sub-noun] <verb> [args]`, e.g. `client role create`.
  Verbs: list, get, create, update, delete, plus domain verbs (add, remove,
  enable, disable, export, import, ...). Aliases: `ls` for list, `rm` for delete,
  plural nouns for groups.
- **Never redeclare a global flag** (`--realm/-r`, `--output/-o`, `--quiet/-q`,
  `--verbose/-v`, `--server`, `--context`, `--token`, `--no-input`, `--color`,
  `--insecure`, `--client-id`, `--auth-realm`) on a subcommand. `-Q` is used for
  query params in `api` because `-q` is taken.
- **Ids or names.** Every argument naming a resource accepts its UUID or its
  human name (`app.Lookup.Find`, `app.FindGroup`, `app.ClientUUID`). A UUID
  never triggers a lookup request.
- **Every id argument or flag gets a completion** when something lists it:
  `ValidArgsFunction` / `RegisterFlagCompletionFunc` via `a.Fetched(...)` or a
  Lookup's `Completer()`. Use `app.NoMoreArgs(n, f)` for fixed-arity commands.
  Completions feed both shell completion and the interactive prompt.
- **Use `app.Resource`** for plain collections; hand-write only what it can't
  express. Look at `internal/commands/usercmd` and `realmcmd` first.
- **Bodies** come from `-f/--file` (JSON/YAML, `-` = stdin) and `-s path=value`
  (`app.BodyFlags`), plus typed field flags (`app.Fields`). Updates GET the
  current representation, overlay changes and PUT the whole thing, so unknown
  fields from newer servers survive. Keep bodies as `map[string]any`; do not
  add Go structs for representations.
- **Secrets** come from a `--<what>-file` option, piped stdin, or a hidden
  prompt (`a.ReadSecret`). Never a plain flag value, positional, or `--set`.
- **Confirmation.** An operation that removes or replaces more than the one
  thing named on the line takes `app.YesFlag` and calls `a.Confirm(yes, "This
  deletes 3 users in realm 'x'.")`. A single-resource delete does not ask.
  Without a terminal Confirm refuses unless `--yes` is given.
- **Errors**: return `*errs.Error` (`errs.Usage`, `errs.NotFound`, ...) - never
  `os.Exit` or print errors yourself. Report only what the server said or what
  the status settles; do not guess which parameter a 4xx was about.
- **Output**: results go through `a.Printer.Print(data, columns)` to stdout;
  messages through `a.Printer.Success/Note/Warn` to stderr. Give list commands
  sensible default `[]output.Column`. Write commands print a success line; with
  structured output (`a.Printer.IsStructured()`) print data, not prose.
- **Versions**: never compare version numbers in a command. Add a capability
  to `internal/compat` (with `since`/`until`/`feature`) and call
  `a.Require(ctx, cap)` (hard requirement) or `a.Has(ctx, cap)` (choose
  between endpoint shapes). Unknown query params are ignored by Keycloak, so
  optional filters need no gating.
- **Paths**: build with `c.RealmPath("seg", id, ...)` which escapes each
  segment. Paths in `apptest` replies are the escaped form.
- **Comments** say why, not what. No decorative emoji.

## Tests

`go test ./...`. Each command package tests itself with `apptest.Run`, which
fails on any request without a reply, so a test pins the exact endpoints. Copy
stub response shapes from the Keycloak source
(`services/src/main/java/org/keycloak/services/resources/admin/`), not from
what you assume. Default stub server version is 26.6.3; set
`apptest.Options{Version: "27.0.0"}` to test the other shape.

## Build and release

`make build` builds for the host; `make dist` cross-compiles
linux/darwin/windows on amd64/arm64. Releases: pushing a tag without a `v`
prefix (`1.2.3`, `1.2.3-rc.1`) runs `.github/workflows/release.yml`, where
goreleaser attaches bare binaries, archives and checksums to the GitHub
release. The tag is stamped into
`github.com/emdzej/keycloak-cli/internal/app.Version`; `v`-prefixed tags
don't trigger a release.
