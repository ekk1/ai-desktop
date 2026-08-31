package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ekk1/ai-desktop/internal/worker"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ai-worker", flag.ContinueOnError)
	flags.SetOutput(stderr)
	port := flags.Int("port", 8288, "loopback HTTP port")
	showVersion := flags.Bool("version", false, "print version and exit")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "ai-worker does not accept positional arguments")
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if *port < 1 || *port > 65535 {
		fmt.Fprintln(stderr, "port must be between 1 and 65535")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := worker.RunServer(ctx, worker.ServerOptions{Port: *port, Version: version}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
