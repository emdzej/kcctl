package root

import (
	"github.com/emdzej/kcctl/internal/commands/apicmd"
	"github.com/emdzej/kcctl/internal/commands/authcmd"
	"github.com/emdzej/kcctl/internal/commands/clientcmd"
	"github.com/emdzej/kcctl/internal/commands/clientscopecmd"
	"github.com/emdzej/kcctl/internal/commands/componentcmd"
	"github.com/emdzej/kcctl/internal/commands/configcmd"
	"github.com/emdzej/kcctl/internal/commands/eventcmd"
	"github.com/emdzej/kcctl/internal/commands/flowcmd"
	"github.com/emdzej/kcctl/internal/commands/groupcmd"
	"github.com/emdzej/kcctl/internal/commands/idpcmd"
	"github.com/emdzej/kcctl/internal/commands/keycmd"
	"github.com/emdzej/kcctl/internal/commands/orgcmd"
	"github.com/emdzej/kcctl/internal/commands/policycmd"
	"github.com/emdzej/kcctl/internal/commands/realmcmd"
	"github.com/emdzej/kcctl/internal/commands/rolecmd"
	"github.com/emdzej/kcctl/internal/commands/servercmd"
	"github.com/emdzej/kcctl/internal/commands/sessioncmd"
	"github.com/emdzej/kcctl/internal/commands/usercmd"
	"github.com/emdzej/kcctl/internal/commands/workflowcmd"
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
