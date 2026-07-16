package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/markos-narinin/git-snapshot/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cli := app.New()
	err := cli.Run(ctx, os.Args[1:])
	if err != nil {
		app.WriteError(os.Stderr, err, app.ErrorWantsJSON(os.Args[1:]))
		os.Exit(app.ExitCode(err))
	}
}
