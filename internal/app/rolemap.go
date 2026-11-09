package app

import (
	"context"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/emdzej/keycloak-cli/internal/errs"
	"github.com/emdzej/keycloak-cli/internal/kc"
	"github.com/emdzej/keycloak-cli/internal/output"
)

var RoleColumns = []output.Column{
	output.Col("name", ".name"), output.Col("composite", ".composite"),
	output.Col("description", ".description"), output.Col("id", ".id"),
}

// RoleMappingCmds builds `role list|add|remove` for anything that holds role
// mappings (users, groups) or scope mappings (clients, client scopes). base
// resolves the owner's mapping root, e.g. .../users/{id}/role-mappings.
func (a *App) RoleMappingCmds(owner string, ownerComp cobra.CompletionFunc, base func(ctx context.Context, c *kc.Client, ref string) (string, error)) *cobra.Command {
	group := Group("role", "Manage the "+owner+"'s role mappings", "roles")
	var client string
	group.PersistentFlags().StringVar(&client, "client", "", "work with this client's roles instead of realm roles")
	group.RegisterFlagCompletionFunc("client", a.ClientCompleter())

	target := func(ctx context.Context, c *kc.Client, ref string) (string, error) {
		root, err := base(ctx, c, ref)
		if err != nil {
			return "", err
		}
		if client == "" {
			return root + "/realm", nil
		}
		id, err := ClientUUID(ctx, c, client)
		if err != nil {
			return "", err
		}
		return root + "/clients/" + url.PathEscape(id), nil
	}

	roleArgs := func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return ownerComp(cmd, args, tc)
		}
		if client != "" {
			return a.ClientRoleCompleter(&client)(cmd, args, tc)
		}
		return a.RealmRoleCompleter()(cmd, args, tc)
	}

	var effective, available bool
	list := &cobra.Command{
		Use:               "list <" + owner + ">",
		Aliases:           []string{"ls"},
		Short:             "List roles mapped to the " + owner,
		Args:              ExactArgs(1, "<"+owner+">"),
		ValidArgsFunction: NoMoreArgs(1, ownerComp),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.Client()
			if err != nil {
				return err
			}
			if effective && available {
				return errs.Usage("--effective and --available cannot be combined")
			}
			p, err := target(ctx, c, args[0])
			if err != nil {
				return err
			}
			switch {
			case effective:
				p += "/composite"
			case available:
				p += "/available"
			}
			v, err := c.Get(ctx, p, nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, RoleColumns)
		},
	}
	list.Flags().BoolVar(&effective, "effective", false, "include roles inherited through composites and groups")
	list.Flags().BoolVar(&available, "available", false, "list roles that could still be mapped")

	change := func(verb, short string, del bool) *cobra.Command {
		return &cobra.Command{
			Use:               verb + " <" + owner + "> <role>...",
			Short:             short,
			Args:              MinArgs(2, "<"+owner+"> <role>..."),
			ValidArgsFunction: roleArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				c, err := a.Client()
				if err != nil {
					return err
				}
				p, err := target(ctx, c, args[0])
				if err != nil {
					return err
				}
				roles, err := ResolveRoles(ctx, c, client, args[1:])
				if err != nil {
					return err
				}
				if del {
					_, err = c.Delete(ctx, p, roles)
				} else {
					_, err = c.Post(ctx, p, roles)
				}
				if err != nil {
					return err
				}
				where := "realm"
				if client != "" {
					where = "client '" + client + "'"
				}
				past := map[bool]string{false: "Added", true: "Removed"}[del]
				a.Printer.Success("%s %d %s role(s) %s %s '%s'.", past, len(roles), where,
					map[bool]string{false: "to", true: "from"}[del], owner, args[0])
				return nil
			},
		}
	}
	group.AddCommand(list,
		change("add", "Map roles to the "+owner, false),
		change("remove", "Unmap roles from the "+owner, true))
	return group
}

// ResolveRoles fetches full role representations by name, which the mapping
// and composite endpoints require (they match on id and name). client empty
// means realm roles.
func ResolveRoles(ctx context.Context, c *kc.Client, client string, names []string) ([]any, error) {
	base := c.RealmPath("roles")
	if client != "" {
		id, err := ClientUUID(ctx, c, client)
		if err != nil {
			return nil, err
		}
		base = c.RealmPath("clients", id, "roles")
	}
	out := make([]any, 0, len(names))
	for _, n := range names {
		v, err := c.Get(ctx, base+"/"+url.PathEscape(n), nil)
		if err != nil {
			if errs.As(err).Code == errs.CodeNotFound {
				where := "realm '" + c.Realm + "'"
				if client != "" {
					where = fmt.Sprintf("client '%s'", client)
				}
				return nil, errs.NotFound("no role named '%s' in %s", n, where)
			}
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
