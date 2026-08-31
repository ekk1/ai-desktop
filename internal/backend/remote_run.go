package backend

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/ekk1/ai-desktop/internal/worker"
)

const (
	remoteStartTimeout  = 10 * time.Second
	remoteStatusTimeout = 2 * time.Second
	remotePollInterval  = 200 * time.Millisecond
	remoteRetryInterval = 250 * time.Millisecond
	remoteStopTimeout   = 10 * time.Second
)

func (manager *Manager) startRemote(ctx context.Context, profile Profile, commandText string) (RunInfo, error) {
	manager.mu.Lock()
	if current, exists := manager.runs[profile.ID]; exists && isActive(current.info.State) {
		manager.mu.Unlock()
		return RunInfo{}, ErrRunning
	}
	localRunID, err := randomID()
	if err != nil {
		manager.mu.Unlock()
		return RunInfo{}, err
	}
	client := manager.workerFactory(profile.Execution.WorkerBaseURL)
	process := &managedProcess{
		log:             NewLogBuffer(profile.LogBufferBytes),
		done:            make(chan struct{}),
		remote:          client,
		remoteStartDone: make(chan struct{}),
		info: RunInfo{
			RunID:           localRunID,
			ProfileID:       profile.ID,
			ProfileName:     profile.Name,
			State:           StateStarting,
			StartedAt:       time.Now().UTC(),
			ProfileSnapshot: cloneProfile(profile),
			ExecutionKind:   ExecutionWorker,
			ConnectionState: ConnectionUnknown,
		},
	}
	manager.runs[profile.ID] = process
	manager.mu.Unlock()

	startRequest := worker.StartRequest{
		Command:          commandText,
		WorkDir:          profile.WorkDir,
		Env:              cloneStringMap(profile.Env),
		StopGraceSeconds: profile.StopGraceSeconds,
		LogBufferBytes:   profile.LogBufferBytes,
		Readiness: worker.Readiness{
			Kind:           profile.Readiness.Kind,
			DelaySeconds:   profile.Readiness.DelaySeconds,
			URL:            profile.Readiness.URL,
			Pattern:        profile.Readiness.Pattern,
			TimeoutSeconds: profile.Readiness.TimeoutSeconds,
		},
	}
	startContext, cancel := context.WithTimeout(ctx, remoteStartTimeout)
	remoteRun, err := client.Start(startContext, startRequest)
	cancel()
	if err != nil {
		var clientErr *worker.ClientError
		if !errors.As(err, &clientErr) {
			if recovered, ok := recoverRemoteStart(client, startRequest); ok {
				remoteRun = recovered
				err = nil
			}
		}
		if err != nil {
			manager.failRemoteStart(process, err)
			return RunInfo{}, fmt.Errorf("start remote backend profile %q: %w", profile.Name, err)
		}
	}
	if remoteRun.RunID == "" || remoteRun.InstanceID == "" {
		err := fmt.Errorf("worker returned an incomplete run identity")
		manager.failRemoteStart(process, err)
		return RunInfo{}, err
	}

	remoteContext, remoteCancel := context.WithCancel(manager.ctx)
	manager.mu.Lock()
	process.remoteCancel = remoteCancel
	process.info.WorkerInstanceID = remoteRun.InstanceID
	process.info.WorkerRunID = remoteRun.RunID
	process.info.ConnectionState = ConnectionConnected
	process.info.ConnectionError = ""
	manager.applyRemoteRunLocked(process, remoteRun)
	initial := cloneRunInfo(process.info)
	stopPending := process.stopRequested
	manager.closeRemoteStartLocked(process)
	manager.mu.Unlock()
	if stopPending {
		stopContext, stopCancel := context.WithTimeout(context.Background(), remoteStopTimeout)
		defer stopCancel()
		if err := manager.stopRemote(stopContext, process); err != nil {
			return RunInfo{}, err
		}
		initial = manager.remoteRunInfo(process)
	}
	if isActive(initial.State) {
		go manager.watchRemoteRun(remoteContext, process)
		go manager.mirrorRemoteLogs(remoteContext, process)
	}
	return initial, nil
}

func (manager *Manager) failRemoteStart(process *managedProcess, startErr error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !isActive(process.info.State) {
		return
	}
	endedAt := time.Now().UTC()
	process.info.State = StateFailed
	process.info.EndedAt = &endedAt
	process.info.Error = truncateBackendError(startErr.Error())
	process.info.ConnectionState = ConnectionUnknown
	process.info.ConnectionError = truncateBackendError(startErr.Error())
	manager.closeRemoteStartLocked(process)
	manager.closeRemoteDoneLocked(process)
}

func (manager *Manager) watchRemoteRun(ctx context.Context, process *managedProcess) {
	ticker := time.NewTicker(remotePollInterval)
	defer ticker.Stop()
	for {
		if !manager.refreshRemoteStatus(ctx, process) {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		case <-process.done:
			return
		}
	}
}

func (manager *Manager) refreshRemoteStatus(ctx context.Context, process *managedProcess) bool {
	requestContext, cancel := context.WithTimeout(ctx, remoteStatusTimeout)
	status, err := process.remote.Status(requestContext)
	cancel()
	if err != nil {
		manager.markRemoteConnectionError(process, err)
		return true
	}
	if status.Run == nil {
		requestContext, cancel = context.WithTimeout(ctx, remoteStatusTimeout)
		health, healthErr := process.remote.Health(requestContext)
		cancel()
		if healthErr != nil {
			manager.markRemoteConnectionError(process, healthErr)
			return true
		}
		manager.mu.Lock()
		defer manager.mu.Unlock()
		if health.InstanceID != process.info.WorkerInstanceID {
			manager.interruptRemoteLocked(process, "worker instance changed while the run was active")
		} else {
			manager.interruptRemoteLocked(process, "worker no longer reports the active run")
		}
		return false
	}
	if status.Run.InstanceID != process.info.WorkerInstanceID || status.Run.RunID != process.info.WorkerRunID {
		manager.mu.Lock()
		manager.interruptRemoteLocked(process, "worker run identity changed")
		manager.mu.Unlock()
		return false
	}
	manager.applyRemoteRun(process, *status.Run)
	manager.mu.RLock()
	active := isActive(process.info.State)
	manager.mu.RUnlock()
	if !active {
		manager.finalizeRemoteTerminal(process)
	}
	return active
}

func recoverRemoteStart(client WorkerClient, request worker.StartRequest) (worker.Run, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteStatusTimeout)
	defer cancel()
	status, err := client.Status(ctx)
	if err != nil || status.Run == nil {
		return worker.Run{}, false
	}
	if status.Run.RunID == "" || status.Run.InstanceID == "" || !remoteStartRequestsEqual(status.Run.Request, request) {
		return worker.Run{}, false
	}
	return *status.Run, true
}

func remoteStartRequestsEqual(left, right worker.StartRequest) bool {
	return left.Command == right.Command &&
		left.WorkDir == right.WorkDir &&
		maps.Equal(left.Env, right.Env) &&
		left.StopGraceSeconds == right.StopGraceSeconds &&
		left.LogBufferBytes == right.LogBufferBytes &&
		left.Readiness == right.Readiness
}

func (manager *Manager) applyRemoteRun(process *managedProcess, remoteRun worker.Run) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.applyRemoteRunLocked(process, remoteRun)
}

func (manager *Manager) applyRemoteRunLocked(process *managedProcess, remoteRun worker.Run) {
	if process.info.WorkerInstanceID != "" && (remoteRun.InstanceID != process.info.WorkerInstanceID || remoteRun.RunID != process.info.WorkerRunID) {
		manager.interruptRemoteLocked(process, "worker run identity changed")
		return
	}
	process.info.PID = remoteRun.PID
	process.info.ConnectionState = ConnectionConnected
	process.info.ConnectionError = ""
	process.info.ExitCode = cloneIntPointer(remoteRun.ExitCode)
	process.info.EndedAt = cloneTimePointer(remoteRun.EndedAt)
	process.info.Error = remoteRun.Error
	switch remoteRun.State {
	case worker.StateStarting:
		process.info.State = StateStarting
	case worker.StateRunning:
		process.info.State = StateRunning
	case worker.StateStopping:
		process.info.State = StateStopping
	case worker.StateStopped:
		process.info.State = StateStopped
	case worker.StateFailed:
		process.info.State = StateFailed
	default:
		manager.interruptRemoteLocked(process, fmt.Sprintf("worker returned unsupported state %q", remoteRun.State))
	}
}

func (manager *Manager) closeRemoteStartLocked(process *managedProcess) {
	if process.remoteStartDone == nil || process.remoteStartClosed {
		return
	}
	process.remoteStartClosed = true
	close(process.remoteStartDone)
}

func (manager *Manager) closeRemoteStopLocked(process *managedProcess) {
	if process.remoteStopDone == nil {
		return
	}
	close(process.remoteStopDone)
}

func (manager *Manager) remoteRunInfo(process *managedProcess) RunInfo {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return cloneRunInfo(process.info)
}

func (manager *Manager) stopRemote(ctx context.Context, process *managedProcess) error {
	manager.mu.Lock()
	if !isActive(process.info.State) {
		manager.mu.Unlock()
		return nil
	}
	process.stopRequested = true
	process.info.State = StateStopping
	if process.info.WorkerRunID == "" {
		startDone := process.remoteStartDone
		manager.mu.Unlock()
		select {
		case <-startDone:
			return manager.stopRemote(ctx, process)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if process.remoteStopStarted {
		stopDone := process.remoteStopDone
		manager.mu.Unlock()
		select {
		case <-stopDone:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	process.remoteStopStarted = true
	process.remoteStopDone = make(chan struct{})
	client := process.remote
	workerRunID := process.info.WorkerRunID
	profileName := process.info.ProfileName
	manager.mu.Unlock()

	remoteRun, err := client.Stop(ctx, workerRunID)
	manager.mu.Lock()
	manager.closeRemoteStopLocked(process)
	manager.mu.Unlock()
	if err != nil {
		manager.markRemoteConnectionError(process, err)
		return fmt.Errorf("stop remote backend profile %q: %w", profileName, err)
	}
	manager.applyRemoteRun(process, remoteRun)
	manager.mu.RLock()
	active := isActive(process.info.State)
	manager.mu.RUnlock()
	if !active {
		manager.finalizeRemoteTerminal(process)
	}
	return nil
}

func (manager *Manager) finalizeRemoteTerminal(process *managedProcess) {
	manager.mu.Lock()
	if process.doneClosed || process.remoteFinalizing {
		manager.mu.Unlock()
		return
	}
	process.remoteFinalizing = true
	manager.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), remoteStatusTimeout)
	manager.syncRemoteLog(ctx, process)
	cancel()

	manager.mu.Lock()
	process.remoteFinalizing = false
	manager.closeRemoteDoneLocked(process)
	manager.mu.Unlock()
}

func (manager *Manager) markRemoteConnectionError(process *managedProcess, connectionErr error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !isActive(process.info.State) {
		return
	}
	process.info.ConnectionState = ConnectionUnknown
	process.info.ConnectionError = truncateBackendError(connectionErr.Error())
}

func (manager *Manager) interruptRemoteLocked(process *managedProcess, reason string) {
	if !isActive(process.info.State) {
		return
	}
	endedAt := time.Now().UTC()
	process.info.State = StateInterrupted
	process.info.EndedAt = &endedAt
	process.info.Error = reason
	process.info.ConnectionState = ConnectionUnknown
	manager.closeRemoteDoneLocked(process)
}

func (manager *Manager) closeRemoteDoneLocked(process *managedProcess) {
	if process.doneClosed {
		return
	}
	process.doneClosed = true
	if process.remoteCancel != nil {
		process.remoteCancel()
	}
	close(process.done)
}

func (manager *Manager) mirrorRemoteLogs(ctx context.Context, process *managedProcess) {
	for {
		events, failures, err := process.remote.SubscribeLogs(ctx, process.info.WorkerRunID)
		if err != nil {
			manager.markRemoteConnectionError(process, err)
			if !waitRemoteRetry(ctx, process.done) {
				return
			}
			continue
		}
		streamEnded := false
		for !streamEnded {
			select {
			case event, open := <-events:
				if !open {
					events = nil
					if failures == nil {
						streamEnded = true
					}
					continue
				}
				if !manager.applyRemoteLogEvent(process, event) {
					manager.syncRemoteLog(ctx, process)
				}
			case failure, open := <-failures:
				if !open {
					failures = nil
					if events == nil {
						streamEnded = true
					}
					continue
				}
				if failure != nil && !errors.Is(failure, context.Canceled) {
					manager.markRemoteConnectionError(process, failure)
				}
			case <-ctx.Done():
				return
			case <-process.done:
				return
			}
		}
		if !waitRemoteRetry(ctx, process.done) {
			return
		}
	}
}

func (manager *Manager) applyRemoteLogEvent(process *managedProcess, event worker.LogEvent) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if event.Kind == "snapshot" {
		manager.applyRemoteSnapshotLocked(process, event.Offset, event.Data)
		return true
	}
	if !process.remoteOffsetSet || event.Offset > process.remoteNextOffset {
		return false
	}
	data := event.Data
	if event.Offset < process.remoteNextOffset {
		overlap := process.remoteNextOffset - event.Offset
		if overlap >= int64(len(data)) {
			return true
		}
		data = data[overlap:]
	}
	_, _ = process.log.Write(data)
	process.remoteNextOffset += int64(len(data))
	return true
}

func (manager *Manager) syncRemoteLog(ctx context.Context, process *managedProcess) {
	requestContext, cancel := context.WithTimeout(ctx, remoteStatusTimeout)
	snapshot, err := process.remote.Logs(requestContext, process.info.WorkerRunID)
	cancel()
	if err != nil {
		manager.markRemoteConnectionError(process, err)
		return
	}
	manager.mu.Lock()
	manager.applyRemoteSnapshotLocked(process, snapshot.StartOffset, snapshot.Data)
	manager.mu.Unlock()
}

func (manager *Manager) applyRemoteSnapshotLocked(process *managedProcess, start int64, data []byte) {
	end := start + int64(len(data))
	if !process.remoteOffsetSet || process.remoteNextOffset < start || process.remoteNextOffset > end {
		process.log.Clear()
		_, _ = process.log.Write(data)
	} else if process.remoteNextOffset < end {
		_, _ = process.log.Write(data[process.remoteNextOffset-start:])
	}
	process.remoteNextOffset = end
	process.remoteOffsetSet = true
}

func waitRemoteRetry(ctx context.Context, done <-chan struct{}) bool {
	timer := time.NewTimer(remoteRetryInterval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	case <-done:
		return false
	}
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func truncateBackendError(message string) string {
	const limit = 4096
	if len(message) <= limit {
		return message
	}
	return message[:limit]
}
