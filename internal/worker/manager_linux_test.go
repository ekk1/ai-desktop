package worker

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type fixedResolver []net.IPAddr

func (resolver fixedResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr(resolver), nil
}

func TestLoopbackReadinessDialRejectsOffLoopbackResolution(t *testing.T) {
	client := newLoopbackReadinessHTTPClient(fixedResolver{{IP: net.ParseIP("203.0.113.20")}})
	request, err := http.NewRequest(http.MethodGet, "http://localhost:8080/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(request)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("readiness request error = %v, want loopback rejection", err)
	}
}

func shellRequest(command string) StartRequest {
	return StartRequest{
		Command:          command,
		StopGraceSeconds: 1,
		LogBufferBytes:   MinLogBufferBytes,
		Readiness:        Readiness{Kind: ReadinessNone, TimeoutSeconds: 5},
	}
}

func TestManagerRunsOnlyOneProcessGroupAndPreservesRawLogs(t *testing.T) {
	manager := NewManager("instance-test")
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	first, err := manager.Start(context.Background(), shellRequest("trap 'exit 0' TERM; echo stdout-line; echo stderr-line >&2; while :; do sleep 0.1; done"))
	if err != nil {
		t.Fatal(err)
	}
	if first.State != StateRunning || first.InstanceID != "instance-test" || first.PID <= 0 {
		t.Fatalf("first run = %#v", first)
	}
	if _, err := manager.Start(context.Background(), shellRequest("echo second")); !errors.Is(err, ErrSlotBusy) {
		t.Fatalf("second Start() error = %v, want ErrSlotBusy", err)
	}

	waitForLog(t, manager, first.RunID, "stdout-line\nstderr-line\n")
	if _, err := manager.Stop(context.Background(), "stale-run"); !errors.Is(err, ErrRunMismatch) {
		t.Fatalf("stale Stop() error = %v, want ErrRunMismatch", err)
	}
	stopped, err := manager.Stop(context.Background(), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != StateStopped || stopped.EndedAt == nil || stopped.ExitCode == nil || *stopped.ExitCode != 0 {
		t.Fatalf("stopped run = %#v", stopped)
	}
}

func TestManagerRecordsNaturalFailure(t *testing.T) {
	manager := NewManager("instance-test")
	run, err := manager.Start(context.Background(), shellRequest("echo boom; exit 7"))
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForRunState(t, manager, run.RunID, StateFailed)
	if failed.ExitCode == nil || *failed.ExitCode != 7 {
		t.Fatalf("exit code = %#v", failed.ExitCode)
	}
	if !strings.Contains(string(mustLogSnapshot(t, manager, run.RunID).Data), "boom\n") {
		t.Fatalf("log = %q", mustLogSnapshot(t, manager, run.RunID).Data)
	}
}

func TestManagerReadinessTransitions(t *testing.T) {
	t.Run("delay", func(t *testing.T) {
		manager := NewManager("delay-instance")
		t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
		request := shellRequest("trap 'exit 0' TERM; while :; do sleep 0.1; done")
		request.Readiness = Readiness{Kind: ReadinessDelay, DelaySeconds: 1, TimeoutSeconds: 3}
		run, err := manager.Start(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if run.State != StateStarting {
			t.Fatalf("initial state = %q", run.State)
		}
		waitForRunState(t, manager, run.RunID, StateRunning)
	})

	t.Run("log regex", func(t *testing.T) {
		manager := NewManager("regex-instance")
		t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
		request := shellRequest("echo loading; sleep 0.1; echo 'server ready'; trap 'exit 0' TERM; while :; do sleep 0.1; done")
		request.Readiness = Readiness{Kind: ReadinessLogRegex, Pattern: `server ready`, TimeoutSeconds: 3}
		run, err := manager.Start(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		waitForRunState(t, manager, run.RunID, StateRunning)
	})

	t.Run("http", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()
		manager := NewManager("http-instance")
		t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
		request := shellRequest("trap 'exit 0' TERM; while :; do sleep 0.1; done")
		request.Readiness = Readiness{Kind: ReadinessHTTP, URL: server.URL, TimeoutSeconds: 3}
		run, err := manager.Start(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		waitForRunState(t, manager, run.RunID, StateRunning)
	})
}

func TestManagerReadinessTimeoutFailsAndStopsProcess(t *testing.T) {
	manager := NewManager("instance-test")
	request := shellRequest("trap 'exit 0' TERM; while :; do sleep 0.1; done")
	request.Readiness = Readiness{Kind: ReadinessLogRegex, Pattern: `never appears`, TimeoutSeconds: 1}
	run, err := manager.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForRunState(t, manager, run.RunID, StateFailed)
	if !strings.Contains(failed.Error, "readiness") {
		t.Fatalf("error = %q", failed.Error)
	}
}

func TestManagerHTTPReadinessDoesNotFollowRedirects(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		redirectedRequests.Add(1)
		response.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Location", target.URL)
		response.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()

	manager := NewManager("instance-test")
	request := shellRequest("trap 'exit 0' TERM; while :; do sleep 0.1; done")
	request.Readiness = Readiness{Kind: ReadinessHTTP, URL: redirect.URL, TimeoutSeconds: 1}
	run, err := manager.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunState(t, manager, run.RunID, StateFailed)
	if got := redirectedRequests.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests", got)
	}
}

func TestManagerStopKillsTermIgnoringProcessGroup(t *testing.T) {
	manager := NewManager("instance-test")
	request := shellRequest("trap '' TERM; (trap '' TERM; while :; do sleep 1; done) & wait")
	run, err := manager.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	stopped, err := manager.Stop(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != StateStopped {
		t.Fatalf("state = %q", stopped.State)
	}
	if err := syscall.Kill(-run.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process group %d still exists: %v", run.PID, err)
	}
}

func TestManagerNaturalLeaderExitCleansRemainingProcessGroup(t *testing.T) {
	manager := NewManager("instance-test")
	run, err := manager.Start(context.Background(), shellRequest("(trap '' TERM; while :; do sleep 1; done) & exit 0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-run.PID, syscall.SIGKILL) })
	stopped := waitForRunState(t, manager, run.RunID, StateStopped)
	if stopped.ExitCode == nil || *stopped.ExitCode != 0 {
		t.Fatalf("exit code = %#v", stopped.ExitCode)
	}
	if err := syscall.Kill(-run.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process group %d still exists after natural leader exit: %v", run.PID, err)
	}
}

func TestManagerReadinessFailureCleansRemainingProcessGroup(t *testing.T) {
	manager := NewManager("instance-test")
	request := shellRequest("trap 'exit 0' TERM; (trap '' TERM; while :; do sleep 1; done) & while :; do sleep 1; done")
	request.Readiness = Readiness{Kind: ReadinessLogRegex, Pattern: "never", TimeoutSeconds: 1}
	run, err := manager.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-run.PID, syscall.SIGKILL) })
	failed := waitForRunState(t, manager, run.RunID, StateFailed)
	if !strings.Contains(failed.Error, "readiness") {
		t.Fatalf("error = %q, want readiness failure", failed.Error)
	}
	if err := syscall.Kill(-run.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process group %d still exists after readiness failure: %v", run.PID, err)
	}
}

func TestManagerPropagatesProcessGroupCleanupFailure(t *testing.T) {
	cleanupFailure := errors.New("injected process group cleanup failure")
	manager := NewManager("instance-test")
	manager.cleanupProcessGroup = func(int) error { return cleanupFailure }
	run, err := manager.Start(context.Background(), shellRequest("exit 0"))
	if err != nil {
		t.Fatal(err)
	}
	waitForRunState(t, manager, run.RunID, StateStopped)

	if _, err := manager.Stop(context.Background(), run.RunID); !errors.Is(err, cleanupFailure) {
		t.Fatalf("terminal Stop error = %v, want cleanup failure", err)
	}
	if err := manager.Shutdown(context.Background()); !errors.Is(err, cleanupFailure) {
		t.Fatalf("Shutdown error = %v, want cleanup failure", err)
	}
	if _, err := manager.Start(context.Background(), shellRequest("exit 0")); !errors.Is(err, cleanupFailure) {
		t.Fatalf("next Start error = %v, want cleanup failure", err)
	}
}

func TestManagerShutdownStopsActiveRun(t *testing.T) {
	manager := NewManager("instance-test")
	run, err := manager.Start(context.Background(), shellRequest("trap 'exit 0' TERM; while :; do sleep 0.1; done"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	status := manager.Status()
	if status.Run == nil || status.Run.RunID != run.RunID || status.Run.State != StateStopped {
		t.Fatalf("status = %#v", status)
	}
}

func waitForLog(t *testing.T, manager *Manager, runID, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := manager.LogSnapshot(runID)
		if err == nil && string(snapshot.Data) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("log did not become %q; got %q", want, mustLogSnapshot(t, manager, runID).Data)
}

func waitForRunState(t *testing.T, manager *Manager, runID string, want RunState) Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := manager.Status()
		if status.Run != nil && status.Run.RunID == runID && status.Run.State == want {
			return *status.Run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach %s; status = %#v", runID, want, manager.Status())
	return Run{}
}

func mustLogSnapshot(t *testing.T, manager *Manager, runID string) LogSnapshot {
	t.Helper()
	snapshot, err := manager.LogSnapshot(runID)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
