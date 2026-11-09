package auth

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// OpenBrowser launches the user's browser. $BROWSER wins when set (the
// common convention on Linux, and how tests drive the flow); otherwise the
// platform opener is used.
func OpenBrowser(u string) error {
	if b := os.Getenv("BROWSER"); b != "" {
		parts := strings.Fields(b)
		return exec.Command(parts[0], append(parts[1:], u)...).Start()
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}
