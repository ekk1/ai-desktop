package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ekk1/ai-desktop/internal/app"
)

var version = "dev"

func main() {
	dataDir := flag.String("data-dir", "", "workbench data directory")
	port := flag.Int("port", 0, "loopback HTTP port override")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, app.Options{DataDir: *dataDir, PortOverride: *port, Version: version}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
