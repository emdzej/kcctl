// Package eventcmd reads and clears login and admin events and manages the
// realm's event configuration.
package eventcmd

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/errs"
	"github.com/emdzej/keycloak-cli/internal/kc"
	"github.com/emdzej/keycloak-cli/internal/output"
)

func New(a *app.App) *cobra.Command {
	cmd := app.Group("event", "Read and configure login and admin events", "events")
	cmd.AddCommand(list(a), clearCmd(a, "events", "This deletes all stored login events in realm '%s'."), config(a), admin(a))
	return cmd
}

// enumCompleter offers values of a server enum (eventType, operationType,
// resourceType) as listed by serverinfo.
func enumCompleter(a *app.App, name string) cobra.CompletionFunc {
	return a.Fetched(name+" values", func(ctx context.Context, c *kc.Client, _ []string, _ string) ([]app.Item, error) {
		info, err := c.ServerInfo(ctx)
		if err != nil {
			return nil, err
		}
		enums, _ := info["enums"].(map[string]any)
		l, _ := enums[name].([]any)
		out := make([]app.Item, 0, len(l))
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, app.Item{Value: s})
			}
		}
		return out, nil
	})
}

// page holds the first/max/all trio every event list takes.
type page struct {
	first, max int
	all        bool
}

func (p *page) bind(cmd *cobra.Command) {
	cmd.Flags().IntVar(&p.first, "first", 0, "skip this many results")
	cmd.Flags().IntVar(&p.max, "max", 0, "return at most this many results (server default: 100)")
	cmd.Flags().BoolVar(&p.all, "all", false, "fetch every page")
	cmd.Flags().String("direction", "", "sort by time: asc|desc (server default: newest first)")
	cmd.RegisterFlagCompletionFunc("direction", app.Values("asc", "desc"))
}

func (p *page) fetch(ctx context.Context, cmd *cobra.Command, c *kc.Client, path string, q url.Values) (any, error) {
	if d, _ := cmd.Flags().GetString("direction"); d != "" {
		if d != "asc" && d != "desc" {
			return nil, errs.Usage("--direction must be asc or desc")
		}
		q.Set("direction", d)
	}
	if p.all {
		return app.Paginate(ctx, c, path, q, p.first)
	}
	if cmd.Flags().Changed("first") {
		q.Set("first", strconv.Itoa(p.first))
	}
	if cmd.Flags().Changed("max") {
		q.Set("max", strconv.Itoa(p.max))
	}
	return c.Get(ctx, path, q)
}

const dateHelp = " (yyyy-MM-dd or epoch milliseconds, inclusive)"

func list(a *app.App) *cobra.Command {
	var pg page
	var types []string
	var client, user, ip, from, to string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List login events (needs events enabled: 'kcctl event config update --enabled=true')",
		Example: `  kcctl event list --type LOGIN_ERROR --date-from 2026-01-01
  kcctl event list --user alice --client account-console --all -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			q := url.Values{}
			for _, t := range types {
				q.Add("type", t)
			}
			set := func(k, v string) {
				if v != "" {
					q.Set(k, v)
				}
			}
			if user != "" {
				id, err := app.Users.Find(ctx, c, user)
				if err != nil {
					return err
				}
				q.Set("user", id)
			}
			set("client", client)
			set("ipAddress", ip)
			set("dateFrom", from)
			set("dateTo", to)
			v, err := pg.fetch(ctx, cmd, c, c.RealmPath("events"), q)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, []output.Column{
				output.Col("time", ".time"), output.Col("type", ".type"), output.Col("client", ".clientId"),
				output.Col("user", ".userId"), output.Col("ip", ".ipAddress"), output.Col("error", ".error"),
			})
		},
	}
	pg.bind(cmd)
	f := cmd.Flags()
	f.StringSliceVar(&types, "type", nil, "event types, repeatable or comma-separated (e.g. LOGIN,LOGIN_ERROR)")
	f.StringVar(&client, "client", "", "clientId the event was for")
	f.StringVar(&user, "user", "", "user (username or id) the event was for")
	f.StringVar(&ip, "ip", "", "IP address")
	f.StringVar(&from, "date-from", "", "earliest date"+dateHelp)
	f.StringVar(&to, "date-to", "", "latest date"+dateHelp)
	cmd.RegisterFlagCompletionFunc("type", enumCompleter(a, "eventType"))
	cmd.RegisterFlagCompletionFunc("client", a.ClientCompleter())
	cmd.RegisterFlagCompletionFunc("user", a.UserCompleter())
	return cmd
}

func clearCmd(a *app.App, seg, what string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Delete all stored " + map[string]string{"events": "login", "admin-events": "admin"}[seg] + " events of the realm",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			if err := a.Confirm(yes, fmt.Sprintf(what, c.Realm)); err != nil {
				return err
			}
			if _, err := c.Delete(cmd.Context(), c.RealmPath(seg), nil); err != nil {
				return err
			}
			a.Printer.Success("Cleared %s in realm '%s'.", map[string]string{"events": "login events", "admin-events": "admin events"}[seg], c.Realm)
			return nil
		},
	}
	app.YesFlag(cmd, &yes)
	return cmd
}

func config(a *app.App) *cobra.Command {
	cmd := app.Group("config", "Show or change what the realm records")
	get := &cobra.Command{
		Use: "get", Short: "Show the event configuration", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), c.RealmPath("events", "config"), nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, nil)
		},
	}
	fields := app.Fields{
		{Flag: "enabled", Path: "eventsEnabled", Kind: app.Bool, Usage: "store login events"},
		{Flag: "expiration", Path: "eventsExpiration", Kind: app.Int, Usage: "delete stored login events after this many seconds (0: never)"},
		{Flag: "listener", Path: "eventsListeners", Kind: app.Strings, Usage: "event listeners, replaces the list (e.g. jboss-logging,email)"},
		{Flag: "types", Path: "enabledEventTypes", Kind: app.Strings, Usage: "login event types to store, replaces the list"},
		{Flag: "admin-events", Path: "adminEventsEnabled", Kind: app.Bool, Usage: "store admin events"},
		{Flag: "admin-events-details", Path: "adminEventsDetailsEnabled", Kind: app.Bool, Usage: "include representations in admin events"},
	}
	var body app.BodyFlags
	update := &cobra.Command{
		Use:   "update",
		Short: "Change the event configuration (reads it, applies your changes, writes it back)",
		Example: `  kcctl event config update --enabled=true --expiration 604800 --admin-events=true
  kcctl event config update --listener jboss-logging,email`,
		Args: cobra.NoArgs,
	}
	fv := fields.Bind(update)
	body.Bind(update)
	update.RegisterFlagCompletionFunc("types", enumCompleter(a, "eventType"))
	update.RegisterFlagCompletionFunc("listener", a.Fetched("event listeners", func(ctx context.Context, c *kc.Client, _ []string, _ string) ([]app.Item, error) {
		info, err := c.ServerInfo(ctx)
		if err != nil {
			return nil, err
		}
		prov, _ := info["providers"].(map[string]any)
		el, _ := prov["eventsListener"].(map[string]any)
		ps, _ := el["providers"].(map[string]any)
		out := make([]app.Item, 0, len(ps))
		for k := range ps {
			out = append(out, app.Item{Value: k})
		}
		return out, nil
	}))
	update.RunE = func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		changed := body.Given()
		for _, f := range fields {
			changed = changed || cmd.Flags().Changed(f.Flag)
		}
		if !changed {
			return errs.Usage("nothing to change").WithHint("Pass field flags, -s path=value, or -f file.")
		}
		c, err := a.Client()
		if err != nil {
			return err
		}
		p := c.RealmPath("events", "config")
		cur, err := c.Get(ctx, p, nil)
		if err != nil {
			return err
		}
		b, _ := cur.(map[string]any)
		if b, err = body.Build(a, b); err != nil {
			return err
		}
		if err := fv.Apply(cmd, b); err != nil {
			return err
		}
		if _, err := c.Put(ctx, p, b); err != nil {
			return err
		}
		a.Printer.Success("Updated event configuration of realm '%s'.", c.Realm)
		return nil
	}
	cmd.AddCommand(get, update)
	return cmd
}

// inRealm is c scoped to another realm, for ids that live where the acting
// admin signed in rather than in the target realm.
func inRealm(c *kc.Client, realm string) *kc.Client {
	cp := *c
	cp.Realm = realm
	return &cp
}

func admin(a *app.App) *cobra.Command {
	cmd := app.Group("admin", "Read and clear admin events (changes made through the admin API)")
	var pg page
	var ops, resTypes []string
	var path, authUser, authRealm, authClient, authIP, from, to string
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"},
		Short: "List admin events (needs 'kcctl event config update --admin-events=true')",
		Long: `List admin events. --admin-realm, --auth-client and --auth-user describe the
acting administrator; --auth-client and --auth-user are looked up in
--admin-realm when given, else in the target realm.`,
		Example: `  kcctl event admin list --operation DELETE --resource-type USER
  kcctl event admin list --admin-realm master --auth-user admin --resource-path 'users/*'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			q := url.Values{}
			for _, o := range ops {
				q.Add("operationTypes", o)
			}
			for _, r := range resTypes {
				q.Add("resourceTypes", r)
			}
			lookupIn := c
			if authRealm != "" {
				// The query wants the realm's id, which differs from its name.
				id := authRealm
				if !app.IsUUID(authRealm) {
					v, err := c.Get(ctx, kc.RealmPathFor(authRealm), nil)
					if err != nil {
						return err
					}
					m, _ := v.(map[string]any)
					id, _ = m["id"].(string)
				}
				q.Set("authRealm", id)
				lookupIn = inRealm(c, authRealm)
			}
			if authUser != "" {
				id, err := app.Users.Find(ctx, lookupIn, authUser)
				if err != nil {
					return err
				}
				q.Set("authUser", id)
			}
			if authClient != "" {
				id, err := app.ClientUUID(ctx, lookupIn, authClient)
				if err != nil {
					return err
				}
				q.Set("authClient", id)
			}
			for k, v := range map[string]string{"resourcePath": path, "authIpAddress": authIP, "dateFrom": from, "dateTo": to} {
				if v != "" {
					q.Set(k, v)
				}
			}
			v, err := pg.fetch(ctx, cmd, c, c.RealmPath("admin-events"), q)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, []output.Column{
				output.Col("time", ".time"), output.Col("operation", ".operationType"),
				output.Col("resource", ".resourceType"), output.Col("path", ".resourcePath"),
				output.Col("by user", ".authDetails.userId"), output.Col("ip", ".authDetails.ipAddress"),
				output.Col("error", ".error"),
			})
		},
	}
	pg.bind(list)
	f := list.Flags()
	f.StringSliceVar(&ops, "operation", nil, "operation types, repeatable or comma-separated: CREATE,UPDATE,DELETE,ACTION")
	f.StringSliceVar(&resTypes, "resource-type", nil, "resource types, repeatable or comma-separated (e.g. USER,CLIENT)")
	f.StringVar(&path, "resource-path", "", "resource path; * matches any segment (e.g. users/*)")
	f.StringVar(&authRealm, "admin-realm", "", "realm the acting admin signed in to")
	f.StringVar(&authClient, "auth-client", "", "client the acting admin used (clientId or id)")
	f.StringVar(&authUser, "auth-user", "", "acting admin (username or id)")
	f.StringVar(&authIP, "auth-ip", "", "IP address of the acting admin")
	f.StringVar(&from, "date-from", "", "earliest date"+dateHelp)
	f.StringVar(&to, "date-to", "", "latest date"+dateHelp)
	list.RegisterFlagCompletionFunc("operation", enumCompleter(a, "operationType"))
	list.RegisterFlagCompletionFunc("resource-type", enumCompleter(a, "resourceType"))
	list.RegisterFlagCompletionFunc("admin-realm", a.CompleteRealms)
	list.RegisterFlagCompletionFunc("auth-user", a.UserCompleter())
	list.RegisterFlagCompletionFunc("auth-client", a.ClientCompleter())

	cmd.AddCommand(list, clearCmd(a, "admin-events", "This deletes all stored admin events in realm '%s'."))
	return cmd
}
