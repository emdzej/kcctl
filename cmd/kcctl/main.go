package main

import (
	"os"

	"github.com/emdzej/kcctl/internal/root"
)

func main() {
	os.Exit(root.Main(os.Args[1:]))
}
