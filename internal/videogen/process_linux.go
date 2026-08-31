//go:build linux

package videogen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	ErrCLIAttemptExists     = errors.New("video CLI attempt already exists")
	ErrCLIAttemptNotFound   = errors.New("video CLI attempt not found")
	ErrCLIAttemptNotRunning = errors.New("video CLI attempt is not running")
	ErrCLIExecutorShutdown  = errors.New("video CLI executor is shut down")
)

type CLIRunState string

const (
	CLIStatePreparing  CLIRunState = "preparing"
	CLIStateRunning    CLIRunState = "running"
	CLIStateStopping   CLIRunState = "stopping"
	CLIStateValidating CLIRunState = "validating"
	CLIStateSucceeded  CLIRunState = "succeeded"
	CLIStateFailed     CLIRunState = "failed"
	CLIStateStopped    CLIRunState = "stopped"
)

// CLIRunRequest contains already-expanded trusted shell commands and the one
// declared output that a caller permits the executor to consume.
type CLIRunRequest struct {
	AttemptID       string
	PrepareCommand  string
	Command         string
	WorkspaceRoot   string
	WorkDir         string
	Env             map[string]string
	Timeout         time.Duration
	StopGrace       time.Duration
	LogBufferBytes  int
	OutputDir       string
	OutputPath      string
	OutputMediaType string
	OutputExtension string
	MaxOutputBytes  int64
}

// CLIRunResult is both the synchronous result and the cloned Status snapshot.
type CLIRunResult struct {
	AttemptID  string
	State      CLIRunState
	PID        int
	StartedAt  time.Time
	EndedAt    *time.Time
	ExitCode   int
	OutputPath string
	OutputSize int64
	Error      string
	Request    CLIRunRequest
}

// CLIRunStatus is a cloned point-in-time view suitable for lifecycle polling.
type CLIRunStatus struct {
	AttemptID string
	PID       int
	Running   bool
	StartedAt time.Time
	State     CLIRunState
	Request   CLIRunRequest
}

type cliAttempt struct {
	log           *videoLogBuffer
	done          chan struct{}
	result        CLIRunResult
	cmd           *exec.Cmd
	stopRequested bool
	awaitingMain  bool
	logDraining   bool
	pgid          int
}

type commandOutcome struct {
	exitCode int
	err      error
}

type CLIExecutor struct {
	mu         sync.RWMutex
	attempts   map[string]*cliAttempt
	shutdown   bool
	beforeMain func()
}

func NewCLIExecutor() *CLIExecutor {
	return &CLIExecutor{attempts: make(map[string]*cliAttempt)}
}

// Run executes prepare and main commands synchronously. Callers that need the
// PID while it runs may invoke Run in a goroutine and poll Status.
func (executor *CLIExecutor) Run(ctx context.Context, request CLIRunRequest) (CLIRunResult, error) {
	if ctx == nil {
		return CLIRunResult{}, fmt.Errorf("video CLI run context is nil")
	}
	if err := ctx.Err(); err != nil {
		return CLIRunResult{}, err
	}
	if !validWorkspaceAttemptID(request.AttemptID) || strings.TrimSpace(request.Command) == "" {
		return CLIRunResult{}, fmt.Errorf("video CLI attempt ID and command are required")
	}
	if strings.TrimSpace(request.WorkspaceRoot) == "" {
		return CLIRunResult{}, fmt.Errorf("video CLI workspace root is required")
	}
	if strings.TrimSpace(request.WorkDir) == "" {
		request.WorkDir = request.WorkspaceRoot
	}
	if err := validateCLIEnvironment(request.Env); err != nil {
		return CLIRunResult{}, err
	}
	if !filepath.IsAbs(request.WorkDir) || request.Timeout <= 0 || request.StopGrace < 0 || request.LogBufferBytes < 1 || request.MaxOutputBytes < 1 || request.MaxOutputBytes == maxCLIOutputBytes {
		return CLIRunResult{}, fmt.Errorf("video CLI execution limits are invalid")
	}

	state := CLIStateRunning
	if request.PrepareCommand != "" {
		state = CLIStatePreparing
	}
	attempt := &cliAttempt{
		log:  newVideoLogBuffer(request.LogBufferBytes),
		done: make(chan struct{}),
		result: CLIRunResult{
			AttemptID: request.AttemptID,
			State:     state,
			StartedAt: time.Now().UTC(),
			ExitCode:  -1,
			Request:   cloneCLIRunRequest(request),
		},
	}
	executor.mu.Lock()
	if executor.shutdown {
		executor.mu.Unlock()
		return CLIRunResult{}, ErrCLIExecutorShutdown
	}
	if _, exists := executor.attempts[request.AttemptID]; exists {
		executor.mu.Unlock()
		return CLIRunResult{}, ErrCLIAttemptExists
	}
	executor.attempts[request.AttemptID] = attempt
	executor.mu.Unlock()

	runContext, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	if request.PrepareCommand != "" {
		_, err := executor.runCommand(runContext, attempt, request, request.PrepareCommand, CLIStatePreparing)
		if executor.wasStopped(attempt) {
			return executor.finish(attempt, CLIStateStopped, nil, "", 0)
		}
		if err != nil {
			return executor.finish(attempt, CLIStateFailed, err, "", 0)
		}
	}
	if err := runContext.Err(); err != nil {
		return executor.finish(attempt, CLIStateFailed, err, "", 0)
	}
	if executor.wasStopped(attempt) {
		return executor.finish(attempt, CLIStateStopped, nil, "", 0)
	}
	if executor.beforeMain != nil {
		executor.beforeMain()
	}
	_, err := executor.runCommand(runContext, attempt, request, request.Command, CLIStateRunning)
	if executor.wasStopped(attempt) {
		return executor.finish(attempt, CLIStateStopped, nil, "", 0)
	}
	if err != nil {
		return executor.finish(attempt, CLIStateFailed, err, "", 0)
	}
	outputSize, err := validateCLIOutput(request)
	if err != nil {
		return executor.finish(attempt, CLIStateFailed, err, "", 0)
	}
	return executor.finish(attempt, CLIStateSucceeded, nil, request.OutputPath, outputSize)
}

func (executor *CLIExecutor) runCommand(ctx context.Context, attempt *cliAttempt, request CLIRunRequest, commandText string, state CLIRunState) (int, error) {
	command := exec.CommandContext(context.Background(), "/bin/bash", "-lc", commandText)
	command.Dir = request.WorkDir
	command.Env = append(os.Environ(), sortedEnvironment(request.Env)...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	logRead, logWrite, err := os.Pipe()
	if err != nil {
		return -1, fmt.Errorf("create video CLI log pipe: %w", err)
	}
	defer logRead.Close()
	command.Stdout = logWrite
	command.Stderr = logWrite

	// Serialize the stop-request check with Start and PID publication. Stop
	// either prevents this group from being created or observes its real PID;
	// it can never signal PID 0 and then miss a subsequently launched group.
	executor.mu.Lock()
	if attempt.stopRequested {
		executor.mu.Unlock()
		return -1, nil
	}
	if err := command.Start(); err != nil {
		_ = logWrite.Close()
		executor.mu.Unlock()
		return -1, fmt.Errorf("start video CLI %s command: %w", state, err)
	}
	if err := logWrite.Close(); err != nil {
		executor.mu.Unlock()
		return -1, fmt.Errorf("close parent video CLI log pipe: %w", err)
	}
	attempt.cmd = command
	attempt.awaitingMain = false
	attempt.logDraining = true
	attempt.pgid = command.Process.Pid
	attempt.result.State = state
	attempt.result.PID = command.Process.Pid
	executor.mu.Unlock()
	logDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(attempt.log, logRead)
		close(logDone)
	}()

	waited := make(chan commandOutcome, 1)
	go func() {
		observeErr := waitCLIExitWithoutReaping(command.Process.Pid)
		executor.mu.Lock()
		waitErr := command.Wait()
		exitCode := -1
		if command.ProcessState != nil {
			exitCode = command.ProcessState.ExitCode()
		}
		executor.publishCommandCompletionLocked(attempt, exitCode)
		executor.mu.Unlock()
		<-logDone
		executor.mu.Lock()
		attempt.logDraining = false
		executor.mu.Unlock()
		waited <- commandOutcome{exitCode: exitCode, err: errors.Join(observeErr, waitErr)}
	}()
	select {
	case outcome := <-waited:
		return outcome.exitCode, commandExitError(state, outcome.err)
	case <-ctx.Done():
		executor.mu.Lock()
		if attempt.result.State != CLIStateStopping {
			attempt.result.State = CLIStateStopping
		}
		executor.mu.Unlock()
		if terminateErr := terminateCLIGroup(command.Process.Pid, request.StopGrace, waited); terminateErr != nil {
			return -1, errors.Join(ctx.Err(), terminateErr)
		}
		return command.ProcessState.ExitCode(), ctx.Err()
	}
}

func commandExitError(state CLIRunState, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("video CLI %s command: %w", state, err)
}

func (executor *CLIExecutor) Stop(ctx context.Context, attemptID string) error {
	if ctx == nil {
		return fmt.Errorf("video CLI stop context is nil")
	}
	executor.mu.Lock()
	attempt, exists := executor.attempts[attemptID]
	if !exists || (!activeCLIState(attempt.result.State) && !attempt.awaitingMain && !attempt.logDraining) {
		executor.mu.Unlock()
		return ErrCLIAttemptNotRunning
	}
	attempt.stopRequested = true
	attempt.result.State = CLIStateStopping
	pid := attempt.result.PID
	if pid == 0 {
		pid = attempt.pgid
	}
	grace := attempt.result.Request.StopGrace
	done := attempt.done
	executor.mu.Unlock()

	stopErr := stopCLIProcessGroup(ctx, pid, grace)
	if stopErr == nil {
		<-done
	}
	return stopErr
}

func (executor *CLIExecutor) Status(attemptID string) (CLIRunStatus, error) {
	executor.mu.RLock()
	defer executor.mu.RUnlock()
	attempt, exists := executor.attempts[attemptID]
	if !exists {
		return CLIRunStatus{}, ErrCLIAttemptNotFound
	}
	return CLIRunStatus{
		AttemptID: attempt.result.AttemptID,
		PID:       attempt.result.PID,
		Running:   activeCLIState(attempt.result.State) || attempt.awaitingMain || attempt.logDraining,
		StartedAt: attempt.result.StartedAt,
		State:     attempt.result.State,
		Request:   cloneCLIRunRequest(attempt.result.Request),
	}, nil
}

func (executor *CLIExecutor) SnapshotLog(attemptID string) (VideoLogSnapshot, error) {
	log, err := executor.attemptLog(attemptID)
	if err != nil {
		return VideoLogSnapshot{}, err
	}
	return log.snapshot(), nil
}

func (executor *CLIExecutor) SubscribeLog(attemptID string) (VideoLogSnapshot, <-chan VideoLogChunk, func(), error) {
	log, err := executor.attemptLog(attemptID)
	if err != nil {
		return VideoLogSnapshot{}, nil, nil, err
	}
	snapshot, chunks, cancel := log.subscribeWithSnapshot()
	return snapshot, chunks, cancel, nil
}

func (executor *CLIExecutor) SaveLog(attemptID, destination string) (string, error) {
	log, err := executor.attemptLog(attemptID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(destination) == "" {
		return "", fmt.Errorf("video CLI log destination is required")
	}
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("video CLI log destination must not be a symlink")
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return "", fmt.Errorf("video CLI log destination must be a regular file path")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect video CLI log destination: %w", err)
	}
	if err := writeVideoLog(destination, log.snapshot().Data); err != nil {
		return "", err
	}
	return destination, nil
}

func (executor *CLIExecutor) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("video CLI shutdown context is nil")
	}
	executor.mu.Lock()
	executor.shutdown = true
	active := make([]string, 0, len(executor.attempts))
	for attemptID, attempt := range executor.attempts {
		if activeCLIState(attempt.result.State) || attempt.awaitingMain || attempt.logDraining {
			active = append(active, attemptID)
		}
	}
	executor.mu.Unlock()

	var wait sync.WaitGroup
	errorsByAttempt := make(chan error, len(active))
	for _, attemptID := range active {
		wait.Add(1)
		go func(id string) {
			defer wait.Done()
			if err := executor.Stop(ctx, id); err != nil && !errors.Is(err, ErrCLIAttemptNotRunning) {
				errorsByAttempt <- err
			}
		}(attemptID)
	}
	wait.Wait()
	close(errorsByAttempt)
	var joined error
	for err := range errorsByAttempt {
		joined = errors.Join(joined, err)
	}
	return joined
}

func (executor *CLIExecutor) finish(attempt *cliAttempt, state CLIRunState, runErr error, outputPath string, outputSize int64) (CLIRunResult, error) {
	endedAt := time.Now().UTC()
	executor.mu.Lock()
	attempt.cmd = nil
	attempt.result.State = state
	attempt.result.EndedAt = &endedAt
	attempt.result.OutputPath = outputPath
	attempt.result.OutputSize = outputSize
	if runErr != nil {
		attempt.result.Error = runErr.Error()
	}
	result := cloneCLIRunResult(attempt.result)
	close(attempt.done)
	executor.mu.Unlock()
	return result, runErr
}

func (executor *CLIExecutor) publishCommandCompletionLocked(attempt *cliAttempt, exitCode int) {
	attempt.cmd = nil
	attempt.result.PID = 0
	attempt.result.ExitCode = exitCode
	attempt.awaitingMain = attempt.result.State == CLIStatePreparing
	if !attempt.stopRequested {
		attempt.result.State = CLIStateValidating
	}
}

func (executor *CLIExecutor) wasStopped(attempt *cliAttempt) bool {
	executor.mu.RLock()
	defer executor.mu.RUnlock()
	return attempt.stopRequested
}

func (executor *CLIExecutor) attemptLog(attemptID string) (*videoLogBuffer, error) {
	executor.mu.RLock()
	defer executor.mu.RUnlock()
	attempt, exists := executor.attempts[attemptID]
	if !exists {
		return nil, ErrCLIAttemptNotFound
	}
	return attempt.log, nil
}

func activeCLIState(state CLIRunState) bool {
	return state == CLIStatePreparing || state == CLIStateRunning || state == CLIStateStopping
}

const maxCLIOutputBytes = int64(^uint64(0) >> 1)

func terminateCLIGroup(pid int, grace time.Duration, waited <-chan commandOutcome) error {
	termErr := signalCLIProcessGroup(pid, syscall.SIGTERM)
	if errors.Is(termErr, syscall.ESRCH) {
		termErr = nil
	}
	killErr := waitForCLIGroupExit(context.Background(), pid, grace)
	if killErr != nil {
		return errors.Join(termErr, killErr)
	}
	<-waited
	return errors.Join(termErr, killErr)
}

func waitCLIExitWithoutReaping(pid int) error {
	if pid <= 0 {
		return nil
	}
	var info [128]byte
	_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, 1, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func stopCLIProcessGroup(ctx context.Context, pid int, grace time.Duration) error {
	termErr := signalCLIProcessGroup(pid, syscall.SIGTERM)
	if errors.Is(termErr, syscall.ESRCH) {
		termErr = nil
	}
	return errors.Join(termErr, waitForCLIGroupExit(ctx, pid, grace))
}

// waitForCLIGroupExit always confirms group disappearance. A zero grace
// means KILL immediately; otherwise it grants TERM the requested interval.
func waitForCLIGroupExit(ctx context.Context, pid int, grace time.Duration) error {
	if pid <= 0 {
		if ctx != nil {
			return ctx.Err()
		}
		return nil
	}
	deadline := time.Now().Add(grace)
	var killDeadline time.Time
	killed := grace == 0
	var contextErr error
	if killed {
		killDeadline = time.Now().Add(time.Second)
		if err := signalCLIProcessGroup(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !killed && ctx != nil && ctx.Err() != nil {
			contextErr = ctx.Err()
			killed = true
			killDeadline = time.Now().Add(time.Second)
			if err := signalCLIProcessGroup(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return errors.Join(contextErr, err)
			}
		}
		err := signalCLIProcessGroup(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return contextErr
		}
		if err != nil {
			return err
		}
		if !killed && !time.Now().Before(deadline) {
			killed = true
			killDeadline = time.Now().Add(time.Second)
			if err := signalCLIProcessGroup(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
		}
		if killed && !time.Now().Before(killDeadline) {
			return errors.Join(contextErr, fmt.Errorf("video CLI process group %d remained after SIGKILL", pid))
		}
		<-ticker.C
	}
}

func signalCLIProcessGroup(pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(-pid, signal)
}

func sortedEnvironment(environment map[string]string) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, key+"="+environment[key])
	}
	return entries
}

func validateCLIEnvironment(environment map[string]string) error {
	for key, value := range environment {
		if key == "" || strings.Contains(key, "=") || strings.IndexByte(key, 0) >= 0 || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("video CLI environment contains an invalid entry")
		}
	}
	return nil
}

func cloneCLIRunRequest(request CLIRunRequest) CLIRunRequest {
	clone := request
	if request.Env != nil {
		clone.Env = make(map[string]string, len(request.Env))
		for key, value := range request.Env {
			clone.Env[key] = value
		}
	}
	return clone
}

func cloneCLIRunResult(result CLIRunResult) CLIRunResult {
	clone := result
	clone.Request = cloneCLIRunRequest(result.Request)
	if result.EndedAt != nil {
		endedAt := *result.EndedAt
		clone.EndedAt = &endedAt
	}
	return clone
}

func validateCLIOutput(request CLIRunRequest) (int64, error) {
	outputDir := filepath.Clean(request.OutputDir)
	outputPath := filepath.Clean(request.OutputPath)
	relative, err := filepath.Rel(outputDir, outputPath)
	if request.OutputDir == "" || request.OutputPath == "" || err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return 0, fmt.Errorf("video CLI output path must remain inside outputs")
	}
	resolvedWorkspaceRoot, err := filepath.EvalSymlinks(filepath.Clean(request.WorkspaceRoot))
	if err != nil {
		return 0, fmt.Errorf("resolve video CLI workspace root: %w", err)
	}
	resolvedOutputDir, err := filepath.EvalSymlinks(outputDir)
	if err != nil {
		return 0, fmt.Errorf("resolve video CLI output directory: %w", err)
	}
	resolvedExpectedOutputDir, err := filepath.EvalSymlinks(filepath.Join(request.WorkspaceRoot, "outputs"))
	if err != nil {
		return 0, fmt.Errorf("resolve expected video CLI output directory: %w", err)
	}
	if resolvedOutputDir != resolvedExpectedOutputDir || !pathWithin(resolvedWorkspaceRoot, resolvedOutputDir, false) {
		return 0, fmt.Errorf("video CLI output directory must be the workspace outputs directory")
	}
	resolvedOutputParent, err := filepath.EvalSymlinks(filepath.Dir(outputPath))
	if err != nil {
		return 0, fmt.Errorf("resolve video CLI output parent: %w", err)
	}
	if !pathWithin(resolvedOutputDir, resolvedOutputParent, true) {
		return 0, fmt.Errorf("video CLI output path must remain inside resolved outputs")
	}
	if !strings.EqualFold(filepath.Ext(outputPath), request.OutputExtension) {
		return 0, fmt.Errorf("video CLI output extension does not match its declaration")
	}
	mediaType, _, err := mime.ParseMediaType(request.OutputMediaType)
	if err != nil {
		return 0, fmt.Errorf("parse video CLI output media type: %w", err)
	}
	wantExtension, magic := cliOutputFormat(strings.ToLower(mediaType))
	if wantExtension == "" || !strings.EqualFold(request.OutputExtension, wantExtension) {
		return 0, fmt.Errorf("video CLI output MIME and extension do not match")
	}

	// Resolve intentional internal links once, then open that resolved path
	// relative to an already-open workspace fd. Every component is opened
	// O_NOFOLLOW, so a concurrent rename/symlink swap cannot redirect bytes
	// outside the workspace between containment validation and reading.
	resolvedOutputPath := filepath.Join(resolvedOutputParent, filepath.Base(outputPath))
	relativeOutputPath, err := filepath.Rel(resolvedWorkspaceRoot, resolvedOutputPath)
	if err != nil || filepath.IsAbs(relativeOutputPath) || relativeOutputPath == "." || strings.HasPrefix(relativeOutputPath, ".."+string(filepath.Separator)) || relativeOutputPath == ".." {
		return 0, fmt.Errorf("resolve video CLI output path within workspace")
	}
	file, err := openCLIOutputNoFollow(resolvedWorkspaceRoot, relativeOutputPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 0, fmt.Errorf("video CLI output must be a regular non-symlink file")
	}
	limited := &io.LimitedReader{R: file, N: request.MaxOutputBytes + 1}
	header := make([]byte, 12)
	read, readErr := io.ReadFull(limited, header)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return 0, fmt.Errorf("read video CLI output: %w", readErr)
	}
	rest, err := io.Copy(io.Discard, limited)
	if err != nil {
		return 0, fmt.Errorf("read video CLI output: %w", err)
	}
	total := int64(read) + rest
	if total == 0 {
		return 0, fmt.Errorf("video CLI output is empty")
	}
	if total > request.MaxOutputBytes {
		return 0, fmt.Errorf("video CLI output exceeds %d bytes", request.MaxOutputBytes)
	}
	if !magic(header[:read]) {
		return 0, fmt.Errorf("video CLI output magic does not match %q", mediaType)
	}
	return total, nil
}

func openCLIOutputNoFollow(workspaceRoot, relativePath string) (*os.File, error) {
	rootFD, err := openAbsoluteDirectoryNoFollow(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("open video CLI workspace safely: %w", err)
	}
	fd := rootFD
	closeFD := true
	defer func() {
		if closeFD {
			_ = syscall.Close(fd)
		}
	}()
	parts := strings.Split(relativePath, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("invalid video CLI output path component")
		}
		next, err := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return nil, fmt.Errorf("open video CLI output directory safely: %w", err)
		}
		_ = syscall.Close(fd)
		fd = next
	}
	name := parts[len(parts)-1]
	if name == "" || name == "." || name == ".." {
		return nil, fmt.Errorf("invalid video CLI output file name")
	}
	fileFD, err := syscall.Openat(fd, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open video CLI output safely: %w", err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fileFD, &stat); err != nil {
		_ = syscall.Close(fileFD)
		return nil, fmt.Errorf("stat video CLI output safely: %w", err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		_ = syscall.Close(fileFD)
		return nil, fmt.Errorf("video CLI output must be a regular non-symlink file")
	}
	closeFD = false
	_ = syscall.Close(fd)
	return os.NewFile(uintptr(fileFD), "video-cli-output"), nil
}

// openAbsoluteDirectoryNoFollow anchors an absolute path at an open fd for
// `/`, refusing symlinks in every component rather than trusting a mutable
// absolute pathname between containment checks and descriptor acquisition.
func openAbsoluteDirectoryNoFollow(path string) (int, error) {
	if !filepath.IsAbs(path) {
		return -1, fmt.Errorf("directory path is not absolute")
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			_ = syscall.Close(fd)
			return -1, fmt.Errorf("directory path escapes root")
		}
		next, err := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			_ = syscall.Close(fd)
			return -1, err
		}
		_ = syscall.Close(fd)
		fd = next
	}
	return fd, nil
}

func pathWithin(root, candidate string, allowEqual bool) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return allowEqual || relative != "."
}

func cliOutputFormat(mediaType string) (string, func([]byte) bool) {
	switch mediaType {
	case "video/webm":
		return ".webm", func(header []byte) bool {
			return len(header) >= 4 && header[0] == 0x1a && header[1] == 0x45 && header[2] == 0xdf && header[3] == 0xa3
		}
	case "video/x-msvideo", "video/avi":
		return ".avi", func(header []byte) bool {
			return len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "AVI "
		}
	case "image/webp":
		return ".webp", func(header []byte) bool {
			return len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP"
		}
	case "video/mp4":
		return ".mp4", isoBMFFMagic
	case "video/quicktime":
		return ".mov", isoBMFFMagic
	default:
		return "", func([]byte) bool { return false }
	}
}

func isoBMFFMagic(header []byte) bool {
	return len(header) >= 12 && string(header[4:8]) == "ftyp"
}

func writeVideoLog(destination string, contents []byte) (err error) {
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, ".video-log-*")
	if err != nil {
		return fmt.Errorf("create temporary video CLI log: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect video CLI log: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write video CLI log: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync video CLI log: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close video CLI log: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("replace video CLI log: %w", err)
	}
	return nil
}
