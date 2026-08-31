package worker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

const serverShutdownTimeout = 15 * time.Second

type ServerOptions struct {
	Port     int
	Version  string
	OnListen func(address string)
}

func RunServer(ctx context.Context, options ServerOptions) error {
	if options.Port < 0 || options.Port > 65535 {
		return fmt.Errorf("port must be between 0 and 65535")
	}
	instanceID, err := randomRunID()
	if err != nil {
		return fmt.Errorf("generate worker instance ID: %w", err)
	}
	manager := NewManager(instanceID)
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(options.Port)))
	if err != nil {
		return fmt.Errorf("listen on worker loopback port %d: %w", options.Port, err)
	}
	server := &http.Server{
		Handler:           NewHandler(options.Version, manager),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if options.OnListen != nil {
		options.OnListen(listener.Addr().String())
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()

	select {
	case serveErr := <-serveResult:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		managerErr := manager.Shutdown(shutdownCtx)
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(serveErr, managerErr)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		serverDone := make(chan error, 1)
		managerDone := make(chan error, 1)
		go func() { serverDone <- server.Shutdown(shutdownCtx) }()
		go func() { managerDone <- manager.Shutdown(shutdownCtx) }()
		serverErr := <-serverDone
		serveErr := <-serveResult
		managerErr := <-managerDone
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(serverErr, serveErr, managerErr)
	}
}
