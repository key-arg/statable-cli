// Command statable reads Statable analytics from a terminal, a script, or CI.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/cli"
)

func main() {
	// Ctrl-C and SIGTERM cancel in-flight requests instead of leaving the
	// process to be killed mid-write. Which signal arrived decides the exit
	// status: 130 for SIGINT, 143 for SIGTERM. A CI runner that kills a step
	// on its own timeout must not be reported as an operator pressing Ctrl-C.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		s, ok := <-sig
		if !ok {
			return
		}
		if s == syscall.SIGTERM {
			api.ExitInterrupted = 143
		}
		cancel()

		// The first signal cancels in-flight work so the process can exit
		// cleanly. After that the default behaviour is restored: a command
		// blocked on a terminal read does not watch the context, so without
		// this the interrupt key stopped working entirely and the process
		// could only be killed from another terminal.
		signal.Stop(sig)
		signal.Reset(os.Interrupt, syscall.SIGTERM)
	}()

	root, rt := cli.NewRoot(os.Stdout, os.Stderr)

	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(cli.Render(rt, err))
	}
}
