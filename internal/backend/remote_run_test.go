package backend

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ekk1/ai-desktop/internal/worker"
)

func TestManagerStartsWorkerProfileRemotelyAndSavesLogsLocally(t *testing.T) {
	remoteManager := worker.NewManager("worker-instance")
	t.Cleanup(func() { _ = remoteManager.Shutdown(context.Background()) })
	remoteServer := httptest.NewServer(worker.NewHandler("test", remoteManager))
	defer remoteServer.Close()

	root := t.TempDir()
	repository, err := OpenRepository(filepath.Join(root, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	profile := DefaultProfile()
	profile.Name = "Remote llama"
	profile.Command = "trap 'exit 0' TERM; printf '%s' ${MESSAGE_SH}; while :; do sleep 0.1; done"
	profile.Variables = map[string]string{"MESSAGE": "default"}
	profile.Execution = Execution{Kind: ExecutionWorker, WorkerBaseURL: remoteServer.URL}
	created, err := repository.Create(profile)
	if err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(root, "logs")
	manager := NewManager(repository, logDir)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})

	run, err := manager.Start(context.Background(), created.ID, map[string]string{"MESSAGE": "remote marker"})
	if err != nil {
		t.Fatal(err)
	}
	if run.ExecutionKind != ExecutionWorker || run.WorkerInstanceID != "worker-instance" || run.WorkerRunID == "" || run.PID <= 0 {
		t.Fatalf("remote run = %#v", run)
	}
	if run.ProfileSnapshot.Command != profile.Command {
		t.Fatalf("profile snapshot command = %q", run.ProfileSnapshot.Command)
	}
	waitForLog(t, manager, created.ID, "remote marker")
	path, err := manager.SaveLog(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "remote marker" {
		t.Fatalf("saved remote log = %q, %v", contents, err)
	}
	if !strings.HasPrefix(path, logDir+string(os.PathSeparator)) {
		t.Fatalf("saved path %q is outside %q", path, logDir)
	}
	if err := manager.Stop(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	stopped, ok := manager.Run(created.ID)
	if !ok || stopped.State != StateStopped || stopped.ConnectionState != ConnectionConnected {
		t.Fatalf("stopped run = %#v, found = %v", stopped, ok)
	}
}

func TestManagerDoesNotMarkRemoteRunStoppedWhenWorkerDisconnects(t *testing.T) {
	remoteManager := worker.NewManager("worker-instance")
	t.Cleanup(func() { _ = remoteManager.Shutdown(context.Background()) })
	remoteServer := httptest.NewServer(worker.NewHandler("test", remoteManager))

	root := t.TempDir()
	repository, err := OpenRepository(filepath.Join(root, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	profile := DefaultProfile()
	profile.Name = "Remote SD"
	profile.Command = "trap 'exit 0' TERM; while :; do sleep 0.1; done"
	profile.Execution = Execution{Kind: ExecutionWorker, WorkerBaseURL: remoteServer.URL}
	created, err := repository.Create(profile)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(repository, filepath.Join(root, "logs"))
	if _, err := manager.Start(context.Background(), created.ID, nil); err != nil {
		t.Fatal(err)
	}
	remoteServer.CloseClientConnections()
	remoteServer.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, _ := manager.Run(created.ID)
		if run.ConnectionState == ConnectionUnknown {
			if run.State != StateRunning && run.State != StateStarting {
				t.Fatalf("disconnected state = %q", run.State)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = manager.Shutdown(ctx)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("connection did not become unknown: %#v", mustRun(t, manager, created.ID))
}

func TestManagerRecoversRemoteStartWhenResponseIsLost(t *testing.T) {
	fake := newFakeWorkerClient()
	fake.startErr = io.ErrUnexpectedEOF
	fake.startRun = worker.Run{
		RunID: "worker-run", InstanceID: "worker-instance", State: worker.StateRunning,
		PID: 4321, StartedAt: time.Now().UTC(), Request: worker.StartRequest{
			Command: "server --port 8080", Env: nil, StopGraceSeconds: 10, LogBufferBytes: 1 << 20,
			Readiness: worker.Readiness{Kind: worker.ReadinessNone, TimeoutSeconds: 60},
		},
	}
	fake.status = worker.StatusResponse{Run: &fake.startRun}
	manager, profile := newRemoteFakeManager(t, fake)

	run, err := manager.Start(context.Background(), profile.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.WorkerRunID != "worker-run" || run.WorkerInstanceID != "worker-instance" || run.State != StateRunning {
		t.Fatalf("recovered run = %#v", run)
	}
}

func TestManagerInterruptsRemoteRunWhenWorkerIdentityChanges(t *testing.T) {
	fake := newFakeWorkerClient()
	fake.startRun = worker.Run{
		RunID: "worker-run", InstanceID: "worker-instance", State: worker.StateRunning,
		PID: 4321, StartedAt: time.Now().UTC(),
	}
	fake.status = worker.StatusResponse{Run: &fake.startRun}
	manager, profile := newRemoteFakeManager(t, fake)
	if _, err := manager.Start(context.Background(), profile.ID, nil); err != nil {
		t.Fatal(err)
	}

	replacement := fake.startRun
	replacement.RunID = "replacement-run"
	fake.setStatus(worker.StatusResponse{Run: &replacement})
	waitForState(t, manager, profile.ID, StateInterrupted)
	if err := manager.Stop(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	stopCalls := fake.stopCalls
	fake.mu.Unlock()
	if stopCalls != 0 {
		t.Fatalf("replacement worker run received %d stop calls", stopCalls)
	}
}

func TestManagerDoesNotRecoverLostStartFromDifferentWorkerRequest(t *testing.T) {
	fake := newFakeWorkerClient()
	fake.startErr = io.ErrUnexpectedEOF
	fake.startRun = worker.Run{
		RunID: "unrelated-run", InstanceID: "worker-instance", State: worker.StateRunning,
		PID: 4321, StartedAt: time.Now().UTC(), Request: worker.StartRequest{
			Command: "different command", StopGraceSeconds: 10, LogBufferBytes: worker.MinLogBufferBytes,
			Readiness: worker.Readiness{Kind: worker.ReadinessNone, TimeoutSeconds: 60},
		},
	}
	fake.status = worker.StatusResponse{Run: &fake.startRun}
	manager, profile := newRemoteFakeManager(t, fake)
	if _, err := manager.Start(context.Background(), profile.ID, nil); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestManagerDoesNotAdoptExistingRunAfterStructuredStartRejection(t *testing.T) {
	fake := newFakeWorkerClient()
	fake.startErr = &worker.ClientError{StatusCode: 409, Code: "slot_busy", Message: "already running"}
	fake.startRun = worker.Run{
		RunID: "existing-run", InstanceID: "worker-instance", State: worker.StateRunning,
		PID: 4321, StartedAt: time.Now().UTC(),
	}
	fake.status = worker.StatusResponse{Run: &fake.startRun}
	manager, profile := newRemoteFakeManager(t, fake)
	_, err := manager.Start(context.Background(), profile.ID, nil)
	var clientErr *worker.ClientError
	if !errors.As(err, &clientErr) || clientErr.Code != "slot_busy" {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestManagerShutdownWaitsForBlockedRemoteStartAndStopsResolvedRun(t *testing.T) {
	fake := newFakeWorkerClient()
	fake.startRun = worker.Run{
		RunID: "worker-run", InstanceID: "worker-instance", State: worker.StateRunning,
		PID: 4321, StartedAt: time.Now().UTC(),
	}
	fake.startStarted = make(chan struct{}, 1)
	fake.startRelease = make(chan struct{})
	manager, profile := newRemoteFakeManager(t, fake)

	started := make(chan error, 1)
	go func() {
		_, err := manager.Start(context.Background(), profile.ID, nil)
		started <- err
	}()
	select {
	case <-fake.startStarted:
	case <-time.After(time.Second):
		t.Fatal("remote Start did not block")
	}

	shutdown := make(chan error, 1)
	go func() { shutdown <- manager.Shutdown(context.Background()) }()
	select {
	case err := <-shutdown:
		t.Fatalf("Shutdown returned before remote Start resolved: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(fake.startRelease)
	if err := <-started; err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := <-shutdown; err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.stopCalls != 1 || len(fake.stopRunIDs) != 1 || fake.stopRunIDs[0] != "worker-run" {
		t.Fatalf("stop calls = %d, run IDs = %q", fake.stopCalls, fake.stopRunIDs)
	}
}

func TestManagerReconcilesFinalRemoteLogSnapshotBeforeCompletion(t *testing.T) {
	fake := newFakeWorkerClient()
	fake.startRun = worker.Run{
		RunID: "worker-run", InstanceID: "worker-instance", State: worker.StateRunning,
		PID: 4321, StartedAt: time.Now().UTC(),
	}
	fake.status = worker.StatusResponse{Run: &fake.startRun}
	manager, profile := newRemoteFakeManager(t, fake)
	if _, err := manager.Start(context.Background(), profile.ID, nil); err != nil {
		t.Fatal(err)
	}

	terminal := fake.startRun
	terminal.State = worker.StateStopped
	fake.setStatus(worker.StatusResponse{Run: &terminal})
	fake.setLogs(worker.LogSnapshot{StartOffset: 0, EndOffset: int64(len("final output\n")), Data: []byte("final output\n")})
	waitForState(t, manager, profile.ID, StateStopped)
	logs, err := manager.LogSnapshot(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(logs) != "final output\n" {
		t.Fatalf("logs = %q, want terminal snapshot", logs)
	}
}

func mustRun(t *testing.T, manager *Manager, profileID string) RunInfo {
	t.Helper()
	run, ok := manager.Run(profileID)
	if !ok {
		t.Fatal("run not found")
	}
	return run
}

type fakeWorkerClient struct {
	mu           sync.Mutex
	startRun     worker.Run
	startErr     error
	status       worker.StatusResponse
	health       worker.HealthResponse
	startRequest worker.StartRequest
	stopCalls    int
	stopRunIDs   []string
	logs         worker.LogSnapshot
	startStarted chan struct{}
	startRelease chan struct{}
}

func newFakeWorkerClient() *fakeWorkerClient {
	return &fakeWorkerClient{health: worker.HealthResponse{Status: "ok", Version: "test", InstanceID: "worker-instance"}}
}

func (fake *fakeWorkerClient) Health(context.Context) (worker.HealthResponse, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.health, nil
}

func (fake *fakeWorkerClient) Status(context.Context) (worker.StatusResponse, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	status := fake.status
	if status.Run != nil {
		clone := *status.Run
		status.Run = &clone
	}
	return status, nil
}

func (fake *fakeWorkerClient) Start(ctx context.Context, request worker.StartRequest) (worker.Run, error) {
	fake.mu.Lock()
	fake.startRequest = request
	run := fake.startRun
	if run.Request.Command == "" {
		run.Request = request
		fake.startRun.Request = request
		if fake.status.Run != nil && fake.status.Run.RunID == run.RunID {
			fake.status.Run.Request = request
		}
	}
	started, release := fake.startStarted, fake.startRelease
	fake.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return worker.Run{}, ctx.Err()
		}
	}
	return run, fake.startErr
}

func (fake *fakeWorkerClient) Stop(_ context.Context, runID string) (worker.Run, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.stopCalls++
	fake.stopRunIDs = append(fake.stopRunIDs, runID)
	run := fake.startRun
	if run.RunID != runID {
		return worker.Run{}, worker.ErrRunMismatch
	}
	run.State = worker.StateStopped
	endedAt := time.Now().UTC()
	exitCode := 0
	run.EndedAt = &endedAt
	run.ExitCode = &exitCode
	fake.status.Run = &run
	return run, nil
}

func (fake *fakeWorkerClient) Logs(context.Context, string) (worker.LogSnapshot, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.logs, nil
}

func (fake *fakeWorkerClient) setLogs(logs worker.LogSnapshot) {
	fake.mu.Lock()
	fake.logs = logs
	fake.mu.Unlock()
}

func (fake *fakeWorkerClient) SubscribeLogs(ctx context.Context, _ string) (<-chan worker.LogEvent, <-chan error, error) {
	events := make(chan worker.LogEvent)
	failures := make(chan error)
	go func() {
		<-ctx.Done()
		close(events)
		close(failures)
	}()
	return events, failures, nil
}

func (fake *fakeWorkerClient) setStatus(status worker.StatusResponse) {
	fake.mu.Lock()
	fake.status = status
	fake.mu.Unlock()
}

func newRemoteFakeManager(t *testing.T, fake WorkerClient) (*Manager, Profile) {
	t.Helper()
	root := t.TempDir()
	repository, err := OpenRepository(filepath.Join(root, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	profile := DefaultProfile()
	profile.Name = "Remote test"
	profile.Command = "server --port 8080"
	profile.Execution = Execution{Kind: ExecutionWorker, WorkerBaseURL: "http://127.0.0.1:8288"}
	profile, err = repository.Create(profile)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(repository, filepath.Join(root, "logs"), func(string) WorkerClient { return fake })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	return manager, profile
}
