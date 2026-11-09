package app

import (
	"context"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/errs"
	"github.com/emdzej/kcctl/internal/kc"
	"github.com/emdzej/kcctl/internal/output"
)

var mapperColumns = []output.Column{
	output.Col("name", ".name"), output.Col("mapper", ".protocolMapper"),
	output.Col("protocol", ".protocol"), output.Col("id", ".id"),
}

// ProtocolMapperCmds builds `mapper list|get|create|update|delete` for an
// owner with protocol mappers (clients, client scopes). base resolves the
// owner ref to its .../protocol-mappers root.
func (a *App) ProtocolMapperCmds(owner string, ownerComp cobra.CompletionFunc, base func(ctx context.Context, c *kc.Client, ref string) (string, error)) *cobra.Command {
	group := Group("mapper", "Manage the "+owner+"'s protocol mappers", "mappers", "protocol-mapper")
	ownerArg := "<" + owner + ">"

	models := func(ctx context.Context, ref string) (*kc.Client, string, error) {
		c, err := a.Client()
		if err != nil {
			return nil, "", err
		}
		root, err := base(ctx, c, ref)
		if err != nil {
			return nil, "", err
		}
		return c, root + "/models", nil
	}
	// Mapper names are unique per owner, so names resolve by listing.
	mapperID := func(ctx context.Context, c *kc.Client, p, ref string) (string, error) {
		if IsUUID(ref) {
			return ref, nil
		}
		l := Lookup{What: "protocol mapper", NameField: "name",
			Path: func(context.Context, *kc.Client) (string, error) { return p, nil }}
		return l.Find(ctx, c, ref)
	}
	mapperComp := func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return ownerComp(cmd, args, tc)
		}
		if len(args) > 1 && cmd.Name() != "delete" {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return a.Fetched("protocol mappers", func(ctx context.Context, c *kc.Client, args []string, _ string) ([]Item, error) {
			root, err := base(ctx, c, args[0])
			if err != nil {
				return nil, err
			}
			v, err := c.Get(ctx, root+"/models", nil)
			if err != nil {
				return nil, err
			}
			return ItemsFrom(v, "name", "protocolMapper"), nil
		})(cmd, args, tc)
	}
	fields := Fields{
		{Flag: "name", Usage: "mapper name"},
		{Flag: "mapper-type", Path: "protocolMapper", Usage: "mapper type id, e.g. oidc-usermodel-attribute-mapper"},
		{Flag: "protocol", Usage: "openid-connect|saml"},
		{Flag: "config", Path: "config", Kind: KVString, Usage: "mapper config entry"},
	}

	list := &cobra.Command{
		Use: "list " + ownerArg, Aliases: []string{"ls"}, Short: "List protocol mappers",
		Args: ExactArgs(1, ownerArg), ValidArgsFunction: NoMoreArgs(1, ownerComp),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := models(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), p, nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, mapperColumns)
		},
	}
	get := &cobra.Command{
		Use: "get " + ownerArg + " <mapper>", Short: "Show a protocol mapper",
		Args: ExactArgs(2, ownerArg+" <mapper>"), ValidArgsFunction: mapperComp,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, p, err := models(ctx, args[0])
			if err != nil {
				return err
			}
			id, err := mapperID(ctx, c, p, args[1])
			if err != nil {
				return err
			}
			v, err := c.Get(ctx, p+"/"+url.PathEscape(id), nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, mapperColumns)
		},
	}

	var cb BodyFlags
	create := &cobra.Command{
		Use:   "create " + ownerArg,
		Short: "Add a protocol mapper",
		Example: `  kcctl client mapper create web --name dept --mapper-type oidc-usermodel-attribute-mapper \
    --config user.attribute=dept --config claim.name=dept --config access.token.claim=true`,
		Args: ExactArgs(1, ownerArg), ValidArgsFunction: NoMoreArgs(1, ownerComp),
	}
	cfv := fields.Bind(create)
	cb.Bind(create)
	create.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		b, err := cb.Build(a, map[string]any{"protocol": "openid-connect"})
		if err != nil {
			return err
		}
		if err := cfv.Apply(cmd, b); err != nil {
			return err
		}
		if err := RequireFields(b, "name", "protocolMapper"); err != nil {
			return err
		}
		c, p, err := models(ctx, args[0])
		if err != nil {
			return err
		}
		resp, err := c.Post(ctx, p, b)
		if err != nil {
			return err
		}
		name, _ := b["name"].(string)
		return a.ReportCreated("protocol mapper", name, resp.CreatedID())
	}

	var ub BodyFlags
	update := &cobra.Command{
		Use: "update " + ownerArg + " <mapper>", Short: "Change a protocol mapper",
		Args: ExactArgs(2, ownerArg+" <mapper>"), ValidArgsFunction: mapperComp,
	}
	ufv := fields.Bind(update)
	ub.Bind(update)
	update.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if !ub.Given() && !anyChanged(cmd, fields) {
			return errs.Usage("nothing to change").WithHint("Pass field flags, -s path=value, or -f file.")
		}
		c, p, err := models(ctx, args[0])
		if err != nil {
			return err
		}
		id, err := mapperID(ctx, c, p, args[1])
		if err != nil {
			return err
		}
		ip := p + "/" + url.PathEscape(id)
		cur, err := c.Get(ctx, ip, nil)
		if err != nil {
			return err
		}
		b, _ := cur.(map[string]any)
		if b, err = ub.Build(a, b); err != nil {
			return err
		}
		if err := ufv.Apply(cmd, b); err != nil {
			return err
		}
		if _, err := c.Put(ctx, ip, b); err != nil {
			return err
		}
		a.Printer.Success("Updated protocol mapper '%s'.", args[1])
		return nil
	}

	var yes bool
	del := &cobra.Command{
		Use: "delete " + ownerArg + " <mapper>...", Aliases: []string{"rm"}, Short: "Remove protocol mappers",
		Args: MinArgs(2, ownerArg+" <mapper>..."), ValidArgsFunction: mapperComp,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, p, err := models(ctx, args[0])
			if err != nil {
				return err
			}
			if len(args) > 2 {
				if err := a.Confirm(yes, "This removes "+itoa(len(args)-1)+" protocol mappers from "+owner+" '"+args[0]+"'."); err != nil {
					return err
				}
			}
			for _, ref := range args[1:] {
				id, err := mapperID(ctx, c, p, ref)
				if err != nil {
					return err
				}
				if _, err := c.Delete(ctx, p+"/"+url.PathEscape(id), nil); err != nil {
					return err
				}
				a.Printer.Success("Removed protocol mapper '%s'.", ref)
			}
			return nil
		},
	}
	YesFlag(del, &yes)
	group.AddCommand(list, get, create, update, del)
	return group
}

func itoa(i int) string { return IntString(i) }
