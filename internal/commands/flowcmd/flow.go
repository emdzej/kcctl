// Package flowcmd manages authentication flows, their executions and
// authenticator configs, and the realm's required actions.
package flowcmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/kc"
	"github.com/emdzej/kcctl/internal/output"
)

var flowLookup = app.Lookup{What: "flow", Path: app.RealmPath("authentication", "flows"), NameField: "alias"}

var flows = &app.Resource{
	Noun: "flow", Plural: "flows", Lookup: flowLookup,
	Columns: []output.Column{
		output.Col("alias", ".alias"), output.Col("provider", ".providerId"),
		output.Col("top level", ".topLevel"), output.Col("built in", ".builtIn"),
		output.Col("description", ".description"), output.Col("id", ".id"),
	},
	Fields: app.Fields{
		{Flag: "alias", Usage: "flow alias (on update: rename the flow)"},
		{Flag: "description", Usage: "description"},
		{Flag: "provider-id", Path: "providerId", Usage: "basic-flow or client-flow"},
		{Flag: "top-level", Path: "topLevel", Kind: app.Bool, Usage: "create a top-level flow (default true; cannot be changed later)"},
	},
	CreateArg: "alias",
	Required:  []string{"alias"},
	Defaults:  map[string]any{"providerId": "basic-flow", "topLevel": true, "builtIn": false},
	Example: map[string]string{
		"create": "  kcctl flow create my-browser --description 'Browser with OTP'\n  kcctl flow create my-clients --provider-id client-flow",
		"update": "  kcctl flow update my-browser --description 'New text'",
		"delete": "  kcctl flow delete my-browser   # built-in flows cannot be deleted",
	},
}

func New(a *app.App) *cobra.Command {
	cmd := app.Group("flow", "Manage authentication flows, executions and required actions", "flows", "authentication")
	cmd.Long = `Manage authentication flows, executions and required actions.

Flows are bound to the realm through realm attributes, e.g.
  kcctl realm update acme -s browserFlow=my-browser -s directGrantFlow=my-direct-grant
(also resetCredentialsFlow, registrationFlow, clientAuthenticationFlow,
dockerAuthenticationFlow, firstBrokerLoginFlow).`
	cmd.AddCommand(flows.CRUD(a)...)
	for _, c := range cmd.Commands() {
		if c.Name() == "create" {
			c.RegisterFlagCompletionFunc("provider-id", app.Values("basic-flow", "client-flow"))
		}
	}
	cmd.AddCommand(copyCmd(a), executions(a), authenticators(a), requiredActions(a))
	return cmd
}

func flowComp(a *app.App) cobra.CompletionFunc { return flows.RefCompletion(a) }

// flowAlias returns the alias of a flow given its id or alias. Execution and
// copy endpoints are keyed by alias, while get/update/delete take the id.
func flowAlias(ctx context.Context, c *kc.Client, ref string) (string, error) {
	if !app.IsUUID(ref) {
		return ref, nil
	}
	v, err := c.Get(ctx, c.RealmPath("authentication", "flows", ref), nil)
	if err != nil {
		return "", err
	}
	m, _ := v.(map[string]any)
	alias, _ := m["alias"].(string)
	if alias == "" {
		return "", errs.NotFound("flow %s has no alias", ref)
	}
	return alias, nil
}

func copyCmd(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:               "copy <flow> <new-alias>",
		Short:             "Copy a flow (including its executions) under a new alias",
		Example:           "  kcctl flow copy browser my-browser",
		Args:              app.ExactArgs(2, "<flow> <new-alias>"),
		ValidArgsFunction: app.NoMoreArgs(1, flowComp(a)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			alias, err := flowAlias(ctx, c, args[0])
			if err != nil {
				return err
			}
			resp, err := c.Post(ctx, c.RealmPath("authentication", "flows", alias, "copy"), map[string]any{"newName": args[1]})
			if err != nil {
				return err
			}
			return a.ReportCreated("flow", args[1], resp.CreatedID())
		},
	}
}

var execColumns = []output.Column{
	output.Col("level", ".level"), output.Col("index", ".index"),
	output.Col("name", ".displayName"), output.Col("requirement", ".requirement"),
	output.Col("provider", ".providerId"), output.Col("id", ".id"),
}

func listExecutions(ctx context.Context, c *kc.Client, flowRef string) (string, []any, error) {
	alias, err := flowAlias(ctx, c, flowRef)
	if err != nil {
		return "", nil, err
	}
	v, err := c.Get(ctx, c.RealmPath("authentication", "flows", alias, "executions"), nil)
	if err != nil {
		return "", nil, err
	}
	l, _ := v.([]any)
	return alias, l, nil
}

// findExecution matches an execution of the flow by id, provider id or
// display name; names must be unique within the flow.
func findExecution(list []any, ref, alias string) (map[string]any, error) {
	var matches []map[string]any
	for _, x := range list {
		m, _ := x.(map[string]any)
		if m["id"] == ref {
			return m, nil
		}
		if m["providerId"] == ref || m["displayName"] == ref {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return nil, errs.NotFound("no execution '%s' in flow '%s'", ref, alias)
	case 1:
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for i, m := range matches {
		ids[i], _ = m["id"].(string)
	}
	return nil, errs.Usage("%d executions in flow '%s' match '%s'; pass the id instead (%s)", len(matches), alias, ref, strings.Join(ids, ", "))
}

// executionID resolves an execution ref: a UUID as is, anything else by
// name within --flow.
func executionID(ctx context.Context, c *kc.Client, flowRef, ref string) (string, error) {
	if app.IsUUID(ref) {
		return ref, nil
	}
	if flowRef == "" {
		return "", errs.Usage("'%s' is not an execution id; pass --flow to look it up by name", ref)
	}
	alias, list, err := listExecutions(ctx, c, flowRef)
	if err != nil {
		return "", err
	}
	m, err := findExecution(list, ref, alias)
	if err != nil {
		return "", err
	}
	id, _ := m["id"].(string)
	return id, nil
}

// execCompleter lists the executions of the flow named by flowOf(args).
func execCompleter(a *app.App, flowOf func(args []string) string) cobra.CompletionFunc {
	return a.Fetched("executions", func(ctx context.Context, c *kc.Client, args []string, _ string) ([]app.Item, error) {
		f := flowOf(args)
		if f == "" {
			return nil, nil
		}
		_, list, err := listExecutions(ctx, c, f)
		if err != nil {
			return nil, err
		}
		return app.ItemsFrom(list, "id", "displayName"), nil
	})
}

var requirements = []string{"REQUIRED", "ALTERNATIVE", "DISABLED", "CONDITIONAL"}

func checkRequirement(r string) (string, error) {
	u := strings.ToUpper(r)
	for _, x := range requirements {
		if u == x {
			return u, nil
		}
	}
	return "", errs.Usage("requirement must be one of %s", strings.Join(requirements, ", "))
}

// setRequirement PUTs the execution's info representation back with a new
// requirement; that is the only way the API changes one.
func setRequirement(ctx context.Context, c *kc.Client, alias string, exec map[string]any, req string) error {
	exec["requirement"] = req
	_, err := c.Put(ctx, c.RealmPath("authentication", "flows", alias, "executions"), exec)
	return err
}

func executions(a *app.App) *cobra.Command {
	cmd := app.Group("execution", "Manage the executions (steps) of a flow", "executions", "exec")
	firstFlow := func(args []string) string {
		if len(args) > 0 {
			return args[0]
		}
		return ""
	}
	flowThenExec := func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
		switch len(args) {
		case 0:
			return flowComp(a)(cmd, args, tc)
		case 1:
			return execCompleter(a, firstFlow)(cmd, args, tc)
		case 2:
			return requirements, cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	list := &cobra.Command{
		Use: "list <flow>", Aliases: []string{"ls"}, Short: "List a flow's executions, including nested sub-flows",
		Args: app.ExactArgs(1, "<flow>"), ValidArgsFunction: app.NoMoreArgs(1, flowComp(a)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			_, l, err := listExecutions(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			return a.Printer.Print(l, execColumns)
		},
	}

	var provider, addReq string
	add := &cobra.Command{
		Use:   "add <flow> --provider <authenticator>",
		Short: "Add an authenticator execution to a flow",
		Example: `  kcctl flow execution add my-browser --provider auth-otp-form --requirement REQUIRED
  kcctl flow authenticators   # lists provider ids`,
		Args: app.ExactArgs(1, "<flow>"), ValidArgsFunction: app.NoMoreArgs(1, flowComp(a)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if provider == "" {
				return errs.Usage("--provider is required").WithHint("See 'kcctl flow authenticators' for provider ids.")
			}
			req := ""
			if addReq != "" {
				var err error
				if req, err = checkRequirement(addReq); err != nil {
					return err
				}
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			alias, err := flowAlias(ctx, c, args[0])
			if err != nil {
				return err
			}
			resp, err := c.Post(ctx, c.RealmPath("authentication", "flows", alias, "executions", "execution"), map[string]any{"provider": provider})
			if err != nil {
				return err
			}
			id := resp.CreatedID()
			if req != "" {
				_, l, err := listExecutions(ctx, c, alias)
				if err != nil {
					return err
				}
				m, err := findExecution(l, id, alias)
				if err != nil {
					return err
				}
				if err := setRequirement(ctx, c, alias, m, req); err != nil {
					return err
				}
			}
			return a.ReportCreated("execution", provider, id)
		},
	}
	add.Flags().StringVar(&provider, "provider", "", "authenticator provider id, e.g. auth-otp-form")
	add.Flags().StringVar(&addReq, "requirement", "", "set the requirement right away: "+strings.Join(requirements, "|"))
	add.RegisterFlagCompletionFunc("provider", a.Fetched("authenticators", func(ctx context.Context, c *kc.Client, _ []string, _ string) ([]app.Item, error) {
		v, err := c.Get(ctx, c.RealmPath("authentication", "authenticator-providers"), nil)
		if err != nil {
			return nil, err
		}
		return app.ItemsFrom(v, "id", "displayName"), nil
	}))
	add.RegisterFlagCompletionFunc("requirement", app.Values(requirements...))

	var subAlias, subType, subDesc, subProvider string
	addFlow := &cobra.Command{
		Use:     "add-flow <flow> --alias <alias>",
		Short:   "Add a sub-flow to a flow",
		Example: "  kcctl flow execution add-flow my-browser --alias my-browser-otp --type basic-flow",
		Args:    app.ExactArgs(1, "<flow>"), ValidArgsFunction: app.NoMoreArgs(1, flowComp(a)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if subAlias == "" {
				return errs.Usage("--alias is required")
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			alias, err := flowAlias(ctx, c, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"alias": subAlias, "type": subType, "description": subDesc}
			if subProvider != "" {
				body["provider"] = subProvider
			}
			resp, err := c.Post(ctx, c.RealmPath("authentication", "flows", alias, "executions", "flow"), body)
			if err != nil {
				return err
			}
			// Location points at the new flow, not at its execution.
			return a.ReportCreated("sub-flow", subAlias, resp.CreatedID())
		},
	}
	addFlow.Flags().StringVar(&subAlias, "alias", "", "alias of the new sub-flow")
	addFlow.Flags().StringVar(&subType, "type", "basic-flow", "basic-flow or form-flow")
	addFlow.Flags().StringVar(&subDesc, "description", "", "description")
	addFlow.Flags().StringVar(&subProvider, "provider", "", "form provider for a form-flow, e.g. registration-page-form")
	addFlow.RegisterFlagCompletionFunc("type", app.Values("basic-flow", "form-flow"))

	setReq := &cobra.Command{
		Use:     "set-requirement <flow> <execution> REQUIRED|ALTERNATIVE|DISABLED|CONDITIONAL",
		Short:   "Change whether an execution is required, alternative, conditional or disabled",
		Long:    "Change an execution's requirement. <execution> is its id, provider id or display name within the flow.",
		Example: "  kcctl flow execution set-requirement my-browser auth-otp-form REQUIRED",
		Args:    app.ExactArgs(3, "<flow> <execution> <requirement>"), ValidArgsFunction: flowThenExec,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			req, err := checkRequirement(args[2])
			if err != nil {
				return err
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			alias, l, err := listExecutions(ctx, c, args[0])
			if err != nil {
				return err
			}
			m, err := findExecution(l, args[1], alias)
			if err != nil {
				return err
			}
			if err := setRequirement(ctx, c, alias, m, req); err != nil {
				return err
			}
			a.Printer.Success("Set '%s' in flow '%s' to %s.", args[1], alias, req)
			return nil
		},
	}

	cmd.AddCommand(list, add, addFlow, setReq,
		execAction(a, "raise", "Move an execution up within its flow", "raise-priority"),
		execAction(a, "lower", "Move an execution down within its flow", "lower-priority"),
		execDelete(a), execConfig(a))
	return cmd
}

// byID binds --flow, which lets <execution> be a name and enables completion.
func byID(a *app.App, cmd *cobra.Command, flow *string) {
	cmd.Flags().StringVar(flow, "flow", "", "flow containing the execution (lets <execution> be a name, and completes it)")
	cmd.RegisterFlagCompletionFunc("flow", flowComp(a))
	cmd.ValidArgsFunction = app.NoMoreArgs(1, execCompleter(a, func([]string) string { return *flow }))
}

func execAction(a *app.App, verb, short, seg string) *cobra.Command {
	var flow string
	cmd := &cobra.Command{
		Use: verb + " <execution>", Short: short,
		Example: fmt.Sprintf("  kcctl flow execution %s auth-otp-form --flow my-browser", verb),
		Args:    app.ExactArgs(1, "<execution>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			id, err := executionID(ctx, c, flow, args[0])
			if err != nil {
				return err
			}
			if _, err := c.Post(ctx, c.RealmPath("authentication", "executions", id, seg), nil); err != nil {
				return err
			}
			a.Printer.Success("Moved execution '%s' %s.", args[0], map[string]string{"raise": "up", "lower": "down"}[verb])
			return nil
		},
	}
	byID(a, cmd, &flow)
	return cmd
}

func execDelete(a *app.App) *cobra.Command {
	var flow string
	cmd := &cobra.Command{
		Use: "delete <execution>", Aliases: []string{"rm"}, Short: "Remove an execution (or sub-flow) from its flow",
		Args: app.ExactArgs(1, "<execution>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			id, err := executionID(ctx, c, flow, args[0])
			if err != nil {
				return err
			}
			if _, err := c.Delete(ctx, c.RealmPath("authentication", "executions", id), nil); err != nil {
				return err
			}
			a.Printer.Success("Deleted execution '%s'.", args[0])
			return nil
		},
	}
	byID(a, cmd, &flow)
	return cmd
}

// configID reads the execution's authenticator config id; "" means none yet.
func configID(ctx context.Context, c *kc.Client, execID string) (string, error) {
	v, err := c.Get(ctx, c.RealmPath("authentication", "executions", execID), nil)
	if err != nil {
		return "", err
	}
	m, _ := v.(map[string]any)
	id, _ := m["authenticatorConfig"].(string)
	return id, nil
}

func execConfig(a *app.App) *cobra.Command {
	cmd := app.Group("config", "Show or change an execution's authenticator config")

	var getFlow string
	get := &cobra.Command{
		Use: "get <execution>", Short: "Show the execution's authenticator config",
		Args: app.ExactArgs(1, "<execution>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			id, err := executionID(ctx, c, getFlow, args[0])
			if err != nil {
				return err
			}
			cfg, err := configID(ctx, c, id)
			if err != nil {
				return err
			}
			if cfg == "" {
				return errs.NotFound("execution '%s' has no config", args[0]).WithHint("Create one with 'kcctl flow execution config set " + args[0] + " --alias <name> --config key=value'.")
			}
			v, err := c.Get(ctx, c.RealmPath("authentication", "config", cfg), nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, nil)
		},
	}
	byID(a, get, &getFlow)

	var setFlow string
	var body app.BodyFlags
	set := &cobra.Command{
		Use:   "set <execution>",
		Short: "Create or change the execution's authenticator config",
		Long: `Create or change the execution's authenticator config. An existing config is
read, your changes applied, and written back; a new one needs --alias.`,
		Example: "  kcctl flow execution config set auth-otp-form --flow my-browser --alias otp-cfg --config defaultOtpOutcome=skip",
		Args:    app.ExactArgs(1, "<execution>"),
	}
	fv := app.Fields{
		{Flag: "alias", Usage: "config alias (required when creating)"},
		{Flag: "config", Kind: app.KVString, Usage: "set a config property"},
	}.Bind(set)
	body.Bind(set)
	set.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if !body.Given() && !cmd.Flags().Changed("alias") && !cmd.Flags().Changed("config") {
			return errs.Usage("nothing to change").WithHint("Pass --config key=value, --alias, -s path=value, or -f file.")
		}
		c, err := a.Client()
		if err != nil {
			return err
		}
		id, err := executionID(ctx, c, setFlow, args[0])
		if err != nil {
			return err
		}
		cfg, err := configID(ctx, c, id)
		if err != nil {
			return err
		}
		cur := map[string]any{}
		if cfg != "" {
			v, err := c.Get(ctx, c.RealmPath("authentication", "config", cfg), nil)
			if err != nil {
				return err
			}
			if m, ok := v.(map[string]any); ok {
				cur = m
			}
		}
		b, err := body.Build(a, cur)
		if err != nil {
			return err
		}
		if err := fv.Apply(cmd, b); err != nil {
			return err
		}
		if cfg != "" {
			if _, err := c.Put(ctx, c.RealmPath("authentication", "config", cfg), b); err != nil {
				return err
			}
			a.Printer.Success("Updated config of execution '%s'.", args[0])
			return nil
		}
		if err := app.RequireFields(b, "alias"); err != nil {
			return err
		}
		resp, err := c.Post(ctx, c.RealmPath("authentication", "executions", id, "config"), b)
		if err != nil {
			return err
		}
		alias, _ := b["alias"].(string)
		return a.ReportCreated("authenticator config", alias, resp.CreatedID())
	}
	byID(a, set, &setFlow)

	cmd.AddCommand(get, set)
	return cmd
}

var providerKinds = map[string]string{
	"authenticator": "authenticator-providers",
	"client":        "client-authenticator-providers",
	"form":          "form-providers",
	"form-action":   "form-action-providers",
}

func authenticators(a *app.App) *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:     "authenticators",
		Aliases: []string{"providers"},
		Short:   "List the authenticator providers that executions can use",
		Example: "  kcctl flow authenticators\n  kcctl flow authenticators --kind form-action",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			seg, ok := providerKinds[kind]
			if !ok {
				return errs.Usage("--kind must be authenticator, client, form or form-action")
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), c.RealmPath("authentication", seg), nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, []output.Column{
				output.Col("id", ".id"), output.Col("name", ".displayName"), output.Col("description", ".description"),
			})
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "authenticator", "authenticator|client|form|form-action")
	cmd.RegisterFlagCompletionFunc("kind", app.Values("authenticator", "client", "form", "form-action"))
	return cmd
}

var requiredActionLookup = app.Lookup{What: "required action", Path: app.RealmPath("authentication", "required-actions"), NameField: "alias", IDField: "alias"}

var reqActions = &app.Resource{
	Noun: "required action", Plural: "required actions", Lookup: requiredActionLookup, KeyByName: true,
	Columns: []output.Column{
		output.Col("alias", ".alias"), output.Col("name", ".name"), output.Col("enabled", ".enabled"),
		output.Col("default", ".defaultAction"), output.Col("priority", ".priority"),
	},
	Fields: app.Fields{
		{Flag: "name", Usage: "display name"},
		{Flag: "enabled", Kind: app.Bool, Usage: "whether the action can be used"},
		{Flag: "default", Path: "defaultAction", Kind: app.Bool, Usage: "add the action to every new user"},
		{Flag: "config", Kind: app.KVString, Usage: "set a config property"},
	},
	Example: map[string]string{
		"update": "  kcctl flow required-action update CONFIGURE_TOTP --default=true",
	},
}

func requiredActions(a *app.App) *cobra.Command {
	cmd := app.Group("required-action", "Manage the realm's required actions", "required-actions", "ra")
	comp := app.NoMoreArgs(1, a.Fetched("required actions", requiredActionLookup.Completer()))

	var unregistered bool
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List required actions", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			if unregistered {
				v, err := c.Get(cmd.Context(), c.RealmPath("authentication", "unregistered-required-actions"), nil)
				if err != nil {
					return err
				}
				return a.Printer.Print(v, []output.Column{output.Col("provider", ".providerId"), output.Col("name", ".name")})
			}
			v, err := c.Get(cmd.Context(), c.RealmPath("authentication", "required-actions"), nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, reqActions.Columns)
		},
	}
	list.Flags().BoolVar(&unregistered, "unregistered", false, "list providers that are installed but not registered in the realm")

	get, update, del := reqActions.GetCmd(a), reqActions.UpdateCmd(a), reqActions.DeleteCmd(a)
	get.ValidArgsFunction, update.ValidArgsFunction = comp, comp
	del.ValidArgsFunction = a.Fetched("required actions", requiredActionLookup.Completer())
	del.Short = "Unregister required actions"

	var regName string
	register := &cobra.Command{
		Use:     "register <provider-id>",
		Short:   "Register an installed required-action provider in the realm",
		Example: "  kcctl flow required-action list --unregistered\n  kcctl flow required-action register my-action",
		Args:    app.ExactArgs(1, "<provider-id>"),
		ValidArgsFunction: app.NoMoreArgs(1, a.Fetched("unregistered required actions", func(ctx context.Context, c *kc.Client, _ []string, _ string) ([]app.Item, error) {
			v, err := c.Get(ctx, c.RealmPath("authentication", "unregistered-required-actions"), nil)
			if err != nil {
				return nil, err
			}
			return app.ItemsFrom(v, "providerId", "name"), nil
		})),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			name := regName
			if name == "" {
				// The server stores a null name otherwise; use the provider's own.
				v, err := c.Get(ctx, c.RealmPath("authentication", "unregistered-required-actions"), nil)
				if err != nil {
					return err
				}
				for _, it := range app.ItemsFrom(v, "providerId", "name") {
					if it.Value == args[0] {
						name = it.Desc
					}
				}
			}
			if _, err := c.Post(ctx, c.RealmPath("authentication", "register-required-action"), map[string]any{"providerId": args[0], "name": name}); err != nil {
				return err
			}
			a.Printer.Success("Registered required action '%s'.", args[0])
			return nil
		},
	}
	register.Flags().StringVar(&regName, "name", "", "display name (default: the provider's name)")

	cmd.AddCommand(list, get, update, del, register,
		raEnable(a, comp, true), raEnable(a, comp, false),
		raMove(a, comp, "raise", "raise-priority"), raMove(a, comp, "lower", "lower-priority"))
	return cmd
}

func raEnable(a *app.App, comp cobra.CompletionFunc, on bool) *cobra.Command {
	verb := map[bool]string{true: "enable", false: "disable"}[on]
	return &cobra.Command{
		Use: verb + " <alias>", Short: strings.ToUpper(verb[:1]) + verb[1:] + " a required action",
		Args: app.ExactArgs(1, "<alias>"), ValidArgsFunction: comp,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			p := c.RealmPath("authentication", "required-actions", args[0])
			v, err := c.Get(ctx, p, nil)
			if err != nil {
				return err
			}
			// PUT replaces every field, so send the full current representation.
			m, _ := v.(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			m["enabled"] = on
			if _, err := c.Put(ctx, p, m); err != nil {
				return err
			}
			a.Printer.Success("%sd required action '%s'.", strings.ToUpper(verb[:1])+verb[1:], args[0])
			return nil
		},
	}
}

func raMove(a *app.App, comp cobra.CompletionFunc, verb, seg string) *cobra.Command {
	return &cobra.Command{
		Use: verb + " <alias>", Short: map[string]string{"raise": "Move a required action up", "lower": "Move a required action down"}[verb],
		Args: app.ExactArgs(1, "<alias>"), ValidArgsFunction: comp,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			if _, err := c.Post(cmd.Context(), c.RealmPath("authentication", "required-actions", args[0], seg), nil); err != nil {
				return err
			}
			a.Printer.Success("Moved required action '%s' %s.", args[0], map[string]string{"raise": "up", "lower": "down"}[verb])
			return nil
		},
	}
}
