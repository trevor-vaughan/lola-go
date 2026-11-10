// Command lola installs AI-assistant modules. See `lola help`.
package main

import (
	"context"
	"errors"
	"os"

	"github.com/trevor-vaughan/lola-go/internal/cli"
)

func main() {
	ctx, stop := cli.NotifyContext(context.Background())
	root, err := cli.NewRootCmd(os.Getenv)
	if err != nil {
		stop()
		os.Exit(cli.StartupError(os.Stderr, err, os.Args[1:]))
	}
	root.SetContext(ctx)
	code := cli.Execute(root)
	stop()
	// A run a signal stopped has cleaned up; it now dies of that signal, as it would have without
	// the handler, so that a shell script calling lola stops too.
	var sig cli.Interrupted
	if errors.As(context.Cause(ctx), &sig) && code == 128+int(sig.Signal) {
		reraise(sig.Signal)
	}
	os.Exit(code)
}
