package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	ErrSlotBusy    = errors.New("worker process slot is busy")
	ErrRunMismatch = errors.New("worker run ID does not match the current run")
	ErrNoRun       = errors.New("worker has no run")
)

type ipAddressResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type managedProcess struct {
	cmd             *exec.Cmd
	log             *LogBuffer
	done            chan struct{}
	run             Run
	stopRequested   bool
	failureReason   string
	readinessCancel context.CancelFunc
}

type Manager struct {
	mu         sync.RWMutex
	instanceID string
	current    *managedProcess
	httpClient *http.Client
}

func NewManager(instanceID string) *Manager {
	return &Manager{
		instanceID: instanceID,
		httpClient: newLoopbackReadinessHTTPClient(net.DefaultResolver),
	}
}

func newLoopbackReadinessHTTPClient(resolver ipAddressResolver) *http.Client {
	return &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{
			Proxy:       nil,
			DialContext: loopbackDialContext(resolver),
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func loopbackDialContext(resolver ipAddressResolver) func(context.Context, string, string) (net.Conn, error) {
	dialer := net.Dialer{}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve readiness host %q: %w", host, err)
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("readiness host %q resolved to no addresses", host)
		}
		for _, resolved := range addresses {
			if !resolved.IP.IsLoopback() {
				return nil, fmt.Errorf("readiness host %q resolved to non-loopback address %s", host, resolved.IP)
			}
		}
		var lastErr error
		for _, resolved := range addresses {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

func (manager *Manager) Start(ctx context.Context, request StartRequest) (Run, error) {
	if err := request.Validate(); err != nil {
		return Run{}, err
	}
	if err := ctx.Err(); err != nil {
		return Run{}, err
	}
	runID, err := randomRunID()
	if err != nil {
		return Run{}, err
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.current != nil && isActiveState(manager.current.run.State) {
		return Run{}, ErrSlotBusy
	}

	logBuffer := NewLogBuffer(request.LogBufferBytes)
	command := exec.Command("/bin/bash", "-lc", request.Command)
	command.Dir = request.WorkDir
	command.Env = mergedEnvironment(request.Env)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	command.Stdout = logBuffer
	command.Stderr = logBuffer
	startedAt := time.Now().UTC()
	process := &managedProcess{
		cmd:  command,
		log:  logBuffer,
		done: make(chan struct{}),
		run: Run{
			RunID:      runID,
			InstanceID: manager.instanceID,
			State:      StateStarting,
			StartedAt:  startedAt,
			Request:    cloneStartRequest(request),
		},
	}
	manager.current = process
	if err := command.Start(); err != nil {
		endedAt := time.Now().UTC()
		process.run.State = StateFailed
		process.run.EndedAt = &endedAt
		process.run.Error = truncateError(err.Error())
		close(process.done)
		return cloneRun(process.run), fmt.Errorf("start worker process: %w", err)
	}
	process.run.PID = command.Process.Pid
	if request.Readiness.Kind == ReadinessNone {
		process.run.State = StateRunning
	}
	result := cloneRun(process.run)
	go manager.wait(process)
	if request.Readiness.Kind != ReadinessNone {
		manager.startReadiness(process)
	}
	return result, nil
}

func (manager *Manager) Stop(ctx context.Context, runID string) (Run, error) {
	manager.mu.Lock()
	process := manager.current
	if process == nil {
		manager.mu.Unlock()
		return Run{}, ErrNoRun
	}
	if process.run.RunID != runID {
		manager.mu.Unlock()
		return Run{}, ErrRunMismatch
	}
	if !isActiveState(process.run.State) {
		run := manager.runWithLogOffsetsLocked(process)
		manager.mu.Unlock()
		return run, nil
	}
	process.stopRequested = true
	process.run.State = StateStopping
	if process.readinessCancel != nil {
		process.readinessCancel()
	}
	pid := process.run.PID
	grace := time.Duration(process.run.Request.StopGraceSeconds) * time.Second
	done := process.done
	manager.mu.Unlock()

	if err := signalProcessGroup(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return Run{}, fmt.Errorf("terminate worker process group %d: %w", pid, err)
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return manager.currentRun(runID)
	case <-ctx.Done():
		return manager.killAndWait(runID, pid, done, ctx.Err())
	case <-timer.C:
	}
	if err := signalProcessGroup(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return Run{}, fmt.Errorf("kill worker process group %d: %w", pid, err)
	}
	select {
	case <-done:
		return manager.currentRun(runID)
	case <-ctx.Done():
		return manager.waitAfterKill(runID, done, ctx.Err())
	}
}

func (manager *Manager) Status() StatusResponse {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.current == nil {
		return StatusResponse{}
	}
	run := manager.runWithLogOffsetsLocked(manager.current)
	return StatusResponse{Run: &run}
}

func (manager *Manager) LogSnapshot(runID string) (LogSnapshot, error) {
	manager.mu.RLock()
	process := manager.current
	if process == nil {
		manager.mu.RUnlock()
		return LogSnapshot{}, ErrNoRun
	}
	if process.run.RunID != runID {
		manager.mu.RUnlock()
		return LogSnapshot{}, ErrRunMismatch
	}
	logBuffer := process.log
	manager.mu.RUnlock()
	return logBuffer.Snapshot(), nil
}

func (manager *Manager) SubscribeLog(runID string) (LogSnapshot, <-chan LogChunk, func(), error) {
	manager.mu.RLock()
	process := manager.current
	if process == nil {
		manager.mu.RUnlock()
		return LogSnapshot{}, nil, nil, ErrNoRun
	}
	if process.run.RunID != runID {
		manager.mu.RUnlock()
		return LogSnapshot{}, nil, nil, ErrRunMismatch
	}
	logBuffer := process.log
	manager.mu.RUnlock()
	snapshot, chunks, cancel := logBuffer.SubscribeWithSnapshot()
	return snapshot, chunks, cancel, nil
}

func (manager *Manager) Shutdown(ctx context.Context) error {
	manager.mu.RLock()
	if manager.current == nil || !isActiveState(manager.current.run.State) {
		manager.mu.RUnlock()
		return nil
	}
	runID := manager.current.run.RunID
	manager.mu.RUnlock()
	_, err := manager.Stop(ctx, runID)
	return err
}

func (manager *Manager) wait(process *managedProcess) {
	err := process.cmd.Wait()
	endedAt := time.Now().UTC()
	exitCode := process.cmd.ProcessState.ExitCode()

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if process.readinessCancel != nil {
		process.readinessCancel()
	}
	process.run.EndedAt = &endedAt
	process.run.ExitCode = &exitCode
	switch {
	case process.stopRequested:
		process.run.State = StateStopped
	case process.failureReason != "":
		process.run.State = StateFailed
		process.run.Error = truncateError(process.failureReason)
	case process.run.State == StateStarting:
		process.run.State = StateFailed
		process.run.Error = "process exited before readiness"
	case err != nil:
		process.run.State = StateFailed
		process.run.Error = truncateError(err.Error())
	default:
		process.run.State = StateStopped
	}
	close(process.done)
}

func (manager *Manager) startReadiness(process *managedProcess) {
	timeout := time.Duration(process.run.Request.Readiness.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	process.readinessCancel = cancel
	go func() {
		err := manager.waitForReadiness(ctx, process)
		cancel()
		if err == nil {
			manager.mu.Lock()
			if manager.current == process && process.run.State == StateStarting {
				process.run.State = StateRunning
			}
			manager.mu.Unlock()
			return
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		manager.failReadiness(process, err)
	}()
}

func (manager *Manager) waitForReadiness(ctx context.Context, process *managedProcess) error {
	readiness := process.run.Request.Readiness
	switch readiness.Kind {
	case ReadinessDelay:
		timer := time.NewTimer(time.Duration(readiness.DelaySeconds) * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-process.done:
			return fmt.Errorf("process exited before readiness")
		case <-ctx.Done():
			return ctx.Err()
		}
	case ReadinessLogRegex:
		pattern := regexp.MustCompile(readiness.Pattern)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			if pattern.Match(process.log.Snapshot().Data) {
				return nil
			}
			select {
			case <-ticker.C:
			case <-process.done:
				return fmt.Errorf("process exited before readiness")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	case ReadinessHTTP:
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, readiness.URL, nil)
			if err != nil {
				return err
			}
			response, err := manager.httpClient.Do(request)
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode >= 200 && response.StatusCode < 300 {
					return nil
				}
			}
			select {
			case <-ticker.C:
			case <-process.done:
				return fmt.Errorf("process exited before readiness")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	default:
		return nil
	}
}

func (manager *Manager) failReadiness(process *managedProcess, readinessErr error) {
	manager.mu.Lock()
	if manager.current != process || process.run.State != StateStarting {
		manager.mu.Unlock()
		return
	}
	process.failureReason = "readiness: " + readinessErr.Error()
	process.run.State = StateStopping
	pid := process.run.PID
	grace := time.Duration(process.run.Request.StopGraceSeconds) * time.Second
	done := process.done
	manager.mu.Unlock()

	_ = signalProcessGroup(pid, syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		_ = signalProcessGroup(pid, syscall.SIGKILL)
	case <-time.After(grace + time.Second):
		return
	}
}

func (manager *Manager) currentRun(runID string) (Run, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.current == nil {
		return Run{}, ErrNoRun
	}
	if manager.current.run.RunID != runID {
		return Run{}, ErrRunMismatch
	}
	return manager.runWithLogOffsetsLocked(manager.current), nil
}

func (manager *Manager) runWithLogOffsetsLocked(process *managedProcess) Run {
	run := cloneRun(process.run)
	snapshot := process.log.Snapshot()
	run.LogStartOffset = snapshot.StartOffset
	run.LogEndOffset = snapshot.EndOffset
	return run
}

func (manager *Manager) killAndWait(runID string, pid int, done <-chan struct{}, prior error) (Run, error) {
	killErr := signalProcessGroup(pid, syscall.SIGKILL)
	if errors.Is(killErr, syscall.ESRCH) {
		killErr = nil
	}
	run, waitErr := manager.waitAfterKill(runID, done, prior)
	return run, errors.Join(killErr, waitErr)
}

func (manager *Manager) waitAfterKill(runID string, done <-chan struct{}, prior error) (Run, error) {
	select {
	case <-done:
		run, err := manager.currentRun(runID)
		return run, errors.Join(prior, err)
	case <-time.After(time.Second):
		return Run{}, errors.Join(prior, fmt.Errorf("worker process did not exit after SIGKILL"))
	}
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(-pid, signal)
}

func isActiveState(state RunState) bool {
	return state == StateStarting || state == StateRunning || state == StateStopping
}

func mergedEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if found {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

func randomRunID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate worker run ID: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func truncateError(message string) string {
	if len(message) <= MaxErrorBytes {
		return message
	}
	return message[:MaxErrorBytes]
}
