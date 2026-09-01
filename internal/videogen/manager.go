package videogen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

var (
	ErrVideoPresetNotFound = errors.New("video preset not found")
	ErrVideoPresetDisabled = errors.New("video preset is disabled")
	ErrVideoManagerClosed  = errors.New("video manager is shutting down")
	ErrVideoItemDisabled   = errors.New("video item is disabled")
	ErrVideoResultLimit    = errors.New("video result exceeds provider byte limit")
)

// VideoRemoteClient is the transport boundary for native stable-diffusion.cpp
// video jobs. It deliberately receives the full saved provider rather than a
// URL so the transport can enforce its configured limits.
type VideoRemoteClient interface {
	Submit(context.Context, videoconfig.HTTPProvider, []byte) (sdcpp.VideoSubmission, error)
	Job(context.Context, videoconfig.HTTPProvider, string) (sdcpp.VideoJob, error)
	Cancel(context.Context, videoconfig.HTTPProvider, string) error
}

const videoCancellationTimeout = 5 * time.Second

// AttemptEvent is the batch stream payload. Snapshot events are emitted when a
// client subscribes; state events are emitted after every persisted mutation.
type AttemptEvent struct {
	Type     string    `json:"type"`
	BatchID  string    `json:"batch_id"`
	Attempt  Attempt   `json:"attempt"`
	Attempts []Attempt `json:"attempts,omitempty"`
}

type videoLimiter struct {
	mu      sync.Mutex
	limit   int
	used    int
	changed chan struct{}
}

func newVideoLimiter(limit int) *videoLimiter {
	return &videoLimiter{limit: limit, changed: make(chan struct{})}
}

func (limiter *videoLimiter) setLimit(limit int) {
	limiter.mu.Lock()
	if limiter.limit != limit {
		limiter.limit = limit
		close(limiter.changed)
		limiter.changed = make(chan struct{})
	}
	limiter.mu.Unlock()
}

func (limiter *videoLimiter) acquire(ctx context.Context) bool {
	for {
		limiter.mu.Lock()
		if limiter.used < limiter.limit {
			limiter.used++
			limiter.mu.Unlock()
			return true
		}
		changed := limiter.changed
		limiter.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}

func (limiter *videoLimiter) release() {
	limiter.mu.Lock()
	if limiter.used > 0 {
		limiter.used--
	}
	close(limiter.changed)
	limiter.changed = make(chan struct{})
	limiter.mu.Unlock()
}

type videoPreset struct {
	kind  videoconfig.ExecutionKind
	key   string
	http  *videoconfig.HTTPProvider
	cli   *videoconfig.CLIPreset
	limit int
}

type videoAttemptRun struct {
	lifecycle sync.Mutex
	attemptID string
	batchID   string
	itemID    string
	preset    videoPreset
	prepared  PreparedHTTP
	ctx       context.Context
	cancel    context.CancelFunc
}

type videoScheduledAttempt struct {
	attempt     Attempt
	run         *videoAttemptRun
	batchLimit  *videoLimiter
	presetLimit *videoLimiter
}

type videoBatchQueue struct{ jobs chan videoScheduledAttempt }

type preparedVideoStart struct {
	item     Item
	prepared PreparedHTTP
	snapshot Snapshot
}

type pendingTerminalUpdate struct {
	run     *videoAttemptRun
	input   UpdateAttemptInput
	lastErr error
}

// Manager supplies one persistent scheduler for both remote HTTP jobs and
// local CLI jobs. Calls which create Attempts do all validation before placing
// an external request or process on the queue.
type Manager struct {
	startMu          sync.Mutex
	mu               sync.RWMutex
	config           *config.Repository
	service          *Service
	assembler        *HTTPAssembler
	remote           VideoRemoteClient
	workspace        *WorkspaceManager
	cli              *CLIExecutor
	assets           *asset.Repository
	accepting        bool
	attempts         map[string]*videoAttemptRun
	pendingTerminal  map[string]pendingTerminalUpdate
	presetSem        map[string]*videoLimiter
	batchSem         map[string]*videoLimiter
	batchQueues      map[string]*videoBatchQueue
	subscribers      map[string]map[uint64]chan AttemptEvent
	nextSubID        uint64
	done             chan struct{}
	doneOnce         sync.Once
	shutdownOnce     sync.Once
	shutdownComplete chan struct{}
	shutdownErr      error
	starts           sync.WaitGroup
	wg               sync.WaitGroup
}

func NewManager(configuration *config.Repository, service *Service, assembler *HTTPAssembler, remote VideoRemoteClient, workspace *WorkspaceManager, cli *CLIExecutor, assets *asset.Repository) *Manager {
	manager := &Manager{
		config: configuration, service: service, assembler: assembler, remote: remote, workspace: workspace, cli: cli, assets: assets,
		accepting: true, attempts: make(map[string]*videoAttemptRun), pendingTerminal: make(map[string]pendingTerminalUpdate), presetSem: make(map[string]*videoLimiter),
		batchSem: make(map[string]*videoLimiter), batchQueues: make(map[string]*videoBatchQueue),
		subscribers: make(map[string]map[uint64]chan AttemptEvent), done: make(chan struct{}), shutdownComplete: make(chan struct{}),
	}
	return manager
}

func (manager *Manager) StartBatch(batchID string) ([]Attempt, error) {
	if err := manager.beginStart(); err != nil {
		return nil, err
	}
	defer manager.starts.Done()
	manager.startMu.Lock()
	defer manager.startMu.Unlock()
	batch, ok := manager.service.Get(batchID)
	if !ok {
		return nil, ErrBatchNotFound
	}
	if err := manager.recoverPendingTerminal(batchID); err != nil {
		return nil, err
	}
	batch, _ = manager.service.Get(batchID)
	preset, err := manager.lookupPreset(batch)
	if err != nil {
		return nil, err
	}
	plans := make([]preparedVideoStart, 0, len(batch.Items))
	for _, item := range batch.Items {
		if !item.Enabled {
			continue
		}
		if hasActiveAttempt(item) {
			return nil, ErrActiveAttempt
		}
		prepared, snapshot, err := manager.preflight(batch, item, preset)
		if err != nil {
			return nil, err
		}
		plans = append(plans, preparedVideoStart{item: item, prepared: prepared, snapshot: snapshot})
	}
	attempts := make([]Attempt, 0, len(plans))
	runs := make([]*videoAttemptRun, 0, len(plans))
	for _, plan := range plans {
		attempt, run, err := manager.createRun(batch, plan.item, preset, plan.prepared, plan.snapshot)
		if err != nil {
			return attempts, errors.Join(err, manager.cancelUnenqueuedRuns(runs, "batch_start_failed"))
		}
		attempts, runs = append(attempts, attempt), append(runs, run)
	}
	for index, run := range runs {
		manager.enqueueRun(batch, attempts[index], run)
	}
	return attempts, nil
}

func (manager *Manager) StartItem(batchID, itemID string) (Attempt, error) {
	if err := manager.beginStart(); err != nil {
		return Attempt{}, err
	}
	defer manager.starts.Done()
	manager.startMu.Lock()
	defer manager.startMu.Unlock()
	batch, ok := manager.service.Get(batchID)
	if !ok {
		return Attempt{}, ErrBatchNotFound
	}
	if err := manager.recoverPendingTerminal(batchID); err != nil {
		return Attempt{}, err
	}
	batch, _ = manager.service.Get(batchID)
	position := itemIndex(batch.Items, itemID)
	if position < 0 {
		return Attempt{}, ErrItemNotFound
	}
	item := batch.Items[position]
	if !item.Enabled {
		return Attempt{}, ErrVideoItemDisabled
	}
	if hasActiveAttempt(item) {
		return Attempt{}, ErrActiveAttempt
	}
	preset, err := manager.lookupPreset(batch)
	if err != nil {
		return Attempt{}, err
	}
	return manager.start(batch, item, preset)
}

// Retry takes the current Batch and Item definition, not the old snapshot, so
// each retry records the currently selected preset and assets as a new Attempt.
func (manager *Manager) Retry(attemptID string) (Attempt, error) {
	attempt, ok := manager.GetAttempt(attemptID)
	if !ok {
		return Attempt{}, ErrAttemptNotFound
	}
	manager.mu.RLock()
	_, pending := manager.pendingTerminal[attemptID]
	manager.mu.RUnlock()
	if pending {
		if err := manager.retryPendingTerminal(attemptID); err != nil {
			return Attempt{}, fmt.Errorf("recover terminal video attempt %s: %w", attemptID, err)
		}
		attempt, ok = manager.GetAttempt(attemptID)
		if !ok {
			return Attempt{}, ErrAttemptNotFound
		}
	}
	if !terminalAttemptState(attempt.State) {
		return Attempt{}, ErrActiveAttempt
	}
	return manager.StartItem(attempt.BatchID, attempt.ItemID)
}

func (manager *Manager) GetAttempt(attemptID string) (Attempt, bool) {
	manager.mu.RLock()
	run := manager.attempts[attemptID]
	manager.mu.RUnlock()
	if run != nil {
		if attempt, ok := manager.attemptFrom(run.batchID, run.itemID, attemptID); ok {
			return manager.decoratePendingTerminal(attempt), true
		}
	}
	for _, batch := range manager.service.List(Filter{}) {
		for _, item := range batch.Items {
			if position := attemptIndex(item.Attempts, attemptID); position >= 0 {
				return manager.decoratePendingTerminal(cloneAttempt(item.Attempts[position])), true
			}
		}
	}
	return Attempt{}, false
}

func (manager *Manager) decoratePendingTerminal(attempt Attempt) Attempt {
	manager.mu.RLock()
	attempt = manager.decoratePendingTerminalLocked(attempt)
	manager.mu.RUnlock()
	return attempt
}

func (manager *Manager) decoratePendingTerminalLocked(attempt Attempt) Attempt {
	if pending, ok := manager.pendingTerminal[attempt.ID]; ok {
		attempt.Error = AttemptError{Code: "storage_failure", Message: boundedVideoError(pending.lastErr.Error())}
	}
	return attempt
}

func (manager *Manager) recoverPendingTerminal(batchID string) error {
	manager.mu.RLock()
	ids := make([]string, 0)
	for id, pending := range manager.pendingTerminal {
		if pending.run.batchID == batchID {
			ids = append(ids, id)
		}
	}
	manager.mu.RUnlock()
	for _, id := range ids {
		if err := manager.retryPendingTerminal(id); err != nil {
			return fmt.Errorf("recover terminal video attempt %s: %w", id, err)
		}
	}
	return nil
}

func (manager *Manager) retryPendingTerminal(attemptID string) error {
	manager.mu.RLock()
	pending, ok := manager.pendingTerminal[attemptID]
	manager.mu.RUnlock()
	if !ok {
		return nil
	}
	attempt, err := manager.service.UpdateAttempt(pending.run.batchID, pending.run.itemID, pending.run.attemptID, pending.input)
	if err != nil {
		if current, exists := manager.attemptFrom(pending.run.batchID, pending.run.itemID, pending.run.attemptID); exists && terminalAttemptState(current.State) {
			err = nil
			attempt = current
		}
	}
	manager.mu.Lock()
	if err == nil {
		delete(manager.pendingTerminal, attemptID)
	} else {
		pending.lastErr = err
		manager.pendingTerminal[attemptID] = pending
	}
	manager.mu.Unlock()
	if err == nil {
		manager.publish(pending.run.batchID, AttemptEvent{Type: "state", Attempt: attempt})
	}
	return err
}

func (manager *Manager) start(batch Batch, item Item, preset videoPreset) (Attempt, error) {
	prepared, snapshot, err := manager.preflight(batch, item, preset)
	if err != nil {
		return Attempt{}, err
	}
	attempt, run, err := manager.createRun(batch, item, preset, prepared, snapshot)
	if err != nil {
		return Attempt{}, err
	}
	manager.enqueueRun(batch, attempt, run)
	return attempt, nil
}

func (manager *Manager) preflight(batch Batch, item Item, preset videoPreset) (PreparedHTTP, Snapshot, error) {
	var (
		prepared PreparedHTTP
		snapshot Snapshot
		err      error
	)
	switch preset.kind {
	case videoconfig.ExecutionHTTP:
		prepared, snapshot, err = manager.assembler.BuildHTTP(batch, item, *preset.http)
	case videoconfig.ExecutionLocalCLI:
		snapshot, err = manager.buildCLISnapshot(batch, item, *preset.cli)
	default:
		err = fmt.Errorf("unsupported video execution kind %q", preset.kind)
	}
	if err != nil {
		return PreparedHTTP{}, Snapshot{}, err
	}
	return prepared, snapshot, nil
}

func (manager *Manager) createRun(batch Batch, item Item, preset videoPreset, prepared PreparedHTTP, snapshot Snapshot) (Attempt, *videoAttemptRun, error) {
	attempt, err := manager.service.CreateAttempt(batch.ID, item.ID, CreateAttemptInput{State: AttemptQueued, Snapshot: snapshot})
	if err != nil {
		return Attempt{}, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &videoAttemptRun{attemptID: attempt.ID, batchID: batch.ID, itemID: item.ID, preset: preset, prepared: prepared, ctx: ctx, cancel: cancel}
	return attempt, run, nil
}

// cancelUnenqueuedRuns prevents a persistence failure halfway through a batch
// from leaving earlier queued Attempts active without an in-memory run or any
// chance of dispatch. No external work has been enqueued at this point.
func (manager *Manager) cancelUnenqueuedRuns(runs []*videoAttemptRun, code string) error {
	var failures []error
	for _, run := range runs {
		run.cancel()
		current, ok := manager.GetAttempt(run.attemptID)
		if !ok || current.State != AttemptQueued {
			continue
		}
		input := updateFor(current, AttemptCancelled)
		input.Error = AttemptError{Code: code, Message: "video batch scheduling did not complete"}
		if _, err := manager.completeAttempt(run, input); err != nil {
			failures = append(failures, fmt.Errorf("cancel unqueued video attempt %s: %w", run.attemptID, err))
		}
	}
	return errors.Join(failures...)
}

func (manager *Manager) enqueueRun(batch Batch, attempt Attempt, run *videoAttemptRun) {
	manager.mu.Lock()
	manager.attempts[attempt.ID] = run
	presetLimit := manager.presetSemaphore(run.preset)
	batchLimit := manager.batchSemaphore(batch)
	queue := manager.batchQueue(batch.ID)
	manager.wg.Add(1)
	manager.mu.Unlock()
	manager.publish(batch.ID, AttemptEvent{Type: "state", Attempt: attempt})
	queue.jobs <- videoScheduledAttempt{attempt: attempt, run: run, batchLimit: batchLimit, presetLimit: presetLimit}
}

func (manager *Manager) beginStart() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.accepting {
		return ErrVideoManagerClosed
	}
	manager.starts.Add(1)
	return nil
}

func (manager *Manager) lookupPreset(batch Batch) (videoPreset, error) {
	videos := manager.config.Snapshot().Videos
	switch batch.ExecutionKind {
	case videoconfig.ExecutionHTTP:
		for _, provider := range videos.HTTPProviders {
			if provider.ID != batch.PresetID {
				continue
			}
			if !provider.Enabled {
				return videoPreset{}, fmt.Errorf("%w: %s", ErrVideoPresetDisabled, batch.PresetID)
			}
			clone := cloneHTTPProvider(provider)
			return videoPreset{kind: batch.ExecutionKind, key: string(batch.ExecutionKind) + ":" + provider.ID, http: &clone, limit: provider.MaxConcurrentJobs}, nil
		}
	case videoconfig.ExecutionLocalCLI:
		for _, preset := range videos.CLIPresets {
			if preset.ID != batch.PresetID {
				continue
			}
			if !preset.Enabled {
				return videoPreset{}, fmt.Errorf("%w: %s", ErrVideoPresetDisabled, batch.PresetID)
			}
			clone := cloneCLIPreset(preset)
			return videoPreset{kind: batch.ExecutionKind, key: string(batch.ExecutionKind) + ":" + preset.ID, cli: &clone, limit: 1}, nil
		}
	}
	return videoPreset{}, fmt.Errorf("%w: %s", ErrVideoPresetNotFound, batch.PresetID)
}

func (manager *Manager) presetSemaphore(preset videoPreset) *videoLimiter {
	limiter := manager.presetSem[preset.key]
	if limiter == nil {
		limiter = newVideoLimiter(preset.limit)
		manager.presetSem[preset.key] = limiter
	} else {
		limiter.setLimit(preset.limit)
	}
	return limiter
}

func (manager *Manager) batchSemaphore(batch Batch) *videoLimiter {
	limiter := manager.batchSem[batch.ID]
	if limiter == nil {
		limiter = newVideoLimiter(batch.Concurrency)
		manager.batchSem[batch.ID] = limiter
	} else {
		limiter.setLimit(batch.Concurrency)
	}
	return limiter
}

func (manager *Manager) batchQueue(batchID string) *videoBatchQueue {
	queue := manager.batchQueues[batchID]
	if queue == nil {
		queue = &videoBatchQueue{jobs: make(chan videoScheduledAttempt, 4096)}
		manager.batchQueues[batchID] = queue
		go manager.dispatch(queue)
	}
	return queue
}

func (manager *Manager) dispatch(queue *videoBatchQueue) {
	for {
		select {
		case scheduled := <-queue.jobs:
			if !scheduled.batchLimit.acquire(scheduled.run.ctx) {
				manager.cancelQueued(scheduled.run, scheduled.run.ctx.Err())
				manager.finishRun(scheduled.run)
				manager.wg.Done()
				continue
			}
			if !scheduled.presetLimit.acquire(scheduled.run.ctx) {
				scheduled.batchLimit.release()
				manager.cancelQueued(scheduled.run, scheduled.run.ctx.Err())
				manager.finishRun(scheduled.run)
				manager.wg.Done()
				continue
			}
			go manager.runScheduled(scheduled)
		case <-manager.done:
			return
		}
	}
}

func (manager *Manager) runScheduled(scheduled videoScheduledAttempt) {
	defer manager.wg.Done()
	defer manager.finishRun(scheduled.run)
	defer scheduled.batchLimit.release()
	defer scheduled.presetLimit.release()
	switch scheduled.run.preset.kind {
	case videoconfig.ExecutionHTTP:
		manager.runHTTP(scheduled.run)
	case videoconfig.ExecutionLocalCLI:
		manager.runCLI(scheduled.run)
	default:
		manager.fail(scheduled.run, "unsupported_execution", "unsupported video execution kind")
	}
}

func (manager *Manager) runHTTP(run *videoAttemptRun) {
	jobCtx, cancel := context.WithTimeout(run.ctx, run.prepared.JobTimeout)
	defer cancel()
	run.lifecycle.Lock()
	current, ok := manager.GetAttempt(run.attemptID)
	if !ok || terminalAttemptState(current.State) {
		run.lifecycle.Unlock()
		return
	}
	updated, err := manager.updateAttempt(run, updateFor(current, AttemptSubmitting))
	run.lifecycle.Unlock()
	if err != nil {
		manager.fail(run, "storage_failure", boundedVideoError(err.Error()))
		return
	}
	current = updated
	submission, err := manager.remote.Submit(jobCtx, *run.preset.http, run.prepared.Body)
	if err != nil {
		if run.ctx.Err() == nil {
			manager.finishHTTPError(run, current, jobCtx, err)
		}
		return
	}
	run.lifecycle.Lock()
	current, ok = manager.GetAttempt(run.attemptID)
	if !ok || terminalAttemptState(current.State) {
		run.lifecycle.Unlock()
		manager.bestEffortRemoteCancel(run, submission.ID)
		return
	}
	input := updateFor(current, AttemptPolling)
	input.RemoteJobID, input.RemoteStatus = submission.ID, submission.Status
	updated, err = manager.updateAttempt(run, input)
	run.lifecycle.Unlock()
	if err != nil {
		manager.bestEffortRemoteCancel(run, submission.ID)
		manager.fail(run, "storage_failure", boundedVideoError(err.Error()))
		return
	}
	current = updated
	for {
		job, err := manager.remote.Job(jobCtx, *run.preset.http, submission.ID)
		run.lifecycle.Lock()
		current, ok = manager.GetAttempt(run.attemptID)
		if !ok || terminalAttemptState(current.State) {
			run.lifecycle.Unlock()
			return
		}
		if err != nil {
			run.lifecycle.Unlock()
			if run.ctx.Err() == nil {
				manager.finishHTTPError(run, current, jobCtx, err)
			}
			return
		}
		if err := jobCtx.Err(); err != nil {
			run.lifecycle.Unlock()
			if run.ctx.Err() == nil {
				manager.finishHTTPError(run, current, jobCtx, err)
			}
			return
		}
		switch job.Status {
		case "queued", "generating":
			input := updateFor(current, AttemptPolling)
			input.RemoteJobID, input.RemoteStatus, input.QueuePosition = submission.ID, job.Status, job.QueuePosition
			updated, err = manager.updateAttempt(run, input)
			run.lifecycle.Unlock()
			if err != nil {
				manager.fail(run, "storage_failure", boundedVideoError(err.Error()))
				return
			}
			current = updated
			if !waitForVideoPoll(jobCtx, run.prepared.PollInterval) {
				if run.ctx.Err() == nil {
					manager.finishHTTPError(run, current, jobCtx, jobCtx.Err())
				}
				return
			}
		case "failed":
			input := updateFor(current, AttemptFailed)
			input.RemoteJobID, input.RemoteStatus, input.QueuePosition = submission.ID, job.Status, job.QueuePosition
			input.Error = AttemptError{Code: "remote_failed", Message: "remote video generation failed"}
			if job.Error != nil {
				input.Error = AttemptError{Code: job.Error.Code, Message: boundedVideoError(job.Error.Message)}
			}
			_, _ = manager.completeAttempt(run, input)
			run.lifecycle.Unlock()
			return
		case "cancelled":
			input := updateFor(current, AttemptCancelled)
			input.RemoteJobID, input.RemoteStatus, input.QueuePosition = submission.ID, job.Status, job.QueuePosition
			input.Error = AttemptError{}
			_, _ = manager.completeAttempt(run, input)
			run.lifecycle.Unlock()
			return
		case "completed":
			updated, err := manager.importHTTPResult(run, current, job)
			if err != nil {
				input := updateFor(current, AttemptFailed)
				input.RemoteJobID, input.RemoteStatus, input.QueuePosition = submission.ID, job.Status, job.QueuePosition
				input.Error = AttemptError{Code: "invalid_result", Message: boundedVideoError(err.Error())}
				_, _ = manager.completeAttempt(run, input)
				run.lifecycle.Unlock()
				return
			}
			input := updateFor(updated, AttemptSucceeded)
			input.RemoteJobID, input.RemoteStatus, input.QueuePosition = submission.ID, job.Status, job.QueuePosition
			input.ActualFrameCount = job.Result.FrameCount
			input.Error = AttemptError{}
			_, _ = manager.completeAttempt(run, input)
			run.lifecycle.Unlock()
			return
		default:
			run.lifecycle.Unlock()
			manager.finishHTTPError(run, current, jobCtx, fmt.Errorf("unknown remote video job state %q", job.Status))
			return
		}
	}
}

func (manager *Manager) importHTTPResult(run *videoAttemptRun, current Attempt, job sdcpp.VideoJob) (Attempt, error) {
	if job.Result == nil {
		return current, errors.New("completed video job has no result")
	}
	reader, mediaType, extension, err := streamHTTPVideoResult(job.Result.B64JSON, run.preset.http.MaxVideoBytes)
	if err != nil {
		return current, err
	}
	if mediaType != job.Result.MIMEType || !videoFormatMatches(job.Result.OutputFormat, mediaType) {
		return current, fmt.Errorf("video MIME or output format does not match its content")
	}
	created, err := manager.assets.Import(asset.ImportInput{
		Reader: reader, DisplayName: fmt.Sprintf("%s-%s-%s.%s", run.batchID, run.itemID, current.ID, extension),
		MediaType: mediaType, Source: "videogen:" + current.ID,
	})
	if err != nil {
		return current, fmt.Errorf("import video result: %w", err)
	}
	updated, err := manager.service.AttachVideoResult(run.batchID, run.itemID, current.ID, created.ID)
	if err != nil {
		// The import is intentionally retained: the archive Asset is recoverable
		// even when attaching its reference cannot be persisted.
		return current, fmt.Errorf("attach imported video result: %w", err)
	}
	manager.publish(run.batchID, AttemptEvent{Type: "state", Attempt: updated})
	return updated, nil
}

func streamHTTPVideoResult(encoded string, maximum int64) (io.Reader, string, string, error) {
	if encoded == "" || strings.ContainsAny(encoded, "\r\n\t ") {
		return nil, "", "", errors.New("video base64 is empty or contains whitespace")
	}
	if int64(len(encoded)) > ((maximum+2)/3)*4+4 {
		return nil, "", "", ErrVideoResultLimit
	}
	decoded := &videoDecodedLimitReader{reader: base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded)), remaining: maximum}
	header := make([]byte, 12)
	read, err := io.ReadFull(decoded, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, "", "", fmt.Errorf("decode video base64: %w", err)
	}
	mediaType, extension, err := sniffHTTPVideo(header[:read])
	if err != nil {
		return nil, "", "", err
	}
	return io.MultiReader(bytes.NewReader(header[:read]), decoded), mediaType, extension, nil
}

// videoDecodedLimitReader bounds the same streaming reader later handed to
// asset.Import. It probes one byte after the limit so exact-limit payloads end
// cleanly while an oversized result fails before an Asset record is written.
type videoDecodedLimitReader struct {
	reader    io.Reader
	remaining int64
}

func (reader *videoDecodedLimitReader) Read(destination []byte) (int, error) {
	if reader.remaining <= 0 {
		var probe [1]byte
		count, err := reader.reader.Read(probe[:])
		if count > 0 {
			return 0, ErrVideoResultLimit
		}
		return 0, err
	}
	if int64(len(destination)) > reader.remaining {
		destination = destination[:reader.remaining]
	}
	count, err := reader.reader.Read(destination)
	reader.remaining -= int64(count)
	return count, err
}

func sniffHTTPVideo(contents []byte) (string, string, error) {
	switch {
	case len(contents) >= 4 && bytes.Equal(contents[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}):
		return "video/webm", "webm", nil
	case len(contents) >= 12 && bytes.Equal(contents[:4], []byte("RIFF")) && bytes.Equal(contents[8:12], []byte("AVI ")):
		return "video/x-msvideo", "avi", nil
	case len(contents) >= 12 && bytes.Equal(contents[:4], []byte("RIFF")) && bytes.Equal(contents[8:12], []byte("WEBP")):
		return "image/webp", "webp", nil
	default:
		return "", "", errors.New("unsupported video signature")
	}
}

func videoFormatMatches(format, mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "webm":
		return mediaType == "video/webm"
	case "avi":
		return mediaType == "video/x-msvideo"
	case "webp":
		return mediaType == "image/webp"
	default:
		return false
	}
}

func (manager *Manager) finishHTTPError(run *videoAttemptRun, current Attempt, ctx context.Context, err error) {
	state, code := AttemptFailed, "request_failed"
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		state, code = AttemptCancelled, "cancelled"
	} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		code = "timeout"
	}
	input := updateFor(current, state)
	input.Error = AttemptError{Code: code, Message: boundedVideoError(err.Error())}
	_, _ = manager.completeAttempt(run, input)
}

func (manager *Manager) fail(run *videoAttemptRun, code, message string) {
	current, ok := manager.GetAttempt(run.attemptID)
	if !ok || terminalAttemptState(current.State) {
		return
	}
	input := updateFor(current, AttemptFailed)
	input.Error = AttemptError{Code: code, Message: boundedVideoError(message)}
	_, _ = manager.completeAttempt(run, input)
}

func updateFor(current Attempt, state AttemptState) UpdateAttemptInput {
	return UpdateAttemptInput{
		State: state, RemoteJobID: current.RemoteJobID, RemoteStatus: current.RemoteStatus, QueuePosition: current.QueuePosition,
		PID: current.PID, ActualFrameCount: current.ActualFrameCount, WorkspaceRelativePath: current.WorkspaceRelativePath,
		OutputAssetID: current.OutputAssetID, Error: current.Error,
	}
}

func (manager *Manager) updateAttempt(run *videoAttemptRun, input UpdateAttemptInput) (Attempt, error) {
	var (
		attempt Attempt
		err     error
	)
	for retry := 0; retry < 3; retry++ {
		attempt, err = manager.service.UpdateAttempt(run.batchID, run.itemID, run.attemptID, input)
		if err == nil {
			manager.publish(run.batchID, AttemptEvent{Type: "state", Attempt: attempt})
			return attempt, nil
		}
		if retry < 2 {
			timer := time.NewTimer(time.Duration(1<<retry) * 10 * time.Millisecond)
			<-timer.C
		}
	}
	return attempt, err
}

func (manager *Manager) completeAttempt(run *videoAttemptRun, input UpdateAttemptInput) (Attempt, error) {
	attempt, err := manager.updateAttempt(run, input)
	if err == nil {
		manager.mu.Lock()
		delete(manager.pendingTerminal, run.attemptID)
		manager.mu.Unlock()
		return attempt, nil
	}
	current, _ := manager.attemptFrom(run.batchID, run.itemID, run.attemptID)
	manager.mu.Lock()
	manager.pendingTerminal[run.attemptID] = pendingTerminalUpdate{run: run, input: input, lastErr: err}
	manager.mu.Unlock()
	current.Error = AttemptError{Code: "storage_failure", Message: boundedVideoError(err.Error())}
	manager.publish(run.batchID, AttemptEvent{Type: "persistence_error", Attempt: current})
	return current, err
}

func (manager *Manager) attemptFrom(batchID, itemID, attemptID string) (Attempt, bool) {
	batch, ok := manager.service.Get(batchID)
	if !ok {
		return Attempt{}, false
	}
	itemPosition := itemIndex(batch.Items, itemID)
	if itemPosition < 0 {
		return Attempt{}, false
	}
	attemptPosition := attemptIndex(batch.Items[itemPosition].Attempts, attemptID)
	if attemptPosition < 0 {
		return Attempt{}, false
	}
	return cloneAttempt(batch.Items[itemPosition].Attempts[attemptPosition]), true
}

func waitForVideoPoll(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func boundedVideoError(message string) string {
	if len(message) <= 4096 {
		return message
	}
	message = message[:4096]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}

func (manager *Manager) bestEffortRemoteCancel(run *videoAttemptRun, jobID string) {
	if jobID == "" || run.preset.http == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), videoCancellationTimeout)
	defer cancel()
	_ = manager.remote.Cancel(ctx, *run.preset.http, jobID)
}

func cloneCLIPreset(source videoconfig.CLIPreset) videoconfig.CLIPreset {
	clone := source
	clone.Env = make(map[string]string, len(source.Env))
	for key, value := range source.Env {
		clone.Env[key] = value
	}
	clone.DefaultParams = append(json.RawMessage(nil), source.DefaultParams...)
	return clone
}

// buildCLISnapshot records only asset metadata. Workspace preparation verifies
// the retained bytes again immediately before launching the local command.
func (manager *Manager) buildCLISnapshot(batch Batch, item Item, preset videoconfig.CLIPreset) (Snapshot, error) {
	params, err := MergeParams(preset.DefaultParams, batch.CommonParams, item.ParamsOverride)
	if err != nil {
		return Snapshot{}, err
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return Snapshot{}, fmt.Errorf("encode CLI parameters: %w", err)
	}
	timing, err := ResolveTiming(batch.Timing, item.TimingOverride)
	if err != nil {
		return Snapshot{}, err
	}
	inputs, err := manager.snapshotCLIInputs(item)
	if err != nil {
		return Snapshot{}, err
	}
	clone := cloneCLIPreset(preset)
	return Snapshot{ExecutionKind: videoconfig.ExecutionLocalCLI, CLIPreset: &clone, Params: encoded, Prompt: item.Prompt,
		NegativePrompt: item.NegativePrompt, Timing: timing, InputAssets: inputs, CreatedAt: time.Now().UTC()}, nil
}

func (manager *Manager) snapshotCLIInputs(item Item) ([]AssetSnapshot, error) {
	type chosen struct{ id, role string }
	values := make([]chosen, 0, 2+len(item.ControlFrameIDs)+len(item.SelectedAssets))
	if item.InitImageID != "" {
		values = append(values, chosen{id: item.InitImageID, role: "init"})
	}
	if item.EndImageID != "" {
		values = append(values, chosen{id: item.EndImageID, role: "end"})
	}
	for _, id := range item.ControlFrameIDs {
		values = append(values, chosen{id: id, role: "control"})
	}
	selected := append([]SelectedAsset(nil), item.SelectedAssets...)
	sort.SliceStable(selected, func(left, right int) bool { return selected[left].Order < selected[right].Order })
	for _, selected := range selected {
		values = append(values, chosen{id: selected.AssetID, role: selected.Role})
	}
	inputs := make([]AssetSnapshot, 0, len(values))
	for order, selected := range values {
		stored, ok := manager.assets.Get(selected.id)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrVideoAssetNotFound, selected.id)
		}
		if stored.State != asset.StateActive && !containsReference(stored, asset.Reference{Module: videoItemReferenceModule, RecordID: item.ID}) {
			return nil, fmt.Errorf("%w: %s", ErrVideoAssetNotActive, selected.id)
		}
		inputs = append(inputs, AssetSnapshot{ID: stored.ID, SHA256: stored.SHA256, MediaType: stored.MediaType, DisplayName: stored.DisplayName, Size: stored.Size, Role: selected.role, Order: order})
	}
	return inputs, nil
}

func (manager *Manager) runCLI(run *videoAttemptRun) {
	run.lifecycle.Lock()
	current, ok := manager.GetAttempt(run.attemptID)
	if !ok || terminalAttemptState(current.State) {
		run.lifecycle.Unlock()
		return
	}
	workspace, prepareErr := manager.workspace.Prepare(run.attemptID, current.Snapshot)
	if prepareErr != nil {
		// AttemptRunning is the first state which permits a terminal failure.
		started := updateFor(current, AttemptRunning)
		if current, updateErr := manager.updateAttempt(run, started); updateErr == nil {
			input := updateFor(current, AttemptFailed)
			input.Error = AttemptError{Code: "workspace_prepare_failed", Message: boundedVideoError(prepareErr.Error())}
			_, _ = manager.completeAttempt(run, input)
		}
		run.lifecycle.Unlock()
		return
	}
	request, templateErr := manager.cliRequest(run, workspace, current.Snapshot)
	if templateErr != nil {
		started := updateFor(current, AttemptRunning)
		started.WorkspaceRelativePath = run.attemptID
		if current, updateErr := manager.updateAttempt(run, started); updateErr == nil {
			input := updateFor(current, AttemptFailed)
			input.Error = AttemptError{Code: "template_failed", Message: boundedVideoError(templateErr.Error())}
			_, _ = manager.completeAttempt(run, input)
		}
		run.lifecycle.Unlock()
		return
	}
	input := updateFor(current, AttemptRunning)
	input.WorkspaceRelativePath = run.attemptID
	updated, err := manager.updateAttempt(run, input)
	run.lifecycle.Unlock()
	if err != nil {
		return
	}
	current = updated
	resultCh := make(chan struct {
		result CLIRunResult
		err    error
	}, 1)
	go func() {
		result, err := manager.cli.Run(run.ctx, request)
		resultCh <- struct {
			result CLIRunResult
			err    error
		}{result: result, err: err}
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case outcome := <-resultCh:
			manager.finishCLIRun(run, current, outcome.result, outcome.err)
			return
		case <-ticker.C:
			status, err := manager.cli.Status(run.attemptID)
			if err != nil || status.PID <= 0 {
				continue
			}
			run.lifecycle.Lock()
			latest, ok := manager.GetAttempt(run.attemptID)
			if ok && activeAttemptState(latest.State) && latest.PID != status.PID {
				input := updateFor(latest, AttemptRunning)
				input.PID = status.PID
				_, _ = manager.updateAttempt(run, input)
			}
			run.lifecycle.Unlock()
		}
	}
}

func (manager *Manager) finishCLIRun(run *videoAttemptRun, before Attempt, result CLIRunResult, runErr error) {
	run.lifecycle.Lock()
	defer run.lifecycle.Unlock()
	current, ok := manager.GetAttempt(run.attemptID)
	if !ok || terminalAttemptState(current.State) {
		return
	}
	if result.PID > 0 && current.PID != result.PID {
		input := updateFor(current, AttemptRunning)
		input.PID = result.PID
		updated, err := manager.updateAttempt(run, input)
		if err != nil {
			return
		}
		current = updated
	}
	if runErr != nil || result.State != CLIStateSucceeded {
		state, code := AttemptFailed, "cli_failed"
		message := result.Error
		if message == "" && runErr != nil {
			message = runErr.Error()
		}
		if run.ctx.Err() != nil || result.State == CLIStateStopped {
			state, code = AttemptCancelled, "cancelled"
		} else if errors.Is(runErr, context.DeadlineExceeded) {
			code = "timeout"
		}
		input := updateFor(current, state)
		input.Error = AttemptError{Code: code, Message: boundedVideoError(message)}
		_, _ = manager.completeAttempt(run, input)
		return
	}
	updated, err := manager.importCLIResult(run, current, result)
	if err != nil {
		input := updateFor(current, AttemptFailed)
		input.Error = AttemptError{Code: "invalid_result", Message: boundedVideoError(err.Error())}
		_, _ = manager.completeAttempt(run, input)
		return
	}
	input := updateFor(updated, AttemptSucceeded)
	input.Error = AttemptError{}
	_, _ = manager.completeAttempt(run, input)
}

func (manager *Manager) importCLIResult(run *videoAttemptRun, current Attempt, result CLIRunResult) (Attempt, error) {
	request := result.Request
	if result.OutputPath == "" || request.WorkspaceRoot == "" || request.OutputPath == "" {
		return current, errors.New("CLI completed without its declared output")
	}
	if result.OutputPath != request.OutputPath {
		return current, errors.New("CLI completed with an output path different from its declared output")
	}
	relativeOutputPath, err := filepath.Rel(request.WorkspaceRoot, request.OutputPath)
	if err != nil || filepath.IsAbs(relativeOutputPath) || relativeOutputPath == "." || relativeOutputPath == ".." || strings.HasPrefix(relativeOutputPath, ".."+string(filepath.Separator)) || !strings.HasPrefix(relativeOutputPath, "outputs"+string(filepath.Separator)) {
		return current, errors.New("declared CLI output escaped its workspace")
	}
	file, err := openCLIOutputNoFollow(request.WorkspaceRoot, relativeOutputPath)
	if err != nil {
		return current, fmt.Errorf("open declared CLI output safely: %w", err)
	}
	defer file.Close()
	if _, err := validateCLIImportFile(file, request); err != nil {
		return current, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return current, fmt.Errorf("rewind declared CLI output: %w", err)
	}
	preset := run.preset.cli
	created, err := manager.assets.Import(asset.ImportInput{Reader: file,
		DisplayName: fmt.Sprintf("%s-%s-%s%s", run.batchID, run.itemID, current.ID, preset.OutputExtension),
		MediaType:   preset.OutputMediaType, Source: "videogen:" + current.ID,
	})
	if err != nil {
		return current, fmt.Errorf("import CLI result: %w", err)
	}
	updated, err := manager.service.AttachVideoResult(run.batchID, run.itemID, current.ID, created.ID)
	if err != nil {
		return current, fmt.Errorf("attach imported CLI result: %w", err)
	}
	manager.publish(run.batchID, AttemptEvent{Type: "state", Attempt: updated})
	return updated, nil
}

func validateCLIImportFile(file *os.File, request CLIRunRequest) (int64, error) {
	if file == nil || request.MaxOutputBytes < 1 || !strings.EqualFold(filepath.Ext(request.OutputPath), request.OutputExtension) {
		return 0, errors.New("declared CLI output limits or extension are invalid")
	}
	mediaType, _, err := mime.ParseMediaType(request.OutputMediaType)
	if err != nil {
		return 0, fmt.Errorf("parse declared CLI output MIME: %w", err)
	}
	wantExtension, magic := cliOutputFormat(strings.ToLower(mediaType))
	if wantExtension == "" || !strings.EqualFold(request.OutputExtension, wantExtension) {
		return 0, errors.New("declared CLI output MIME and extension do not match")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 0, errors.New("declared CLI output is not a regular file")
	}
	limited := &io.LimitedReader{R: file, N: request.MaxOutputBytes + 1}
	header := make([]byte, 12)
	read, readErr := io.ReadFull(limited, header)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return 0, fmt.Errorf("read declared CLI output: %w", readErr)
	}
	rest, err := io.Copy(io.Discard, limited)
	if err != nil {
		return 0, fmt.Errorf("read declared CLI output: %w", err)
	}
	total := int64(read) + rest
	if total == 0 || total > request.MaxOutputBytes {
		return 0, fmt.Errorf("declared CLI output size is invalid")
	}
	if !magic(header[:read]) {
		return 0, fmt.Errorf("declared CLI output magic does not match %q", mediaType)
	}
	return total, nil
}

func (manager *Manager) cliRequest(run *videoAttemptRun, workspace Workspace, snapshot Snapshot) (CLIRunRequest, error) {
	if run.preset.cli == nil {
		return CLIRunRequest{}, errors.New("missing CLI preset")
	}
	preset := run.preset.cli
	values, err := cliTemplateValues(run.attemptID, workspace, snapshot)
	if err != nil {
		return CLIRunRequest{}, err
	}
	raw, err := cliExtraArgsRaw(snapshot.Params)
	if err != nil {
		return CLIRunRequest{}, err
	}
	templateValues := TemplateVariables{Values: values, Raw: raw}
	prepare, err := ExpandCLITemplate(preset.PrepareCommandTemplate, templateValues)
	if err != nil {
		return CLIRunRequest{}, err
	}
	command, err := ExpandCLITemplate(preset.CommandTemplate, templateValues)
	if err != nil {
		return CLIRunRequest{}, err
	}
	workDir := preset.WorkDir
	if workDir == "" {
		workDir = workspace.Root
	}
	environment := cloneStringValues(preset.Env)
	for key, value := range values {
		environment[key] = value
	}
	for key, value := range raw {
		environment[key] = value
	}
	return CLIRunRequest{AttemptID: run.attemptID, PrepareCommand: prepare, Command: command, WorkspaceRoot: workspace.Root,
		WorkDir: workDir, Env: environment, Timeout: time.Duration(preset.TimeoutSeconds) * time.Second,
		StopGrace: time.Duration(preset.StopGraceSeconds) * time.Second, LogBufferBytes: preset.LogBufferBytes, OutputDir: workspace.OutputDir,
		OutputPath: workspace.OutputPath, OutputMediaType: preset.OutputMediaType, OutputExtension: preset.OutputExtension,
		MaxOutputBytes: preset.MaxOutputBytes}, nil
}

func cliTemplateValues(attemptID string, workspace Workspace, snapshot Snapshot) (map[string]string, error) {
	pathsByRole := make(map[string][]string)
	type selectedAssetPath struct {
		AssetID string `json:"asset_id"`
		Role    string `json:"role"`
		Order   int    `json:"order"`
		Path    string `json:"path"`
	}
	selected := make([]selectedAssetPath, 0)
	controls := make([]string, 0)
	for index, input := range snapshot.InputAssets {
		if index >= len(workspace.Inputs) {
			return nil, errors.New("workspace inputs do not match attempt snapshot")
		}
		path := workspace.Inputs[index].Path
		relative, err := filepath.Rel(workspace.Root, path)
		if err != nil || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, errors.New("workspace input path escaped its root")
		}
		pathsByRole[input.Role] = append(pathsByRole[input.Role], path)
		switch input.Role {
		case "control":
			controls = append(controls, path)
		case "init", "end":
		default:
			selected = append(selected, selectedAssetPath{AssetID: input.ID, Role: input.Role, Order: input.Order, Path: path})
		}
	}
	controlJSON, err := json.Marshal(controls)
	if err != nil {
		return nil, err
	}
	selectedJSON, err := json.Marshal(selected)
	if err != nil {
		return nil, err
	}
	seed := ""
	var params map[string]any
	if err := json.Unmarshal(snapshot.Params, &params); err == nil {
		if value, exists := params["seed"]; exists {
			seed = fmt.Sprint(value)
		}
	}
	first := func(role string) string {
		if values := pathsByRole[role]; len(values) > 0 {
			return values[0]
		}
		return ""
	}
	return map[string]string{
		"ATTEMPT_ID": attemptID, "WORKSPACE_DIR": workspace.Root, "INPUT_DIR": workspace.InputDir, "OUTPUT_DIR": workspace.OutputDir,
		"OUTPUT_PATH": workspace.OutputPath, "PROMPT": snapshot.Prompt, "NEGATIVE_PROMPT": snapshot.NegativePrompt, "SEED": seed,
		"FPS": strconv.Itoa(snapshot.Timing.FPS), "VIDEO_FRAMES": strconv.Itoa(snapshot.Timing.RequestedFrames),
		"DURATION_SECONDS": strconv.FormatFloat(snapshot.Timing.DurationSeconds, 'f', -1, 64), "INIT_IMAGE": first("init"),
		"END_IMAGE": first("end"), "CONTROL_FRAMES_JSON": string(controlJSON), "SELECTED_ASSETS_JSON": string(selectedJSON),
		"MANIFEST_PATH": workspace.ManifestPath,
	}, nil
}

func cliExtraArgsRaw(params json.RawMessage) (map[string]string, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(params, &values); err != nil {
		return nil, fmt.Errorf("decode CLI parameters for raw arguments: %w", err)
	}
	raw, exists := values["extra_args_raw"]
	if !exists {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New("extra_args_raw must be a JSON string")
	}
	return map[string]string{"EXTRA_ARGS_RAW": value}, nil
}

func cloneStringValues(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func (manager *Manager) cancelQueued(run *videoAttemptRun, cause error) {
	run.lifecycle.Lock()
	defer run.lifecycle.Unlock()
	manager.cancelQueuedLocked(run, cause)
}

func (manager *Manager) cancelQueuedLocked(run *videoAttemptRun, cause error) error {
	current, ok := manager.GetAttempt(run.attemptID)
	if !ok {
		return ErrAttemptNotFound
	}
	if terminalAttemptState(current.State) {
		return nil
	}
	if current.State != AttemptQueued {
		return ErrInvalidAttemptTransition
	}
	input := updateFor(current, AttemptCancelled)
	message := "video attempt cancelled while queued"
	if cause != nil {
		message = boundedVideoError(cause.Error())
	}
	input.Error = AttemptError{Code: "cancelled", Message: message}
	_, err := manager.completeAttempt(run, input)
	return err
}

func (manager *Manager) finishRun(run *videoAttemptRun) {
	attempt, ok := manager.GetAttempt(run.attemptID)
	manager.mu.RLock()
	_, hasPendingTerminal := manager.pendingTerminal[run.attemptID]
	manager.mu.RUnlock()
	if ok && activeAttemptState(attempt.State) && !hasPendingTerminal {
		return
	}
	manager.mu.Lock()
	if manager.attempts[run.attemptID] == run {
		delete(manager.attempts, run.attemptID)
	}
	manager.mu.Unlock()
}

func (manager *Manager) publish(batchID string, event AttemptEvent) {
	event.BatchID = batchID
	event.Attempt = cloneAttempt(event.Attempt)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, stream := range manager.subscribers[batchID] {
		select {
		case stream <- event:
		default:
			select {
			case <-stream:
			default:
			}
			select {
			case stream <- event:
			default:
			}
		}
	}
}

func (manager *Manager) Cancel(attemptID string) error {
	return manager.CancelContext(context.Background(), attemptID)
}

// CancelContext asks the active executor to cancel attemptID. Its remote
// request is bounded independently of the provider's ordinary connection and
// job limits, and it stops promptly when ctx is cancelled.
func (manager *Manager) CancelContext(ctx context.Context, attemptID string) error {
	return manager.cancelWithContext(ctx, attemptID)
}

func (manager *Manager) cancelWithContext(ctx context.Context, attemptID string) error {
	if ctx == nil {
		return errors.New("video cancellation context is nil")
	}
	cancelCtx, cancel := context.WithTimeout(ctx, videoCancellationTimeout)
	defer cancel()
	manager.mu.RLock()
	run := manager.attempts[attemptID]
	manager.mu.RUnlock()
	current, ok := manager.GetAttempt(attemptID)
	if !ok {
		return ErrAttemptNotFound
	}
	if terminalAttemptState(current.State) {
		return nil
	}
	if run == nil {
		return ErrAttemptNotFound
	}
	if err := lockVideoLifecycle(cancelCtx, &run.lifecycle); err != nil {
		return err
	}
	defer run.lifecycle.Unlock()
	current, ok = manager.GetAttempt(attemptID)
	if !ok {
		return ErrAttemptNotFound
	}
	if terminalAttemptState(current.State) {
		return nil
	}
	if current.State == AttemptQueued {
		run.cancel()
		return manager.cancelQueuedLocked(run, nil)
	}
	switch run.preset.kind {
	case videoconfig.ExecutionHTTP:
		if current.RemoteJobID == "" {
			run.cancel()
			input := updateFor(current, AttemptCancelled)
			input.Error = AttemptError{Code: "cancelled", Message: "video attempt cancelled by user"}
			_, err := manager.completeAttempt(run, input)
			return err
		}
		err := manager.remote.Cancel(cancelCtx, *run.preset.http, current.RemoteJobID)
		var httpError *sdcpp.HTTPError
		if errors.As(err, &httpError) && httpError.StatusCode == 409 {
			input := updateFor(current, AttemptPolling)
			input.RemoteStatus = "generating"
			input.Error = AttemptError{Code: "remote_cannot_cancel", Message: "remote generation is continuing; this server does not support mid-generation cancellation"}
			_, updateErr := manager.updateAttempt(run, input)
			return updateErr
		}
		if err != nil {
			// Cancellation is a request to the remote service. A transport
			// failure says nothing about whether the remote job is still running,
			// so retain the persisted polling state and let it continue.
			return err
		}
		run.cancel()
		input := updateFor(current, AttemptCancelled)
		input.RemoteStatus = "cancelled"
		input.Error = AttemptError{Code: "cancelled", Message: "video attempt cancelled by user"}
		_, updateErr := manager.completeAttempt(run, input)
		return updateErr
	case videoconfig.ExecutionLocalCLI:
		run.cancel()
		stopCtx, cancel := context.WithTimeout(cancelCtx, time.Second)
		stopErr := manager.cli.Stop(stopCtx, current.ID)
		cancel()
		if stopErr != nil && !errors.Is(stopErr, ErrCLIAttemptNotRunning) && !errors.Is(stopErr, ErrCLIAttemptNotFound) {
			return stopErr
		}
		input := updateFor(current, AttemptCancelled)
		input.Error = AttemptError{Code: "cancelled", Message: "video attempt cancelled by user"}
		_, updateErr := manager.completeAttempt(run, input)
		return updateErr
	default:
		return fmt.Errorf("unsupported video execution kind %q", run.preset.kind)
	}
}

// lockVideoLifecycle acquires a run lifecycle mutex without letting a
// disconnected client wait unboundedly behind completion/import work.
func lockVideoLifecycle(ctx context.Context, lifecycle *sync.Mutex) error {
	for {
		if lifecycle.TryLock() {
			return nil
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (manager *Manager) SubscribeBatch(batchID string) (<-chan AttemptEvent, func(), error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.accepting {
		return nil, nil, ErrVideoManagerClosed
	}
	batch, ok := manager.service.Get(batchID)
	if !ok {
		return nil, nil, ErrBatchNotFound
	}
	capacity := len(batch.Items) + 16
	if capacity < 64 {
		capacity = 64
	}
	stream := make(chan AttemptEvent, capacity)
	manager.nextSubID++
	subscriberID := manager.nextSubID
	if manager.subscribers[batchID] == nil {
		manager.subscribers[batchID] = make(map[uint64]chan AttemptEvent)
	}
	manager.subscribers[batchID][subscriberID] = stream
	initial := make([]Attempt, 0, len(batch.Items))
	for _, item := range batch.Items {
		if len(item.Attempts) == 0 {
			continue
		}
		initial = append(initial, manager.decoratePendingTerminalLocked(cloneAttempt(item.Attempts[len(item.Attempts)-1])))
	}
	snapshot := AttemptEvent{Type: "snapshot", BatchID: batchID, Attempts: cloneAttemptEvents(initial)}
	if len(initial) > 0 {
		snapshot.Attempt = cloneAttempt(initial[0])
	}
	stream <- snapshot
	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			manager.mu.Lock()
			defer manager.mu.Unlock()
			subscribers := manager.subscribers[batchID]
			if subscribers == nil {
				return
			}
			if existing, exists := subscribers[subscriberID]; exists {
				delete(subscribers, subscriberID)
				close(existing)
			}
			if len(subscribers) == 0 {
				delete(manager.subscribers, batchID)
			}
		})
	}
	return stream, unsubscribe, nil
}

func cloneAttemptEvents(source []Attempt) []Attempt {
	clone := make([]Attempt, len(source))
	for index := range source {
		clone[index] = cloneAttempt(source[index])
	}
	return clone
}

// SubscribeCLILog preserves the executor's raw byte offsets for SSE callers;
// browser-side clearing remains a display-only concern.
func (manager *Manager) SubscribeCLILog(attemptID string) (VideoLogSnapshot, <-chan VideoLogChunk, func(), error) {
	attempt, ok := manager.GetAttempt(attemptID)
	if !ok {
		return VideoLogSnapshot{}, nil, nil, ErrAttemptNotFound
	}
	if attempt.ExecutionKind != videoconfig.ExecutionLocalCLI {
		return VideoLogSnapshot{}, nil, nil, fmt.Errorf("video attempt is not a local CLI attempt")
	}
	snapshot, chunks, cancel, err := manager.cli.SubscribeLog(attemptID)
	if err == nil || !errors.Is(err, ErrCLIAttemptNotFound) || terminalAttemptState(attempt.State) {
		return snapshot, chunks, cancel, err
	}
	capacity := 1
	if attempt.Snapshot.CLIPreset != nil && attempt.Snapshot.CLIPreset.LogBufferBytes > 0 {
		capacity = attempt.Snapshot.CLIPreset.LogBufferBytes
	}
	pendingContext, pendingCancel := context.WithCancel(context.Background())
	pendingChunks := make(chan VideoLogChunk, videoLogSubscriberCapacity)
	go manager.forwardPendingCLILog(pendingContext, attemptID, pendingChunks)
	var once sync.Once
	return VideoLogSnapshot{CapacityBytes: capacity}, pendingChunks, func() { once.Do(pendingCancel) }, nil
}

func (manager *Manager) forwardPendingCLILog(ctx context.Context, attemptID string, destination chan<- VideoLogChunk) {
	defer close(destination)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, chunks, cancel, err := manager.cli.SubscribeLog(attemptID)
		if err == nil {
			defer cancel()
			if len(snapshot.Data) > 0 {
				select {
				case destination <- VideoLogChunk{Offset: snapshot.StartOffset, Data: snapshot.Data}:
				case <-ctx.Done():
					return
				case <-manager.done:
					return
				}
			}
			for {
				select {
				case chunk, open := <-chunks:
					if !open {
						return
					}
					select {
					case destination <- chunk:
					case <-ctx.Done():
						return
					case <-manager.done:
						return
					}
				case <-ctx.Done():
					return
				case <-manager.done:
					return
				}
			}
		}
		attempt, exists := manager.GetAttempt(attemptID)
		if !errors.Is(err, ErrCLIAttemptNotFound) || !exists || terminalAttemptState(attempt.State) {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		case <-manager.done:
			return
		}
	}
}

func (manager *Manager) SaveCLILog(attemptID string) (string, error) {
	attempt, ok := manager.GetAttempt(attemptID)
	if !ok {
		return "", ErrAttemptNotFound
	}
	if attempt.ExecutionKind != videoconfig.ExecutionLocalCLI {
		return "", fmt.Errorf("video attempt is not a local CLI attempt")
	}
	if manager.workspace == nil || strings.TrimSpace(manager.workspace.root) == "" {
		return "", errors.New("video workspace root is unavailable")
	}
	logRoot := filepath.Join(filepath.Dir(filepath.Clean(manager.workspace.root)), "video-logs")
	if err := os.MkdirAll(logRoot, 0o700); err != nil {
		return "", fmt.Errorf("create video log directory: %w", err)
	}
	if err := os.Chmod(logRoot, 0o700); err != nil {
		return "", fmt.Errorf("protect video log directory: %w", err)
	}
	return manager.cli.SaveLog(attemptID, filepath.Join(logRoot, attemptID+".log"))
}

func (manager *Manager) CleanupWorkspace(attemptID string) error {
	attempt, ok := manager.GetAttempt(attemptID)
	if !ok {
		return ErrAttemptNotFound
	}
	if attempt.ExecutionKind != videoconfig.ExecutionLocalCLI {
		return fmt.Errorf("video attempt has no CLI workspace")
	}
	if !terminalAttemptState(attempt.State) {
		return ErrActiveAttempt
	}
	return manager.workspace.Cleanup(attemptID)
}

func (manager *Manager) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("video manager shutdown context is nil")
	}
	manager.shutdownOnce.Do(func() {
		manager.mu.Lock()
		manager.accepting = false
		for batchID, subscribers := range manager.subscribers {
			for subscriberID, stream := range subscribers {
				delete(subscribers, subscriberID)
				close(stream)
			}
			delete(manager.subscribers, batchID)
		}
		manager.mu.Unlock()
		go manager.shutdownBackground(ctx)
	})
	select {
	case <-manager.shutdownComplete:
		manager.mu.RLock()
		err := manager.shutdownErr
		manager.mu.RUnlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) shutdownBackground(shutdownCtx context.Context) {
	manager.starts.Wait()
	manager.mu.RLock()
	runs := make([]*videoAttemptRun, 0, len(manager.attempts))
	for _, run := range manager.attempts {
		runs = append(runs, run)
	}
	manager.mu.RUnlock()
	graceCtx, cancelGrace := context.WithTimeout(shutdownCtx, time.Second)
	defer cancelGrace()
	cancelResults := make(chan error, len(runs))
	for _, run := range runs {
		go func(run *videoAttemptRun) {
			err := manager.cancelWithContext(graceCtx, run.attemptID)
			if err != nil && !errors.Is(err, ErrAttemptNotFound) {
				err = fmt.Errorf("cancel video attempt %s: %w", run.attemptID, err)
			}
			cancelResults <- err
		}(run)
	}
	var shutdownErrors []error
	for range runs {
		select {
		case err := <-cancelResults:
			if err != nil {
				shutdownErrors = append(shutdownErrors, err)
			}
		case <-graceCtx.Done():
			shutdownErrors = append(shutdownErrors, graceCtx.Err())
			goto cancellationsDone
		}
	}

cancellationsDone:
	// Remote 409 and transport errors intentionally leave durable attempts
	// active. Stopping local contexts ends polling without falsely recording a
	// remote terminal state; Repository.Open will mark them interrupted later.
	for _, run := range runs {
		run.cancel()
	}
	if manager.cli != nil {
		cliCtx, cancelCLI := context.WithTimeout(context.Background(), 5*time.Second)
		if err := manager.cli.Shutdown(cliCtx); err != nil && !errors.Is(err, ErrCLIExecutorShutdown) {
			shutdownErrors = append(shutdownErrors, err)
		}
		cancelCLI()
	}
	manager.wg.Wait()
	manager.doneOnce.Do(func() { close(manager.done) })
	manager.mu.Lock()
	manager.shutdownErr = errors.Join(shutdownErrors...)
	manager.mu.Unlock()
	close(manager.shutdownComplete)
}
