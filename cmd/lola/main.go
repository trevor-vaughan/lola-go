// Command lola installs AI-assistant modules. See `lola help`.
package main

import (
	"os"

	"github.com/trevor-vaughan/lola-go/internal/cli"
)

func main() {
	os.Exit(cli.Execute(cli.NewRootCmd(os.Getenv)))
}
