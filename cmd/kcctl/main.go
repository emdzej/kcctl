package main

import (
	"os"

	"github.com/emdzej/keycloak-cli/internal/root"
)

func main() {
	os.Exit(root.Main(os.Args[1:]))
}
