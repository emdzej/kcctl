// Package sessioncmd manages realm-wide sessions (stats, sign-out, revocation)
// and brute-force lockouts.
package sessioncmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/emdzej/kcctl/internal/app"
	"github.com/emdzej/kcctl/internal/kc"
	"github.com/emdzej/kcctl/internal/output"
)

// New builds the `session` group. Brute-force lockouts live under it as
// `session brute-force`; BruteForce builds the same group for mounting at the
// top level as well.
func New(a *app.App) *cobra.Command {
	cmd := app.Group("session", "Manage realm sessions and brute-force lockouts", "sessions")
	cmd.Long = `Manage realm-wide sessions and brute-force lockouts.

A single user's sessions are under 'kcctl user sessions' and 'kcctl user logout'.`
	cmd.AddCommand(stats(a), deleteSession(a),
		globalAction(a, "logout-all", "Sign out every user in the realm", "logout-all",
			"This signs out every user in realm '%s' and tells clients with an admin URL to drop their sessions."),
		globalAction(a, "push-revocation", "Push the realm's not-before revocation policy to clients with an admin URL", "push-revocation",
			"This pushes the revocation policy of realm '%s' to every client with an admin URL, invalidating tokens issued before it."),
		BruteForce(a),
	)
	return cmd
}

func stats(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "stats", Short: "Count active and offline sessions per client", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			v, err := c.Get(cmd.Context(), c.RealmPath("client-session-stats"), nil)
			if err != nil {
				return err
			}
			return a.Printer.Print(v, []output.Column{
				output.Col("client", ".clientId"), output.Col("active", ".active"),
				output.Col("offline", ".offline"), output.Col("id", ".id"),
			})
		},
	}
}

func deleteSession(a *app.App) *cobra.Command {
	var offline, yes bool
	cmd := &cobra.Command{
		Use: "delete <session-id>...", Aliases: []string{"rm"},
		Short:   "End sessions by id (see 'kcctl user sessions <user>')",
		Example: "  kcctl session delete 3f0c...\n  kcctl session delete 3f0c... --offline",
		Args:    app.MinArgs(1, "<session-id>..."),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			if len(args) > 1 {
				if err := a.Confirm(yes, fmt.Sprintf("This ends %d sessions in realm '%s'.", len(args), c.Realm)); err != nil {
					return err
				}
			}
			var q map[string][]string
			if offline {
				q = map[string][]string{"isOffline": {"true"}}
			}
			for _, id := range args {
				if _, err := c.Do(cmd.Context(), kc.Request{Method: "DELETE", Path: c.RealmPath("sessions", id), Query: q}); err != nil {
					return err
				}
				a.Printer.Success("Ended session %s.", id)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "the ids are offline sessions")
	app.YesFlag(cmd, &yes)
	return cmd
}

func globalAction(a *app.App, use, short, seg, what string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: use, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			if err := a.Confirm(yes, fmt.Sprintf(what, c.Realm)); err != nil {
				return err
			}
			resp, err := c.Post(cmd.Context(), c.RealmPath(seg), nil)
			if err != nil {
				return err
			}
			v, err := resp.JSON()
			if err != nil {
				return err
			}
			if a.Printer.IsStructured() {
				return a.Printer.Print(v, nil)
			}
			m, _ := v.(map[string]any)
			ok, _ := m["successRequests"].([]any)
			failed, _ := m["failedRequests"].([]any)
			a.Printer.Success("Done in realm '%s'; %d client(s) notified.", c.Realm, len(ok))
			if len(failed) > 0 {
				a.Printer.Warn("Could not notify %d client admin URL(s): %v", len(failed), failed)
			}
			return nil
		},
	}
	app.YesFlag(cmd, &yes)
	return cmd
}

// BruteForce builds the `brute-force` group (attack-detection endpoints).
func BruteForce(a *app.App) *cobra.Command {
	cmd := app.Group("brute-force", "Inspect and clear brute-force lockouts", "attack-detection")
	comp := app.NoMoreArgs(1, a.UserCompleter())
	userPath := func(cmd *cobra.Command, ref string) (*kc.Client, string, error) {
		c, err := a.Client()
		if err != nil {
			return nil, "", err
		}
		id, err := app.Users.Find(cmd.Context(), c, ref)
		if err != nil {
			return nil, "", err
		}
		return c, c.RealmPath("attack-detection", "brute-force", "users", id), nil
	}
	var yes bool
	clearAll := &cobra.Command{
		Use: "clear-all", Short: "Clear every user's login failures and lockouts", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			if err := a.Confirm(yes, fmt.Sprintf("This clears login failures and lockouts of every user in realm '%s'.", c.Realm)); err != nil {
				return err
			}
			if _, err := c.Delete(cmd.Context(), c.RealmPath("attack-detection", "brute-force", "users"), nil); err != nil {
				return err
			}
			a.Printer.Success("Cleared all brute-force lockouts in realm '%s'.", c.Realm)
			return nil
		},
	}
	app.YesFlag(clearAll, &yes)
	cmd.AddCommand(
		&cobra.Command{
			Use: "status <user>", Short: "Show a user's login failures and lockout state",
			Args: app.ExactArgs(1, "<user>"), ValidArgsFunction: comp,
			RunE: func(cmd *cobra.Command, args []string) error {
				c, p, err := userPath(cmd, args[0])
				if err != nil {
					return err
				}
				v, err := c.Get(cmd.Context(), p, nil)
				if err != nil {
					return err
				}
				return a.Printer.Print(v, nil)
			},
		},
		&cobra.Command{
			Use: "clear <user>", Short: "Clear a user's login failures and lockout",
			Args: app.ExactArgs(1, "<user>"), ValidArgsFunction: comp,
			RunE: func(cmd *cobra.Command, args []string) error {
				c, p, err := userPath(cmd, args[0])
				if err != nil {
					return err
				}
				if _, err := c.Delete(cmd.Context(), p, nil); err != nil {
					return err
				}
				a.Printer.Success("Cleared brute-force state of '%s'.", args[0])
				return nil
			},
		},
		clearAll,
	)
	return cmd
}
