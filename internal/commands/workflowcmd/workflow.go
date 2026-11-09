// Package workflowcmd manages workflows (scheduled, event-driven steps run
// against users or clients). The WORKFLOWS feature must be enabled.
package workflowcmd

import (
	"context"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/compat"
	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/kc"
	"github.com/emdzej/kcctl/internal/output"
)

var columns = []output.Column{
	output.Col("name", ".name"), output.Col("enabled", ".enabled"), output.Col("on", ".on"),
	output.Col("steps", ".steps[*].uses"), output.Col("id", ".id"),
}

var workflows = &app.Resource{
	Noun: "workflow", Plural: "workflows",
	Lookup: app.Lookup{
		What: "workflow", Path: app.RealmPath("workflows"), NameField: "name",
		Query: func(n string) url.Values { return url.Values{"search": {n}, "exact": {"true"}} },
	},
	Columns: columns,
	// The server returns 10 per page unless told otherwise.
	Paged: true,
	ListFlags: func(cmd *cobra.Command) func(url.Values) {
		search := cmd.Flags().String("search", "", "match workflow names containing this")
		return func(q url.Values) {
			if *search != "" {
				q.Set("search", *search)
			}
		}
	},
	Fields: app.Fields{
		{Flag: "name", Usage: "workflow name"},
		{Flag: "enabled", Kind: app.Bool, Usage: "whether the workflow runs"},
	},
	Required: []string{"name"},
	Example: map[string]string{
		"list":   "  kcctl workflow list --all",
		"create": "  kcctl workflow create -f disable-inactive.yaml",
		"update": "  kcctl workflow update disable-inactive --enabled=false\n  kcctl workflow update disable-inactive -f disable-inactive.yaml",
		"get":    "  kcctl workflow get disable-inactive -o yaml",
	},
}

func New(a *app.App) *cobra.Command {
	cmd := app.Group("workflow", "Manage workflows (needs the WORKFLOWS feature)", "workflows")
	cmd.AddCommand(workflows.ListCmd(a), get(a), workflows.CreateCmd(a), workflows.UpdateCmd(a), workflows.DeleteCmd(a),
		activation(a, true), activation(a, false), scheduled(a), migrate(a))
	// Gate every leaf rather than using PersistentPreRunE, which would
	// replace the root's hook that initialises the runtime.
	for _, c := range cmd.Commands() {
		if run := c.RunE; run != nil {
			c.RunE = func(cmd *cobra.Command, args []string) error {
				if err := a.Require(cmd.Context(), compat.Workflows); err != nil {
					return err
				}
				return run(cmd, args)
			}
		}
	}
	return cmd
}

func get(a *app.App) *cobra.Command {
	var noIDs bool
	cmd := &cobra.Command{
		Use: "get <id|name>", Short: "Show one workflow",
		Example: "  kcctl workflow get disable-inactive -o yaml --no-ids > wf.yaml   # reusable as a create file",
		Args:    app.ExactArgs(1, "<id|name>"), ValidArgsFunction: app.NoMoreArgs(1, workflows.RefCompletion(a)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := workflows.Resolve(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			var q url.Values
			if noIDs {
				q = url.Values{"includeId": {"false"}}
			}
			v, err := c.Get(cmd.Context(), p, q)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, columns)
		},
	}
	cmd.Flags().BoolVar(&noIDs, "no-ids", false, "omit workflow and step ids")
	return cmd
}

// resourceTypes maps the CLI's singular names to the server's ResourceType.
var resourceTypes = map[string]string{"user": "USERS", "users": "USERS", "client": "CLIENTS", "clients": "CLIENTS"}

func resourceID(ctx context.Context, c *kc.Client, typ, ref string) (string, error) {
	if typ == "USERS" {
		return app.Users.Find(ctx, c, ref)
	}
	return app.ClientUUID(ctx, c, ref)
}

func activation(a *app.App, on bool) *cobra.Command {
	verb := map[bool]string{true: "activate", false: "deactivate"}[on]
	var notBefore string
	cmd := &cobra.Command{
		Use: verb + " <workflow> user|client <resource>",
		Short: map[bool]string{
			true:  "Start a workflow for one user or client",
			false: "Stop a workflow for one user or client",
		}[on],
		Example: map[bool]string{
			true:  "  kcctl workflow activate disable-inactive user alice --not-before 3600",
			false: "  kcctl workflow deactivate disable-inactive user alice",
		}[on],
		Args: app.ExactArgs(3, "<workflow> user|client <resource>"),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
			switch len(args) {
			case 0:
				return workflows.RefCompletion(a)(cmd, args, tc)
			case 1:
				return []string{"user", "client"}, cobra.ShellCompDirectiveNoFileComp
			case 2:
				if resourceTypes[strings.ToLower(args[1])] == "CLIENTS" {
					return a.ClientCompleter()(cmd, args, tc)
				}
				return a.UserCompleter()(cmd, args, tc)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			typ, ok := resourceTypes[strings.ToLower(args[1])]
			if !ok {
				return errs.Usage("resource type must be user or client, not %q", args[1])
			}
			c, p, err := workflows.Resolve(ctx, a, args[0])
			if err != nil {
				return err
			}
			rid, err := resourceID(ctx, c, typ, args[2])
			if err != nil {
				return err
			}
			var q url.Values
			if on && notBefore != "" {
				q = url.Values{"notBefore": {notBefore}}
			}
			if _, err := c.Do(ctx, kc.Request{Method: "POST", Path: p + "/" + verb + "/" + typ + "/" + url.PathEscape(rid), Query: q}); err != nil {
				return err
			}
			a.Printer.Success("%sd workflow '%s' for %s '%s'.", strings.ToUpper(verb[:1])+verb[1:], args[0], strings.ToLower(args[1]), args[2])
			return nil
		},
	}
	if on {
		cmd.Flags().StringVar(&notBefore, "not-before", "", "when the first step runs: seconds from now, <n>ms, or an ISO-8601 date")
	}
	return cmd
}

func scheduled(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:     "scheduled user|client <resource>",
		Short:   "List workflows with steps scheduled for a user or client",
		Example: "  kcctl workflow scheduled user alice",
		Args:    app.ExactArgs(2, "user|client <resource>"),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
			switch len(args) {
			case 0:
				return []string{"user", "client"}, cobra.ShellCompDirectiveNoFileComp
			case 1:
				if resourceTypes[strings.ToLower(args[0])] == "CLIENTS" {
					return a.ClientCompleter()(cmd, args, tc)
				}
				return a.UserCompleter()(cmd, args, tc)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			typ, ok := resourceTypes[strings.ToLower(args[0])]
			if !ok {
				return errs.Usage("resource type must be user or client, not %q", args[0])
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			rid, err := resourceID(ctx, c, typ, args[1])
			if err != nil {
				return err
			}
			v, err := c.Get(ctx, c.RealmPath("workflows", "scheduled", rid), nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, columns)
		},
	}
}

func migrate(a *app.App) *cobra.Command {
	var from, to string
	var yes bool
	cmd := &cobra.Command{
		Use:     "migrate --from <step-id> --to <step-id>",
		Short:   "Move every resource scheduled at one step to another step",
		Example: "  kcctl workflow migrate --from 1a2b... --to 3c4d...",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if from == "" || to == "" {
				return errs.Usage("--from and --to are both required")
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			if err := a.Confirm(yes, "This reschedules every resource waiting at step "+from+" onto step "+to+"."); err != nil {
				return err
			}
			if _, err := c.Do(cmd.Context(), kc.Request{Method: "POST", Path: c.RealmPath("workflows", "migrate"), Query: url.Values{"from": {from}, "to": {to}}}); err != nil {
				return err
			}
			a.Printer.Success("Migrated scheduled resources from step %s to %s.", from, to)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "step id resources are scheduled at now")
	cmd.Flags().StringVar(&to, "to", "", "step id to move them to")
	app.YesFlag(cmd, &yes)
	return cmd
}
