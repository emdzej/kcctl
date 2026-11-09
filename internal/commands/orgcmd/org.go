// Package orgcmd manages organizations: the organizations themselves, their
// members, invitations and linked identity providers. Everything needs the
// ORGANIZATION server feature, checked before any request.
package orgcmd

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/app"
	"github.com/emdzej/keycloak-cli/internal/compat"
	"github.com/emdzej/keycloak-cli/internal/errs"
	"github.com/emdzej/keycloak-cli/internal/kc"
	"github.com/emdzej/keycloak-cli/internal/output"
)

var columns = []output.Column{
	output.Col("name", ".name"), output.Col("alias", ".alias"),
	output.Col("enabled", ".enabled"), output.Col("domains", ".domains[*].name"),
	output.Col("id", ".id"),
}

// domainsField collects --domain values; BeforeWrite turns them into the
// [{name: ...}] objects the representation holds.
const domainsField = "_domains"

func newOrgs() *app.Resource {
	return &app.Resource{
		Noun: "organization", Plural: "organizations", Lookup: app.Organizations, Columns: columns, Paged: true,
		ListFlags: func(cmd *cobra.Command) func(url.Values) {
			f := cmd.Flags()
			search := f.String("search", "", "match name or domain")
			exact := f.Bool("exact", false, "make --search match exactly")
			attr := f.StringArray("attr", nil, "filter by attribute key:value (repeatable)")
			idp := f.String("idp", "", "only organizations linked to this IdP alias (Keycloak 26.8+)")
			full := f.Bool("full", false, "return full representations (default is brief)")
			return func(q url.Values) {
				addSearch(q, *search, *exact, *attr, *idp)
				if *full {
					q.Set("briefRepresentation", "false")
				}
			}
		},
		Fields: app.Fields{
			{Flag: "alias", Usage: "alias (default: the name); cannot change later"},
			{Flag: "description", Usage: "description"},
			{Flag: "redirect-url", Path: "redirectUrl", Usage: "where members land after registration or invitation"},
			{Flag: "enabled", Kind: app.Bool, Usage: "whether the organization is enabled"},
			{Flag: "domain", Path: domainsField, Kind: app.Strings, Usage: "internet domains; replaces the current set"},
			{Flag: "attr", Path: "attributes", Kind: app.KV, Usage: "set an attribute; empty value removes it"},
		},
		CreateArg: "name",
		Required:  []string{"name"},
		BeforeWrite: func(_ context.Context, _ *app.App, b map[string]any, _ bool) error {
			setDomains(b)
			return nil
		},
		// Deleting an organization deletes its managed members' accounts.
		ConfirmDelete: true,
		Example: map[string]string{
			"list":   "  kcctl org list --search acme\n  kcctl org list --attr tier:gold --all -o json",
			"create": "  kcctl org create acme --domain acme.com --domain acme.io --redirect-url https://acme.com",
			"update": "  kcctl org update acme --description 'ACME Corp' --attr tier=gold",
		},
	}
}

func addSearch(q url.Values, search string, exact bool, attr []string, idp string) {
	if search != "" {
		q.Set("search", search)
	}
	if exact {
		q.Set("exact", "true")
	}
	if len(attr) > 0 {
		q.Set("q", strings.Join(attr, " "))
	}
	if idp != "" {
		q.Set("identityProvider", idp)
	}
}

// setDomains keeps existing domain objects (verified, IdP binding) for names
// still listed, and adds plain {name} objects for new ones.
func setDomains(b map[string]any) {
	v, ok := b[domainsField]
	if !ok {
		return
	}
	delete(b, domainsField)
	old := map[string]any{}
	if l, ok := b["domains"].([]any); ok {
		for _, d := range l {
			if m, ok := d.(map[string]any); ok {
				if n, _ := m["name"].(string); n != "" {
					old[n] = m
				}
			}
		}
	}
	names, _ := v.([]any)
	out := make([]any, 0, len(names))
	for _, x := range names {
		n, _ := x.(string)
		if n == "" {
			continue
		}
		if d, ok := old[n]; ok {
			out = append(out, d)
		} else {
			out = append(out, map[string]any{"name": n})
		}
	}
	b["domains"] = out
}

func New(a *app.App) *cobra.Command {
	cmd := app.Group("org", "Manage organizations", "orgs", "organization")
	r := newOrgs()
	comp := r.RefCompletion(a)
	cmd.AddCommand(r.CRUD(a)...)
	cmd.AddCommand(count(a), members(a, comp), idps(a, comp), invitations(a, comp))
	gate(a, cmd)
	return cmd
}

// gate puts the feature check in front of every runnable command, so a
// server without organizations gets one clear message instead of a 404.
func gate(a *app.App, cmd *cobra.Command) {
	for _, c := range cmd.Commands() {
		gate(a, c)
	}
	if !cmd.HasSubCommands() && cmd.RunE != nil {
		run := cmd.RunE
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			if err := a.Require(cmd.Context(), compat.Organizations); err != nil {
				return err
			}
			return run(cmd, args)
		}
	}
}

func orgPath(ctx context.Context, a *app.App, ref string, segs ...string) (*kc.Client, string, error) {
	c, err := a.Client()
	if err != nil {
		return nil, "", err
	}
	id, err := app.Organizations.Find(ctx, c, ref)
	if err != nil {
		return nil, "", err
	}
	return c, c.RealmPath(append([]string{"organizations", id}, segs...)...), nil
}

func count(a *app.App) *cobra.Command {
	var search, idp string
	var exact bool
	var attr []string
	cmd := &cobra.Command{
		Use: "count", Short: "Count organizations", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			q := url.Values{}
			addSearch(q, search, exact, attr, idp)
			v, err := c.Get(cmd.Context(), c.RealmPath("organizations", "count"), q)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, nil)
		},
	}
	cmd.Flags().StringVar(&search, "search", "", "count only organizations matching name or domain")
	cmd.Flags().BoolVar(&exact, "exact", false, "make --search match exactly")
	cmd.Flags().StringArrayVar(&attr, "attr", nil, "filter by attribute key:value (repeatable)")
	cmd.Flags().StringVar(&idp, "idp", "", "only organizations linked to this IdP alias (Keycloak 26.8+)")
	return cmd
}

// pager binds --first/--max/--all for hand-written paged lists.
type pager struct {
	first, max int
	all        bool
}

func (p *pager) bind(cmd *cobra.Command) {
	cmd.Flags().IntVar(&p.first, "first", 0, "skip this many results")
	cmd.Flags().IntVar(&p.max, "max", 0, "return at most this many results (server default if unset)")
	cmd.Flags().BoolVar(&p.all, "all", false, "fetch every page")
}

func (p *pager) get(cmd *cobra.Command, c *kc.Client, path string, q url.Values) (any, error) {
	if p.all {
		return app.Paginate(cmd.Context(), c, path, q, p.first)
	}
	if cmd.Flags().Changed("first") {
		q.Set("first", strconv.Itoa(p.first))
	}
	if cmd.Flags().Changed("max") {
		q.Set("max", strconv.Itoa(p.max))
	}
	return c.Get(cmd.Context(), path, q)
}

func orgThen(orgComp, next cobra.CompletionFunc, single bool) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return orgComp(cmd, args, tc)
		}
		if next == nil || (single && len(args) > 1) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return next(cmd, args, tc)
	}
}

func postForm(ctx context.Context, c *kc.Client, p string, form, q url.Values) error {
	_, err := c.Do(ctx, kc.Request{Method: "POST", Path: p, Query: q,
		RawBody: []byte(form.Encode()), ContentType: "application/x-www-form-urlencoded"})
	return err
}

func members(a *app.App, orgComp cobra.CompletionFunc) *cobra.Command {
	group := app.Group("member", "Manage organization members", "members")
	memberComp := a.Fetched("members", func(ctx context.Context, c *kc.Client, args []string, tc string) ([]app.Item, error) {
		id, err := app.Organizations.Find(ctx, c, args[0])
		if err != nil {
			return nil, err
		}
		v, err := c.Get(ctx, c.RealmPath("organizations", id, "members"), url.Values{"search": {tc}, "max": {"100"}})
		if err != nil {
			return nil, err
		}
		return app.ItemsFrom(v, "username", "id"), nil
	})

	var pg pager
	var search, mtype string
	var exact, full bool
	list := &cobra.Command{
		Use: "list <org>", Aliases: []string{"ls"}, Short: "List an organization's members",
		Args: app.ExactArgs(1, "<org>"), ValidArgsFunction: app.NoMoreArgs(1, orgComp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := orgPath(cmd.Context(), a, args[0], "members")
			if err != nil {
				return err
			}
			q := url.Values{}
			if search != "" {
				q.Set("search", search)
			}
			if exact {
				q.Set("exact", "true")
			}
			if mtype != "" {
				q.Set("membershipType", mtype)
			}
			if full {
				q.Set("briefRepresentation", "false")
			}
			v, err := pg.get(cmd, c, p, q)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, []output.Column{
				output.Col("username", ".username"), output.Col("email", ".email"),
				output.Col("membership", ".membershipType"), output.Col("enabled", ".enabled"),
				output.Col("id", ".id"),
			})
		},
	}
	pg.bind(list)
	list.Flags().StringVar(&search, "search", "", "match username, email, first or last name")
	list.Flags().BoolVar(&exact, "exact", false, "make --search match exactly")
	list.Flags().StringVar(&mtype, "membership-type", "", "MANAGED|UNMANAGED")
	list.Flags().BoolVar(&full, "full", false, "return full user representations (Keycloak 26.8+)")
	list.RegisterFlagCompletionFunc("membership-type", app.Values("MANAGED", "UNMANAGED"))

	add := &cobra.Command{
		Use: "add <org> <user>...", Short: "Add existing users as unmanaged members",
		Args: app.MinArgs(2, "<org> <user>..."), ValidArgsFunction: orgThen(orgComp, a.UserCompleter(), false),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, p, err := orgPath(ctx, a, args[0], "members")
			if err != nil {
				return err
			}
			for _, ref := range args[1:] {
				uid, err := app.Users.Find(ctx, c, ref)
				if err != nil {
					return err
				}
				// The body is the bare user id; Keycloak strips the JSON quotes.
				if _, err := c.Post(ctx, p, uid); err != nil {
					return err
				}
				a.Printer.Success("Added '%s' to organization '%s'.", ref, args[0])
			}
			return nil
		},
	}

	var yes bool
	remove := &cobra.Command{
		Use:   "remove <org> <member>...",
		Short: "Remove members (managed members are deleted from the realm)",
		Args:  app.MinArgs(2, "<org> <member>..."), ValidArgsFunction: orgThen(orgComp, memberComp, false),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, p, err := orgPath(ctx, a, args[0], "members")
			if err != nil {
				return err
			}
			ids := make([]string, 0, len(args)-1)
			var managed []string
			for _, ref := range args[1:] {
				uid, err := app.Users.Find(ctx, c, ref)
				if err != nil {
					return err
				}
				v, err := c.Get(ctx, p+"/"+url.PathEscape(uid), nil)
				if err != nil {
					return err
				}
				if m, _ := v.(map[string]any); m["membershipType"] == "MANAGED" {
					managed = append(managed, ref)
				}
				ids = append(ids, uid)
			}
			if len(managed) > 0 || len(ids) > 1 {
				what := fmt.Sprintf("This removes %d member(s) from organization '%s'.", len(ids), args[0])
				if len(managed) > 0 {
					what += fmt.Sprintf(" Managed members are deleted from realm '%s': %s.", c.Realm, strings.Join(managed, ", "))
				}
				if err := a.Confirm(yes, what); err != nil {
					return err
				}
			}
			for i, uid := range ids {
				if _, err := c.Delete(ctx, p+"/"+url.PathEscape(uid), nil); err != nil {
					return err
				}
				a.Printer.Success("Removed '%s' from organization '%s'.", args[i+1], args[0])
			}
			return nil
		},
	}
	app.YesFlag(remove, &yes)

	var email, firstName, lastName, inviteClient string
	invite := &cobra.Command{
		Use:     "invite <org> --email <address>",
		Short:   "Email an invitation to join (and register if needed)",
		Example: "  kcctl org member invite acme --email bob@acme.com --first-name Bob",
		Args:    app.ExactArgs(1, "<org>"), ValidArgsFunction: app.NoMoreArgs(1, orgComp),
		RunE: func(cmd *cobra.Command, args []string) error {
			if email == "" {
				return errs.Usage("--email is required")
			}
			c, p, err := orgPath(cmd.Context(), a, args[0], "members", "invite-user")
			if err != nil {
				return err
			}
			form := url.Values{"email": {email}}
			if firstName != "" {
				form.Set("firstName", firstName)
			}
			if lastName != "" {
				form.Set("lastName", lastName)
			}
			if err := postForm(cmd.Context(), c, p, form, clientQuery(inviteClient)); err != nil {
				return err
			}
			a.Printer.Success("Invited %s to organization '%s'.", email, args[0])
			return nil
		},
	}
	invite.Flags().StringVar(&email, "email", "", "address to invite")
	invite.Flags().StringVar(&firstName, "first-name", "", "first name for the new account")
	invite.Flags().StringVar(&lastName, "last-name", "", "last name for the new account")
	linkClientFlag(a, invite, &inviteClient)

	var existingClient string
	inviteExisting := &cobra.Command{
		Use:   "invite-existing <org> <user>",
		Short: "Email an existing user an invitation to join",
		Args:  app.ExactArgs(2, "<org> <user>"), ValidArgsFunction: orgThen(orgComp, a.UserCompleter(), true),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, p, err := orgPath(ctx, a, args[0], "members", "invite-existing-user")
			if err != nil {
				return err
			}
			uid, err := app.Users.Find(ctx, c, args[1])
			if err != nil {
				return err
			}
			if err := postForm(ctx, c, p, url.Values{"id": {uid}}, clientQuery(existingClient)); err != nil {
				return err
			}
			a.Printer.Success("Invited '%s' to organization '%s'.", args[1], args[0])
			return nil
		},
	}
	linkClientFlag(a, inviteExisting, &existingClient)

	membershipType := &cobra.Command{
		Use:   "membership-type <org> <member> <MANAGED|UNMANAGED>",
		Short: "Change whether the organization manages a member's account (Keycloak 26.8+)",
		Args:  app.ExactArgs(3, "<org> <member> <MANAGED|UNMANAGED>"),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
			switch len(args) {
			case 0:
				return orgComp(cmd, args, tc)
			case 1:
				return memberComp(cmd, args, tc)
			case 2:
				return []string{"MANAGED", "UNMANAGED"}, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.Require(ctx, compat.OrgMembershipType); err != nil {
				return err
			}
			t := strings.ToUpper(args[2])
			if t != "MANAGED" && t != "UNMANAGED" {
				return errs.Usage("membership type must be MANAGED or UNMANAGED")
			}
			c, p, err := orgPath(ctx, a, args[0], "members")
			if err != nil {
				return err
			}
			uid, err := app.Users.Find(ctx, c, args[1])
			if err != nil {
				return err
			}
			// The body is the bare enum as a JSON string.
			if _, err := c.Put(ctx, p+"/"+url.PathEscape(uid)+"/membership-type", t); err != nil {
				return err
			}
			a.Printer.Success("Membership of '%s' in '%s' is now %s.", args[1], args[0], t)
			return nil
		},
	}

	group.AddCommand(list, add, remove, invite, inviteExisting, membershipType)
	return group
}

func clientQuery(clientID string) url.Values {
	if clientID == "" {
		return nil
	}
	return url.Values{"client_id": {clientID}}
}

func linkClientFlag(a *app.App, cmd *cobra.Command, v *string) {
	cmd.Flags().StringVar(v, "link-client", "", "client whose redirect the invitation link uses (Keycloak 26.8+; ignored by older servers)")
	cmd.RegisterFlagCompletionFunc("link-client", a.ClientCompleter())
}

func idps(a *app.App, orgComp cobra.CompletionFunc) *cobra.Command {
	group := app.Group("idp", "Manage identity providers linked to an organization", "idps", "identity-provider")
	realmIdPs := a.Fetched("identity providers", func(ctx context.Context, c *kc.Client, _ []string, _ string) ([]app.Item, error) {
		v, err := c.Get(ctx, c.RealmPath("identity-provider", "instances"), url.Values{"briefRepresentation": {"true"}, "realmOnly": {"true"}})
		if err != nil {
			return nil, err
		}
		return app.ItemsFrom(v, "alias", "displayName"), nil
	})
	linked := a.Fetched("linked identity providers", func(ctx context.Context, c *kc.Client, args []string, _ string) ([]app.Item, error) {
		id, err := app.Organizations.Find(ctx, c, args[0])
		if err != nil {
			return nil, err
		}
		v, err := c.Get(ctx, c.RealmPath("organizations", id, "identity-providers"), nil)
		if err != nil {
			return nil, err
		}
		return app.ItemsFrom(v, "alias", "displayName"), nil
	})
	list := &cobra.Command{
		Use: "list <org>", Aliases: []string{"ls"}, Short: "List linked identity providers",
		Args: app.ExactArgs(1, "<org>"), ValidArgsFunction: app.NoMoreArgs(1, orgComp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := orgPath(cmd.Context(), a, args[0], "identity-providers")
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), p, nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, []output.Column{
				output.Col("alias", ".alias"), output.Col("provider", ".providerId"),
				output.Col("domain", ".config[\"kc.org.domain\"]"), output.Col("enabled", ".enabled"),
			})
		},
	}
	link := &cobra.Command{
		Use: "link <org> <idp-alias>...", Aliases: []string{"add"}, Short: "Link identity providers to the organization",
		Args: app.MinArgs(2, "<org> <idp-alias>..."), ValidArgsFunction: orgThen(orgComp, realmIdPs, false),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := orgPath(cmd.Context(), a, args[0], "identity-providers")
			if err != nil {
				return err
			}
			for _, alias := range args[1:] {
				// The body is the bare alias or id; Keycloak strips the JSON quotes.
				if _, err := c.Post(cmd.Context(), p, alias); err != nil {
					return err
				}
				a.Printer.Success("Linked identity provider '%s' to '%s'.", alias, args[0])
			}
			return nil
		},
	}
	unlink := &cobra.Command{
		Use: "unlink <org> <idp-alias>...", Aliases: []string{"remove"}, Short: "Unlink identity providers from the organization",
		Args: app.MinArgs(2, "<org> <idp-alias>..."), ValidArgsFunction: orgThen(orgComp, linked, false),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := orgPath(cmd.Context(), a, args[0], "identity-providers")
			if err != nil {
				return err
			}
			for _, alias := range args[1:] {
				if _, err := c.Delete(cmd.Context(), p+"/"+url.PathEscape(alias), nil); err != nil {
					return err
				}
				a.Printer.Success("Unlinked identity provider '%s' from '%s'.", alias, args[0])
			}
			return nil
		},
	}
	group.AddCommand(list, link, unlink)
	return group
}

func invitations(a *app.App, orgComp cobra.CompletionFunc) *cobra.Command {
	group := app.Group("invitation", "Manage pending organization invitations", "invitations", "invite")
	invComp := a.Fetched("invitations", func(ctx context.Context, c *kc.Client, args []string, _ string) ([]app.Item, error) {
		id, err := app.Organizations.Find(ctx, c, args[0])
		if err != nil {
			return nil, err
		}
		v, err := c.Get(ctx, c.RealmPath("organizations", id, "invitations"), url.Values{"max": {"100"}})
		if err != nil {
			return nil, err
		}
		return app.ItemsFrom(v, "id", "email"), nil
	})
	var pg pager
	var status, email, search, firstName, lastName string
	list := &cobra.Command{
		Use: "list <org>", Aliases: []string{"ls"}, Short: "List invitations",
		Args: app.ExactArgs(1, "<org>"), ValidArgsFunction: app.NoMoreArgs(1, orgComp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := orgPath(cmd.Context(), a, args[0], "invitations")
			if err != nil {
				return err
			}
			q := url.Values{}
			for k, v := range map[string]string{"status": status, "email": email, "search": search, "firstName": firstName, "lastName": lastName} {
				if v != "" {
					q.Set(k, v)
				}
			}
			v, err := pg.get(cmd, c, p, q)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, []output.Column{
				output.Col("email", ".email"), output.Col("status", ".status"),
				output.Col("sent", ".sentDate"), output.Col("expires", ".expiresAt"),
				output.Col("id", ".id"),
			})
		},
	}
	pg.bind(list)
	list.Flags().StringVar(&status, "status", "", "PENDING|EXPIRED")
	list.Flags().StringVar(&email, "email", "", "filter by email")
	list.Flags().StringVar(&search, "search", "", "match email, first or last name")
	list.Flags().StringVar(&firstName, "first-name", "", "filter by first name")
	list.Flags().StringVar(&lastName, "last-name", "", "filter by last name")
	list.RegisterFlagCompletionFunc("status", app.Values("PENDING", "EXPIRED"))

	resend := &cobra.Command{
		Use: "resend <org> <invitation-id>...", Short: "Send invitations again with a fresh link",
		Args: app.MinArgs(2, "<org> <invitation-id>..."), ValidArgsFunction: orgThen(orgComp, invComp, false),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := orgPath(cmd.Context(), a, args[0], "invitations")
			if err != nil {
				return err
			}
			for _, id := range args[1:] {
				if _, err := c.Post(cmd.Context(), p+"/"+url.PathEscape(id)+"/resend", nil); err != nil {
					return err
				}
				a.Printer.Success("Resent invitation %s.", id)
			}
			return nil
		},
	}
	var yes bool
	del := &cobra.Command{
		Use: "delete <org> <invitation-id>...", Aliases: []string{"rm"}, Short: "Withdraw invitations",
		Args: app.MinArgs(2, "<org> <invitation-id>..."), ValidArgsFunction: orgThen(orgComp, invComp, false),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := orgPath(cmd.Context(), a, args[0], "invitations")
			if err != nil {
				return err
			}
			if len(args) > 2 {
				if err := a.Confirm(yes, fmt.Sprintf("This withdraws %d invitations to organization '%s'.", len(args)-1, args[0])); err != nil {
					return err
				}
			}
			for _, id := range args[1:] {
				if _, err := c.Delete(cmd.Context(), p+"/"+url.PathEscape(id), nil); err != nil {
					return err
				}
				a.Printer.Success("Withdrew invitation %s.", id)
			}
			return nil
		},
	}
	app.YesFlag(del, &yes)
	group.AddCommand(list, resend, del)
	return group
}
