package root

import (
	"github.com/emdzej/keycloak-cli/internal/commands/apicmd"
	"github.com/emdzej/keycloak-cli/internal/commands/authcmd"
	"github.com/emdzej/keycloak-cli/internal/commands/clientcmd"
	"github.com/emdzej/keycloak-cli/internal/commands/clientscopecmd"
	"github.com/emdzej/keycloak-cli/internal/commands/componentcmd"
	"github.com/emdzej/keycloak-cli/internal/commands/configcmd"
	"github.com/emdzej/keycloak-cli/internal/commands/eventcmd"
	"github.com/emdzej/keycloak-cli/internal/commands/flowcmd"
	"github.com/emdzej/keycloak-cli/internal/commands/groupcmd"
	"github.com/emdzej/keycloak-cli/internal/commands/idpcmd"
	"github.com/emdzej/keycloak-cli/internal/commands/keycmd"
	"github.com/emdzej/keycloak-cli/internal/commands/orgcmd"
	"github.com/emdzej/keycloak-cli/internal/commands/policycmd"
	"github.com/emdzej/keycloak-cli/internal/commands/realmcmd"
	"github.com/emdzej/keycloak-cli/internal/commands/rolecmd"
	"github.com/emdzej/keycloak-cli/internal/commands/servercmd"
	"github.com/emdzej/keycloak-cli/internal/commands/sessioncmd"
	"github.com/emdzej/keycloak-cli/internal/commands/usercmd"
	"github.com/emdzej/keycloak-cli/internal/commands/workflowcmd"
)

func init() {
	Register(
		realmcmd.New,
		usercmd.New,
		clientcmd.New,
		rolecmd.New,
		groupcmd.New,
		clientscopecmd.New,
		idpcmd.New,
		componentcmd.New,
		componentcmd.UserStorage,
		orgcmd.New,
		flowcmd.New,
		eventcmd.New,
		sessioncmd.New,
		keycmd.New,
		policycmd.New,
		workflowcmd.New,
		configcmd.New,
		authcmd.New,
		servercmd.New,
		apicmd.Token,
		apicmd.API,
	)
}
