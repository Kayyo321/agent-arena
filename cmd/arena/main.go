package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Kayyo321/agent-arena/internal/arena"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cli := arena.NewCLI(os.Stdin, os.Stdout, os.Stderr)
	if e := cli.Run(ctx, os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "arena:", arena.Redact(e.Error()))
		os.Exit(1)
	}
}
