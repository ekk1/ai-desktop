package backend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestManagerStartsOneProfileAndCapturesRawOutput(t *testing.T) {
	manager, repository := newTestManager(t)
	workDir := t.TempDir()
	profile := createTestProfile(t, repository, "server", "printf '%s:%s' \"$VALUE\" ${ARG_SH}; sleep 30")
	profile.WorkDir = workDir
	profile.Env = map[string]string{"VALUE": "environment"}
	profile.Variables = map[string]string{"ARG": "default"}
	profile, err := repository.Update(profile.ID, profile)
	if err != nil {
		t.Fatal(err)
	}

	run, err := manager.Start(profile.ID, map[string]string{"ARG": "override"})
	if err != nil {
		t.Fatal(err)
	}
	if run.State != StateRunning || run.PID <= 0 || run.ProfileSnapshot.WorkDir != workDir {
		t.Fatalf("run = %#v", run)
	}
	if _, err := manager.Start(profile.ID, nil); !errors.Is(err, ErrRunning) {
		t.Fatalf("second Start error = %v", err)
	}
	waitForLog(t, manager, profile.ID, "environment:override")
	if err := manager.Stop(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	stopped, ok := manager.Run(profile.ID)
	if !ok || stopped.State != StateStopped {
		t.Fatalf("stopped run = %#v, %v", stopped, ok)
	}
}

func TestManagerStopsWholeProcessGroup(t *testing.T) {
	manager, repository := newTestManager(t)
	profile := createTestProfile(t, repository, "tree", "sleep 30 & child=$!; printf '%s\\n' \"$child\"; wait")
	if _, err := manager.Start(profile.ID, nil); err != nil {
		t.Fatal(err)
	}
	log := waitForLog(t, manager, profile.ID, "\n")
	childPID, err := strconv.Atoi(strings.TrimSpace(log))
	if err != nil {
		t.Fatalf("child PID log %q: %v", log, err)
	}

	if err := manager.Stop(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(childPID, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(childPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child process %d still exists: %v", childPID, err)
	}
}

func TestManagerKillsProcessGroupWhenStopContextExpires(t *testing.T) {
	manager, repository := newTestManager(t)
	profile := createTestProfile(t, repository, "timeout", "trap '' TERM; printf 'ready'; while :; do sleep 1; done")
	profile.StopGraceSeconds = 10
	profile, err := repository.Update(profile.ID, profile)
	if err != nil {
		t.Fatal(err)
	}
	run, err := manager.Start(profile.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-run.PID, syscall.SIGKILL)
	waitForLog(t, manager, profile.ID, "ready")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = manager.Stop(ctx, profile.ID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop error = %v, want deadline exceeded", err)
	}
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(run.PID, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(run.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("backend PID %d survived expired Stop context: %v", run.PID, err)
	}
}

func TestManagerSavesCrashLogOnlyForUnexpectedFailure(t *testing.T) {
	manager, repository := newTestManager(t)
	profile := createTestProfile(t, repository, "crash", "printf 'fatal output'; exit 7")
	if _, err := manager.Start(profile.ID, nil); err != nil {
		t.Fatal(err)
	}
	waitForState(t, manager, profile.ID, StateFailed)

	entries, err := os.ReadDir(manager.crashLogDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "crash-") {
		t.Fatalf("crash logs = %#v", entries)
	}
	contents, err := os.ReadFile(filepath.Join(manager.crashLogDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "fatal output" {
		t.Fatalf("crash log = %q", contents)
	}
}

func TestManagerWaitsForLogReadiness(t *testing.T) {
	manager, repository := newTestManager(t)
	profile := createTestProfile(t, repository, "ready", "printf 'booting\\n'; sleep 0.05; printf 'READY\\n'; sleep 30")
	profile.Readiness = Readiness{Kind: ReadinessLogRegex, Pattern: `(?m)^READY$`, TimeoutSeconds: 2}
	profile, err := repository.Update(profile.ID, profile)
	if err != nil {
		t.Fatal(err)
	}

	run, err := manager.Start(profile.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != StateStarting {
		t.Fatalf("initial state = %q, want starting", run.State)
	}
	waitForState(t, manager, profile.ID, StateRunning)
	if err := manager.Stop(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
}

func TestManagerWaitsForDelayReadiness(t *testing.T) {
	manager, repository := newTestManager(t)
	profile := createTestProfile(t, repository, "delay", "sleep 30")
	profile.Readiness = Readiness{Kind: ReadinessDelay, DelaySeconds: 1, TimeoutSeconds: 2}
	profile, err := repository.Update(profile.ID, profile)
	if err != nil {
		t.Fatal(err)
	}

	run, err := manager.Start(profile.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != StateStarting {
		t.Fatalf("initial state = %q, want starting", run.State)
	}
	waitForState(t, manager, profile.ID, StateRunning)
	if err := manager.Stop(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
}

func TestManagerWaitsForHTTPReadiness(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	defer health.Close()

	manager, repository := newTestManager(t)
	profile := createTestProfile(t, repository, "http", "sleep 30")
	profile.Readiness = Readiness{Kind: ReadinessHTTP, URL: health.URL, TimeoutSeconds: 2}
	profile, err := repository.Update(profile.ID, profile)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Start(profile.ID, nil); err != nil {
		t.Fatal(err)
	}
	waitForState(t, manager, profile.ID, StateRunning)
	if err := manager.Stop(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
}

func TestManagerCanSaveAndClearCurrentLog(t *testing.T) {
	manager, repository := newTestManager(t)
	profile := createTestProfile(t, repository, "save", "printf 'save me'; sleep 30")
	if _, err := manager.Start(profile.ID, nil); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, manager, profile.ID, "save me")
	path, err := manager.SaveLog(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "save me" {
		t.Fatalf("saved log = %q, %v", contents, err)
	}
	if err := manager.ClearLog(profile.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := manager.LogSnapshot(profile.ID); err != nil || len(got) != 0 {
		t.Fatalf("LogSnapshot after clear = %q, %v", got, err)
	}
	if err := manager.Stop(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
}

func newTestManager(t *testing.T) (*Manager, *Repository) {
	t.Helper()
	root := t.TempDir()
	repository, err := OpenRepository(filepath.Join(root, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(repository, filepath.Join(root, "crash-logs"))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	return manager, repository
}

func createTestProfile(t *testing.T, repository *Repository, name, command string) Profile {
	t.Helper()
	profile := DefaultProfile()
	profile.Name = name
	profile.Command = command
	profile.StopGraceSeconds = 1
	created, err := repository.Create(profile)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func waitForLog(t *testing.T, manager *Manager, profileID, expected string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		contents, err := manager.LogSnapshot(profileID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(contents), expected) {
			return string(contents)
		}
		if time.Now().After(deadline) {
			t.Fatalf("log did not contain %q; got %q", expected, contents)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForState(t *testing.T, manager *Manager, profileID string, state State) RunInfo {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		run, ok := manager.Run(profileID)
		if ok && run.State == state {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not reach %q; got %#v", state, run)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
