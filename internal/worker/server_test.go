package worker

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

func TestRunServerBindsLoopbackAndStopsManagedProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	addresses := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- RunServer(ctx, ServerOptions{
			Port:     0,
			Version:  "v-test",
			OnListen: func(address string) { addresses <- address },
		})
	}()

	var address string
	select {
	case address = <-addresses:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not start listening")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("host = %q", host)
	}
	client := Client{BaseURL: "http://" + address, MaxResponseBytes: 1 << 20}
	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.Version != "v-test" || health.InstanceID == "" {
		t.Fatalf("health = %#v", health)
	}
	run, err := client.Start(context.Background(), shellRequest("trap '' TERM; while :; do sleep 1; done"))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunServer() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
	if err := syscall.Kill(-run.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("managed process group %d remains: %v", run.PID, err)
	}
}

func TestRunServerRejectsInvalidPorts(t *testing.T) {
	t.Parallel()
	for _, port := range []int{-1, 65536} {
		if err := RunServer(context.Background(), ServerOptions{Port: port, Version: "test"}); err == nil {
			t.Errorf("RunServer(port=%d) error = nil", port)
		}
	}
}

func TestWorkerHTTPServerBoundsRequestReadsWithoutBoundingSSEWrites(t *testing.T) {
	server := newHTTPServer("test", NewManager("instance-test"))
	if server.ReadTimeout <= 0 {
		t.Fatalf("ReadTimeout = %v, want bounded JSON request reads", server.ReadTimeout)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want unbounded SSE writes", server.WriteTimeout)
	}
}
