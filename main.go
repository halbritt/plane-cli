// plane is a JSON-first CLI for the public REST API of a self-hosted
// Plane instance. See internal/cli for the command tree and README.md for
// the output contract.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"plane-cli/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := cli.Main(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}
