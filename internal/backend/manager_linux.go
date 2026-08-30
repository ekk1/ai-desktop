package backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"syscall"
	"time"
)

type State string

const (
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
)

var (
	ErrRunning    = errors.New("backend profile is already running")
	ErrNotRunning = errors.New("backend profile has no run")
)

type RunInfo struct {
	RunID           string     `json:"run_id"`
	ProfileID       string     `json:"profile_id"`
	ProfileName     string     `json:"profile_name"`
	State           State      `json:"state"`
	PID             int        `json:"pid"`
	StartedAt       time.Time  `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	Error           string     `json:"error,omitempty"`
	ProfileSnapshot Profile    `json:"profile_snapshot"`
}

type managedProcess struct {
	cmd             *exec.Cmd
	log             *LogBuffer
	done            chan struct{}
	info            RunInfo
	stopRequested   bool
	failureReason   string
	readinessCancel context.CancelFunc
}

type Manager struct {
	mu          sync.RWMutex
	repository  *Repository
	crashLogDir string
	runs        map[string]*managedProcess
	httpClient  *http.Client
}

func NewManager(repository *Repository, crashLogDir string) *Manager {
	return &Manager{
		repository:  repository,
		crashLogDir: crashLogDir,
		runs:        make(map[string]*managedProcess),
		httpClient:  &http.Client{Timeout: time.Second},
	}
}

func (manager *Manager) Start(profileID string, overrides map[string]string) (RunInfo, error) {
	profile, ok := manager.repository.Get(profileID)
	if !ok {
		return RunInfo{}, ErrNotFound
	}
	values := cloneStringMap(profile.Variables)
	if values == nil {
		values = make(map[string]string)
	}
	for key, value := range overrides {
		values[key] = value
	}
	commandText, err := ExpandCommand(profile.Command, values)
	if err != nil {
		return RunInfo{}, err
	}

	manager.mu.Lock()
	if current, exists := manager.runs[profileID]; exists && isActive(current.info.State) {
		manager.mu.Unlock()
		return RunInfo{}, ErrRunning
	}
	runID, err := randomID()
	if err != nil {
		manager.mu.Unlock()
		return RunInfo{}, err
	}
	command := exec.Command("/bin/bash", "-lc", commandText)
	command.Dir = profile.WorkDir
	command.Env = append([]string(nil), os.Environ()...)
	for key, value := range profile.Env {
		command.Env = append(command.Env, key+"="+value)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logBuffer := NewLogBuffer(profile.LogBufferBytes)
	command.Stdout = logBuffer
	command.Stderr = logBuffer
	if err := command.Start(); err != nil {
		manager.mu.Unlock()
		return RunInfo{}, fmt.Errorf("start backend profile %q: %w", profile.Name, err)
	}
	state := StateStarting
	if profile.Readiness.Kind == ReadinessNone {
		state = StateRunning
	}
	process := &managedProcess{
		cmd:  command,
		log:  logBuffer,
		done: make(chan struct{}),
		info: RunInfo{
			RunID:           runID,
			ProfileID:       profile.ID,
			ProfileName:     profile.Name,
			State:           state,
			PID:             command.Process.Pid,
			StartedAt:       time.Now().UTC(),
			ProfileSnapshot: cloneProfile(profile),
		},
	}
	manager.runs[profileID] = process
	initialRun := cloneRunInfo(process.info)
	manager.mu.Unlock()

	go manager.wait(process)
	if state == StateStarting {
		manager.startReadiness(process)
	}
	return initialRun, nil
}

func (manager *Manager) Stop(ctx context.Context, profileID string) error {
	manager.mu.Lock()
	process, exists := manager.runs[profileID]
	if !exists {
		manager.mu.Unlock()
		return ErrNotRunning
	}
	if !isActive(process.info.State) {
		manager.mu.Unlock()
		return nil
	}
	process.stopRequested = true
	process.info.State = StateStopping
	if process.readinessCancel != nil {
		process.readinessCancel()
	}
	pid := process.info.PID
	grace := time.Duration(process.info.ProfileSnapshot.StopGraceSeconds) * time.Second
	done := process.done
	manager.mu.Unlock()

	if err := signalProcessGroup(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("terminate backend process group %d: %w", pid, err)
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return manager.killAfterContext(pid, done, ctx.Err())
	case <-timer.C:
	}
	if err := signalProcessGroup(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill backend process group %d: %w", pid, err)
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return manager.waitAfterKill(done, ctx.Err())
	}
}

func (manager *Manager) killAfterContext(pid int, done <-chan struct{}, contextErr error) error {
	killErr := signalProcessGroup(pid, syscall.SIGKILL)
	if errors.Is(killErr, syscall.ESRCH) {
		killErr = nil
	}
	return errors.Join(contextErr, killErr, manager.waitAfterKill(done, nil))
}

func (manager *Manager) waitAfterKill(done <-chan struct{}, prior error) error {
	select {
	case <-done:
		return prior
	case <-time.After(time.Second):
		return errors.Join(prior, fmt.Errorf("backend process did not exit after SIGKILL"))
	}
}

func (manager *Manager) Shutdown(ctx context.Context) error {
	manager.mu.RLock()
	ids := make([]string, 0, len(manager.runs))
	for id, process := range manager.runs {
		if isActive(process.info.State) {
			ids = append(ids, id)
		}
	}
	manager.mu.RUnlock()

	errorsByProfile := make([]error, 0)
	for _, id := range ids {
		if err := manager.Stop(ctx, id); err != nil && !errors.Is(err, ErrNotRunning) {
			errorsByProfile = append(errorsByProfile, fmt.Errorf("stop backend %s: %w", id, err))
		}
	}
	return errors.Join(errorsByProfile...)
}

func (manager *Manager) Runs() []RunInfo {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	runs := make([]RunInfo, 0, len(manager.runs))
	for _, process := range manager.runs {
		runs = append(runs, cloneRunInfo(process.info))
	}
	sort.Slice(runs, func(left, right int) bool { return runs[left].ProfileName < runs[right].ProfileName })
	return runs
}

func (manager *Manager) Run(profileID string) (RunInfo, bool) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	process, ok := manager.runs[profileID]
	if !ok {
		return RunInfo{}, false
	}
	return cloneRunInfo(process.info), true
}

func (manager *Manager) LogSnapshot(profileID string) ([]byte, error) {
	process, err := manager.process(profileID)
	if err != nil {
		return nil, err
	}
	return process.log.Snapshot(), nil
}

func (manager *Manager) SubscribeLog(profileID string) ([]byte, <-chan []byte, func(), error) {
	process, err := manager.process(profileID)
	if err != nil {
		return nil, nil, nil, err
	}
	chunks, cancel := process.log.Subscribe()
	return process.log.Snapshot(), chunks, cancel, nil
}

func (manager *Manager) ClearLog(profileID string) error {
	process, err := manager.process(profileID)
	if err != nil {
		return err
	}
	process.log.Clear()
	return nil
}

func (manager *Manager) SaveLog(profileID string) (string, error) {
	process, err := manager.process(profileID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(manager.crashLogDir, "manual-"+process.info.RunID+".log")
	if err := writeLogFile(path, process.log.Snapshot()); err != nil {
		return "", err
	}
	return path, nil
}

func (manager *Manager) process(profileID string) (*managedProcess, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	process, ok := manager.runs[profileID]
	if !ok {
		return nil, ErrNotRunning
	}
	return process, nil
}

func (manager *Manager) wait(process *managedProcess) {
	err := process.cmd.Wait()
	ended := time.Now().UTC()
	exitCode := process.cmd.ProcessState.ExitCode()

	manager.mu.RLock()
	unexpectedFailure := !process.stopRequested && (err != nil || process.failureReason != "")
	failureReason := process.failureReason
	manager.mu.RUnlock()
	if unexpectedFailure {
		_ = writeLogFile(filepath.Join(manager.crashLogDir, "crash-"+process.info.RunID+".log"), process.log.Snapshot())
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if process.readinessCancel != nil {
		process.readinessCancel()
	}
	process.info.EndedAt = &ended
	process.info.ExitCode = &exitCode
	switch {
	case process.stopRequested:
		process.info.State = StateStopped
	case failureReason != "":
		process.info.State = StateFailed
		process.info.Error = failureReason
	case err != nil:
		process.info.State = StateFailed
		process.info.Error = err.Error()
	default:
		process.info.State = StateStopped
	}
	close(process.done)
}

func (manager *Manager) startReadiness(process *managedProcess) {
	readiness := process.info.ProfileSnapshot.Readiness
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(readiness.TimeoutSeconds)*time.Second)
	manager.mu.Lock()
	process.readinessCancel = cancel
	manager.mu.Unlock()

	go func() {
		err := manager.waitForReadiness(ctx, process, readiness)
		cancel()
		if err == nil {
			manager.mu.Lock()
			if process.info.State == StateStarting {
				process.info.State = StateRunning
			}
			manager.mu.Unlock()
			return
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		manager.mu.Lock()
		if process.info.State != StateStarting {
			manager.mu.Unlock()
			return
		}
		process.failureReason = "readiness: " + err.Error()
		process.info.State = StateStopping
		pid := process.info.PID
		grace := time.Duration(process.info.ProfileSnapshot.StopGraceSeconds) * time.Second
		manager.mu.Unlock()
		_ = signalProcessGroup(pid, syscall.SIGTERM)
		select {
		case <-process.done:
		case <-time.After(grace):
			_ = signalProcessGroup(pid, syscall.SIGKILL)
		}
	}()
}

func (manager *Manager) waitForReadiness(ctx context.Context, process *managedProcess, readiness Readiness) error {
	switch readiness.Kind {
	case ReadinessDelay:
		select {
		case <-time.After(time.Duration(readiness.DelaySeconds) * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
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
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	case ReadinessLogRegex:
		pattern, err := regexp.Compile(readiness.Pattern)
		if err != nil {
			return err
		}
		if pattern.Match(process.log.Snapshot()) {
			return nil
		}
		chunks, cancel := process.log.Subscribe()
		defer cancel()
		for {
			select {
			case _, ok := <-chunks:
				if !ok {
					return context.Canceled
				}
				if pattern.Match(process.log.Snapshot()) {
					return nil
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	default:
		return nil
	}
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	return syscall.Kill(-pid, signal)
}

func writeLogFile(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return fmt.Errorf("write log %q: %w", path, err)
	}
	return nil
}

func isActive(state State) bool {
	return state == StateStarting || state == StateRunning || state == StateStopping
}

func cloneRunInfo(run RunInfo) RunInfo {
	clone := run
	clone.ProfileSnapshot = cloneProfile(run.ProfileSnapshot)
	if run.EndedAt != nil {
		ended := *run.EndedAt
		clone.EndedAt = &ended
	}
	if run.ExitCode != nil {
		exitCode := *run.ExitCode
		clone.ExitCode = &exitCode
	}
	return clone
}
