package llm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/provider"
	"github.com/ekk1/ai-desktop/internal/session"
)

const (
	RunEventSnapshot = "snapshot"
	RunEventChunk    = "chunk"
	RunEventState    = "state"

	maxRunErrorBytes = 4096
)

var (
	ErrRunNotFound         = errors.New("LLM run not found")
	ErrRunNotActive        = errors.New("LLM run is not active")
	ErrManagerClosed       = errors.New("LLM run manager is closed")
	ErrSessionHasActiveRun = errors.New("session has active LLM runs")
)

type RunEvent struct {
	Type  string `json:"type"`
	Run   Run    `json:"run,omitempty"`
	Chunk string `json:"chunk,omitempty"`
}

type runJob struct {
	run       Run
	prepared  provider.PreparedRequest
	quickPath provider.QuickPath
	cancel    context.CancelFunc
	ctx       context.Context
}

type Manager struct {
	mu          sync.RWMutex
	config      *config.Repository
	sessions    *session.Service
	assembler   *Assembler
	executor    provider.Executor
	store       *RunStore
	accepting   bool
	live        map[string]Run
	active      map[string]context.CancelFunc
	subscribers map[string]map[chan RunEvent]struct{}
	waitGroup   sync.WaitGroup
}

func NewManager(
	configRepository *config.Repository,
	sessions *session.Service,
	assembler *Assembler,
	executor provider.Executor,
	store *RunStore,
) *Manager {
	return &Manager{
		config: configRepository, sessions: sessions, assembler: assembler, executor: executor, store: store,
		accepting: true, live: make(map[string]Run), active: make(map[string]context.CancelFunc),
		subscribers: make(map[string]map[chan RunEvent]struct{}),
	}
}

func (manager *Manager) Start(sessionID, panelID string, quickPathIDs []string) ([]Run, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.accepting {
		return nil, ErrManagerClosed
	}
	if len(quickPathIDs) == 0 {
		return nil, fmt.Errorf("at least one quick path is required")
	}
	seen := make(map[string]struct{}, len(quickPathIDs))
	for _, quickPathID := range quickPathIDs {
		if _, duplicate := seen[quickPathID]; duplicate {
			return nil, fmt.Errorf("duplicate quick path %q", quickPathID)
		}
		seen[quickPathID] = struct{}{}
	}
	workspace, exists := manager.sessions.Get(sessionID)
	if !exists {
		return nil, session.ErrSessionNotFound
	}
	if _, err := manager.sessions.PathTo(sessionID, panelID); err != nil {
		return nil, err
	}
	configuration := manager.config.Snapshot().LLM
	providers := make(map[string]provider.Provider, len(configuration.Providers))
	for _, item := range configuration.Providers {
		providers[item.ID] = item
	}
	quickPaths := make(map[string]provider.QuickPath, len(configuration.QuickPaths))
	for _, item := range configuration.QuickPaths {
		quickPaths[item.ID] = item
	}
	jobs := make([]runJob, 0, len(quickPathIDs))
	now := time.Now().UTC()
	for index, quickPathID := range quickPathIDs {
		quickPath, exists := quickPaths[quickPathID]
		if !exists {
			return nil, fmt.Errorf("quick path %q does not exist", quickPathID)
		}
		providerConfig, exists := providers[quickPath.ProviderID]
		if !exists {
			return nil, fmt.Errorf("quick path %q references a missing provider", quickPathID)
		}
		if !providerConfig.Enabled {
			return nil, fmt.Errorf("provider %q is disabled", providerConfig.ID)
		}
		prepared, snapshot, err := manager.assembler.Build(workspace, panelID, providerConfig, quickPath)
		if err != nil {
			return nil, fmt.Errorf("assemble quick path %q: %w", quickPathID, err)
		}
		runID, err := newRunID()
		if err != nil {
			return nil, err
		}
		createdAt := now.Add(time.Duration(index) * time.Nanosecond)
		run := Run{
			ID: runID, SessionID: sessionID, ParentPanelID: panelID, QuickPathID: quickPathID,
			State: RunQueued, Snapshot: snapshot, CreatedAt: createdAt,
		}
		ctx, cancel := context.WithCancel(context.Background())
		jobs = append(jobs, runJob{run: run, prepared: prepared, quickPath: quickPath, ctx: ctx, cancel: cancel})
	}
	for index := range jobs {
		if err := manager.store.Save(jobs[index].run); err != nil {
			for cancelIndex := range jobs {
				jobs[cancelIndex].cancel()
			}
			return nil, err
		}
	}
	result := make([]Run, len(jobs))
	for index := range jobs {
		manager.live[jobs[index].run.ID] = cloneRun(jobs[index].run)
		manager.active[jobs[index].run.ID] = jobs[index].cancel
		result[index] = cloneRun(jobs[index].run)
		manager.waitGroup.Add(1)
		go manager.execute(jobs[index])
	}
	return result, nil
}

func (manager *Manager) Get(runID string) (Run, bool) {
	manager.mu.RLock()
	run, exists := manager.live[runID]
	manager.mu.RUnlock()
	if exists {
		return cloneRun(run), true
	}
	return manager.store.Get(runID)
}

func (manager *Manager) List(sessionID string) []Run {
	return manager.store.List(sessionID)
}

func (manager *Manager) DeleteSession(sessionID string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for runID := range manager.active {
		if run, exists := manager.live[runID]; exists && run.SessionID == sessionID {
			return ErrSessionHasActiveRun
		}
	}
	if err := manager.sessions.DeleteSession(sessionID); err != nil {
		return err
	}
	for runID, run := range manager.live {
		if run.SessionID == sessionID {
			delete(manager.live, runID)
			delete(manager.subscribers, runID)
		}
	}
	manager.store.ForgetSession(sessionID)
	return nil
}

func (manager *Manager) Cancel(runID string) error {
	manager.mu.RLock()
	_, exists := manager.live[runID]
	cancel, active := manager.active[runID]
	manager.mu.RUnlock()
	if !exists {
		if _, stored := manager.store.Get(runID); !stored {
			return ErrRunNotFound
		}
	}
	if !active {
		return ErrRunNotActive
	}
	cancel()
	return nil
}

func (manager *Manager) Subscribe(runID string) (<-chan RunEvent, func(), error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	run, exists := manager.live[runID]
	if !exists {
		var stored bool
		run, stored = manager.store.Get(runID)
		if !stored {
			return nil, nil, ErrRunNotFound
		}
	}
	channel := make(chan RunEvent, 64)
	channel <- RunEvent{Type: RunEventSnapshot, Run: cloneRun(run)}
	if run.State.terminal() {
		close(channel)
		return channel, func() {}, nil
	}
	if manager.subscribers[runID] == nil {
		manager.subscribers[runID] = make(map[chan RunEvent]struct{})
	}
	manager.subscribers[runID][channel] = struct{}{}
	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			manager.mu.Lock()
			defer manager.mu.Unlock()
			if _, present := manager.subscribers[runID][channel]; present {
				delete(manager.subscribers[runID], channel)
				close(channel)
			}
		})
	}
	return channel, unsubscribe, nil
}

func (manager *Manager) Shutdown(ctx context.Context) error {
	manager.mu.Lock()
	manager.accepting = false
	for _, cancel := range manager.active {
		cancel()
	}
	manager.mu.Unlock()
	done := make(chan struct{})
	go func() {
		manager.waitGroup.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) execute(job runJob) {
	defer manager.waitGroup.Done()
	run := cloneRun(job.run)
	run.State = RunRunning
	run.StartedAt = time.Now().UTC()
	if err := manager.store.Save(run); err != nil {
		manager.finishFailed(run, "persist_failed", err)
		return
	}
	manager.setRunAndPublish(run, RunEvent{Type: RunEventState, Run: cloneRun(run)}, false)
	result, executionError := manager.executor.Execute(job.ctx, job.prepared, func(chunk string) {
		manager.appendChunk(run.ID, chunk)
	})
	current, _ := manager.Get(run.ID)
	run = current
	run.Output = result.Content
	run.StatusCode = result.StatusCode
	if executionError != nil {
		if errors.Is(executionError, context.Canceled) || errors.Is(job.ctx.Err(), context.Canceled) {
			run.State = RunCancelled
			run.Error = RunError{Code: "cancelled", Message: "run cancelled"}
			manager.finish(run)
			return
		}
		run.State = RunFailed
		run.Error = RunError{Code: providerErrorCode(executionError), Message: boundedError(executionError)}
		manager.finish(run)
		return
	}
	panel, err := manager.sessions.CreatePanel(run.SessionID, session.CreatePanelInput{
		ParentID: run.ParentPanelID, Title: job.quickPath.Name, Content: run.Output, Included: true,
		Result: &session.ResultMetadata{Source: "llm", RunID: run.ID, QuickPathID: run.QuickPathID},
	})
	if err != nil {
		run.State = RunFailed
		run.Error = RunError{Code: "panel_create_failed", Message: boundedError(err)}
		manager.finish(run)
		return
	}
	run.State = RunSucceeded
	run.ResultPanelID = panel.ID
	run.Error = RunError{}
	manager.finish(run)
}

func (manager *Manager) appendChunk(runID, chunk string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	run, exists := manager.live[runID]
	if !exists || run.State.terminal() {
		return
	}
	run.Output += chunk
	manager.live[runID] = run
	manager.publishLocked(runID, RunEvent{Type: RunEventChunk, Chunk: chunk}, false)
}

func (manager *Manager) finishFailed(run Run, code string, err error) {
	run.State = RunFailed
	run.Error = RunError{Code: code, Message: boundedError(err)}
	manager.finish(run)
}

func (manager *Manager) finish(run Run) {
	run.CompletedAt = time.Now().UTC()
	if err := manager.store.Save(run); err != nil {
		run.State = RunFailed
		run.Error = RunError{Code: "persist_failed", Message: boundedError(err)}
	}
	manager.setRunAndPublish(run, RunEvent{Type: RunEventState, Run: cloneRun(run)}, true)
}

func (manager *Manager) setRunAndPublish(run Run, event RunEvent, terminal bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.live[run.ID] = cloneRun(run)
	if terminal {
		delete(manager.active, run.ID)
	}
	manager.publishLocked(run.ID, event, terminal)
}

func (manager *Manager) publishLocked(runID string, event RunEvent, terminal bool) {
	for channel := range manager.subscribers[runID] {
		select {
		case channel <- event:
		default:
			if terminal {
				select {
				case <-channel:
				default:
				}
				channel <- event
			}
		}
		if terminal {
			close(channel)
		}
	}
	if terminal {
		delete(manager.subscribers, runID)
	}
}

func providerErrorCode(err error) string {
	if errors.Is(err, provider.ErrHTTPStatus) {
		return "provider_http_status"
	}
	if errors.Is(err, provider.ErrResponseLimit) {
		return "provider_response_limit"
	}
	return "provider_error"
}

func boundedError(err error) string {
	message := err.Error()
	if len(message) > maxRunErrorBytes {
		message = message[:maxRunErrorBytes]
	}
	return message
}

func newRunID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate run ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}
