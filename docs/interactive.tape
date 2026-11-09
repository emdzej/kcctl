# Renders docs/interactive.gif with VHS (https://github.com/charmbracelet/vhs):
#   vhs docs/interactive.tape
# Needs a running Keycloak seeded by docs/demo-seed.sh (see docs/README.md).

Output docs/interactive.gif

Require kcctl

Set Shell "bash"
Set FontSize 15
Set Width 1180
Set Height 600
Set Padding 18
Set Theme "Dracula"
Set TypingSpeed 75ms
Set CursorBlink false

Hide
Type "export PATH=$HOME/go/bin:$PATH XDG_CONFIG_HOME=/tmp/kcdemo/cfg XDG_STATE_HOME=/tmp/kcdemo/state KCCTL_NO_KEYRING=1 PS1='$ '"
Enter
Type "clear"
Enter
Show

Type "kcctl interactive"
Sleep 400ms
Enter
Sleep 2s

# Commands and subcommands complete as you type.
Type "us"
Sleep 900ms
Tab
Type "ro"
Sleep 900ms
Tab
Type "ad"
Sleep 900ms
Tab
Sleep 1.2s

# Users come from the server.
Type "al"
Sleep 1.2s
Tab
Sleep 1.2s

# So do roles, with their descriptions.
Type "au"
Sleep 1.2s
Tab
Sleep 700ms
Enter
Sleep 2.2s

# Each line runs like it would in a shell.
Type "user list"
Sleep 500ms
Enter
Sleep 3s

# Mistakes are marked as you type.
Type "grop list"
Sleep 2s
Ctrl+U
Sleep 400ms

# Group paths complete level by level.
Type "group get /"
Sleep 1.5s
Type "staff/"
Sleep 1.8s
Tab
Sleep 600ms
Enter
Sleep 3s

Type "exit"
Sleep 400ms
Enter
Sleep 800ms
