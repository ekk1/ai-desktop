package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
)

var (
	ErrImageProviderNotFound = errors.New("image provider not found")
	ErrImageProviderDisabled = errors.New("image provider is disabled")
	ErrImageManagerClosed    = errors.New("image manager is shutting down")
	ErrImageResultLimit      = errors.New("image result exceeds provider byte limit")
)

type RemoteClient interface {
	Submit(context.Context, sdcpp.ImageProvider, []byte) (sdcpp.Submission, error)
	Job(context.Context, sdcpp.ImageProvider, string) (sdcpp.Job, error)
	Cancel(context.Context, sdcpp.ImageProvider, string) error
}

type AttemptEvent struct {
	Type    string  `json:"type"`
	Attempt Attempt `json:"attempt"`
}

type attemptRun struct {
	lifecycle sync.Mutex
	attemptID string
	batchID   string
	itemID    string
	provider  sdcpp.ImageProvider
	prepared  PreparedRequest
	ctx       context.Context
	cancel    context.CancelFunc
}

type scheduledAttempt struct {
	ctx         context.Context
	attempt     Attempt
	run         *attemptRun
	providerSem *limiter
	batchSem    *limiter
}

type batchQueue struct {
	jobs chan scheduledAttempt
}

// limiter is a resizable counting semaphore. Active holders remain counted when
// the configured limit changes, so updating a provider or batch cannot briefly
// exceed its new concurrency limit.
type limiter struct {
	mu      sync.Mutex
	limit   int
	used    int
	changed chan struct{}
}

func newLimiter(limit int) *limiter {
	return &limiter{limit: limit, changed: make(chan struct{})}
}

func (limiter *limiter) setLimit(limit int) {
	limiter.mu.Lock()
	if limiter.limit != limit {
		limiter.limit = limit
		close(limiter.changed)
		limiter.changed = make(chan struct{})
	}
	limiter.mu.Unlock()
}

func (limiter *limiter) acquire(ctx context.Context) bool {
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

func (limiter *limiter) release() {
	limiter.mu.Lock()
	if limiter.used > 0 {
		limiter.used--
	}
	close(limiter.changed)
	limiter.changed = make(chan struct{})
	limiter.mu.Unlock()
}

type Manager struct {
	mu             sync.RWMutex
	config         *config.Repository
	service        *Service
	assembler      *Assembler
	assets         *asset.Repository
	remote         RemoteClient
	persistAttempt func(string, string, string, UpdateAttemptInput) (Attempt, error)
	attachResult   func(string, string, string, string) (Attempt, error)
	accepting      bool
	attempts       map[string]*attemptRun
	providerSem    map[string]*limiter
	batchSem       map[string]*limiter
	batchQueues    map[string]*batchQueue
	subscribers    map[string]map[uint64]chan AttemptEvent
	nextSubscriber uint64
	done           chan struct{}
	doneOnce       sync.Once
	starts         sync.WaitGroup
	wg             sync.WaitGroup
}

func NewManager(configuration *config.Repository, service *Service, assembler *Assembler, assets *asset.Repository, remote RemoteClient) *Manager {
	manager := &Manager{
		config: configuration, service: service, assembler: assembler, assets: assets, remote: remote,
		accepting: true, attempts: make(map[string]*attemptRun),
		providerSem: make(map[string]*limiter), batchSem: make(map[string]*limiter), batchQueues: make(map[string]*batchQueue),
		subscribers: make(map[string]map[uint64]chan AttemptEvent), done: make(chan struct{}),
	}
	manager.persistAttempt = service.UpdateAttempt
	manager.attachResult = service.AttachResult
	return manager
}

func (manager *Manager) StartBatch(batchID string) ([]Attempt, error) {
	if err := manager.beginStart(); err != nil {
		return nil, err
	}
	defer manager.starts.Done()
	batch, ok := manager.service.Get(batchID)
	if !ok {
		return nil, ErrBatchNotFound
	}
	provider, err := manager.provider(batch.ProviderID)
	if err != nil {
		return nil, err
	}
	attempts := make([]Attempt, 0, len(batch.Items))
	for _, item := range batch.Items {
		if itemHasActiveAttempt(item) {
			continue
		}
		attempt, err := manager.start(batch, item, provider)
		if err != nil {
			if attempt.ID != "" {
				attempts = append(attempts, attempt)
				continue
			}
			return attempts, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, nil
}

func (manager *Manager) StartItem(batchID, itemID string) (Attempt, error) {
	if err := manager.beginStart(); err != nil {
		return Attempt{}, err
	}
	defer manager.starts.Done()
	batch, ok := manager.service.Get(batchID)
	if !ok {
		return Attempt{}, ErrBatchNotFound
	}
	position := itemIndex(batch.Items, itemID)
	if position < 0 {
		return Attempt{}, ErrItemNotFound
	}
	item := batch.Items[position]
	if itemHasActiveAttempt(item) {
		return Attempt{}, ErrActiveAttempt
	}
	provider, err := manager.provider(batch.ProviderID)
	if err != nil {
		return Attempt{}, err
	}
	return manager.start(batch, item, provider)
}

func (manager *Manager) GetAttempt(attemptID string) (Attempt, bool) {
	manager.mu.RLock()
	run, ok := manager.attempts[attemptID]
	manager.mu.RUnlock()
	if !ok {
		for _, batch := range manager.service.List(Filter{}) {
			for _, item := range batch.Items {
				for _, attempt := range item.Attempts {
					if attempt.ID == attemptID {
						return cloneAttempt(attempt), true
					}
				}
			}
		}
		return Attempt{}, false
	}
	batch, ok := manager.service.Get(run.batchID)
	if !ok {
		return Attempt{}, false
	}
	itemPosition := itemIndex(batch.Items, run.itemID)
	if itemPosition < 0 {
		return Attempt{}, false
	}
	attemptPosition := attemptIndex(batch.Items[itemPosition].Attempts, attemptID)
	if attemptPosition < 0 {
		return Attempt{}, false
	}
	return cloneAttempt(batch.Items[itemPosition].Attempts[attemptPosition]), true
}

func (manager *Manager) Cancel(attemptID string) error {
	manager.mu.RLock()
	run, managed := manager.attempts[attemptID]
	manager.mu.RUnlock()
	attempt, ok := manager.GetAttempt(attemptID)
	if !ok {
		return ErrAttemptNotFound
	}
	if terminalAttemptState(attempt.State) {
		return nil
	}
	if !managed {
		return ErrAttemptNotFound
	}
	// Interrupt polling or result import before waiting for the lifecycle lock.
	// The cancellation path below owns the final persisted state.
	run.cancel()
	run.lifecycle.Lock()
	defer run.lifecycle.Unlock()
	attempt, ok = manager.GetAttempt(attemptID)
	if !ok {
		return ErrAttemptNotFound
	}
	if terminalAttemptState(attempt.State) {
		return nil
	}
	if attempt.RemoteJobID == "" {
		_, updateErr := manager.persistCommandTerminal(run, UpdateAttemptInput{
			State: AttemptCancelled, RemoteJobID: attempt.RemoteJobID, RemoteStatus: attempt.RemoteStatus,
			QueuePosition: attempt.QueuePosition, ResultAssetIDs: attempt.ResultAssetIDs,
			Error: AttemptError{Code: "cancelled", Message: "image attempt cancelled by user"},
		})
		return updateErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(run.provider.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	err := manager.remote.Cancel(ctx, run.provider, attempt.RemoteJobID)
	if err == nil {
		_, updateErr := manager.persistCommandTerminal(run, UpdateAttemptInput{
			State: AttemptCancelled, RemoteJobID: attempt.RemoteJobID, RemoteStatus: "cancelled",
			QueuePosition: attempt.QueuePosition, ResultAssetIDs: attempt.ResultAssetIDs,
			Error: AttemptError{Code: "cancelled", Message: "image attempt cancelled by user"},
		})
		return updateErr
	}
	var httpError *sdcpp.HTTPError
	if errors.As(err, &httpError) {
		switch httpError.StatusCode {
		case 404, 409, 410:
			return manager.recoverAfterCancel(ctx, run, attempt, err)
		}
	}
	_, updateErr := manager.persistCommandTerminal(run, UpdateAttemptInput{
		State: AttemptFailed, RemoteJobID: attempt.RemoteJobID, RemoteStatus: attempt.RemoteStatus,
		QueuePosition: attempt.QueuePosition, ResultAssetIDs: attempt.ResultAssetIDs,
		Error: AttemptError{Code: "cancel_failed", Message: boundedErrorMessage(err.Error())},
	})
	return errors.Join(err, updateErr)
}

func (manager *Manager) recoverAfterCancel(ctx context.Context, run *attemptRun, before Attempt, cancelErr error) error {
	defer run.cancel()
	job, jobErr := manager.remote.Job(ctx, run.provider, before.RemoteJobID)
	current, ok := manager.GetAttempt(before.ID)
	if !ok {
		return ErrAttemptNotFound
	}
	if terminalAttemptState(current.State) {
		return nil
	}
	if jobErr == nil && job.Status == "completed" {
		current, importErr := manager.importResults(ctx, run, current, job)
		if importErr == nil {
			_, updateErr := manager.persistCommandTerminal(run, UpdateAttemptInput{
				State: AttemptSucceeded, RemoteJobID: before.RemoteJobID, RemoteStatus: job.Status,
				QueuePosition: job.QueuePosition, ResultAssetIDs: current.ResultAssetIDs,
			})
			return updateErr
		}
		jobErr = importErr
	}
	message := cancelErr.Error()
	if jobErr != nil {
		message += "; recovery read: " + jobErr.Error()
	} else {
		message += "; recovery status: " + job.Status
	}
	_, updateErr := manager.persistCommandTerminal(run, UpdateAttemptInput{
		State: AttemptFailed, RemoteJobID: before.RemoteJobID, RemoteStatus: job.Status,
		QueuePosition: job.QueuePosition, ResultAssetIDs: current.ResultAssetIDs,
		Error: AttemptError{Code: "cancel_recovery_failed", Message: boundedErrorMessage(message)},
	})
	return updateErr
}

func (manager *Manager) SubscribeBatch(batchID string) (<-chan AttemptEvent, func(), error) {
	if _, ok := manager.service.Get(batchID); !ok {
		return nil, nil, ErrBatchNotFound
	}
	manager.mu.Lock()
	batch, ok := manager.service.Get(batchID)
	if !ok {
		manager.mu.Unlock()
		return nil, nil, ErrBatchNotFound
	}
	capacity := len(batch.Items) + 16
	if capacity < 64 {
		capacity = 64
	}
	stream := make(chan AttemptEvent, capacity)
	manager.nextSubscriber++
	subscriberID := manager.nextSubscriber
	if manager.subscribers[batchID] == nil {
		manager.subscribers[batchID] = make(map[uint64]chan AttemptEvent)
	}
	manager.subscribers[batchID][subscriberID] = stream
	for _, item := range batch.Items {
		if len(item.Attempts) == 0 {
			continue
		}
		stream <- AttemptEvent{Type: "snapshot", Attempt: cloneAttempt(item.Attempts[len(item.Attempts)-1])}
	}
	manager.mu.Unlock()
	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			manager.mu.Lock()
			if subscribers := manager.subscribers[batchID]; subscribers != nil {
				if existing, exists := subscribers[subscriberID]; exists {
					delete(subscribers, subscriberID)
					close(existing)
				}
				if len(subscribers) == 0 {
					delete(manager.subscribers, batchID)
				}
			}
			manager.mu.Unlock()
		})
	}
	return stream, unsubscribe, nil
}

func (manager *Manager) Shutdown(ctx context.Context) error {
	manager.mu.Lock()
	manager.accepting = false
	manager.mu.Unlock()
	startsFinished := make(chan struct{})
	go func() {
		manager.starts.Wait()
		close(startsFinished)
	}()
	select {
	case <-startsFinished:
	case <-ctx.Done():
		return ctx.Err()
	}
	manager.mu.Lock()
	runs := make([]*attemptRun, 0, len(manager.attempts))
	for _, run := range manager.attempts {
		runs = append(runs, run)
	}
	manager.mu.Unlock()
	for _, run := range runs {
		run.cancel()
	}
	var cancelWait sync.WaitGroup
	persistErrors := make(chan error, len(runs))
	for _, run := range runs {
		cancelWait.Add(1)
		go func(run *attemptRun) {
			defer cancelWait.Done()
			run.lifecycle.Lock()
			defer run.lifecycle.Unlock()
			attempt, ok := manager.GetAttempt(run.attemptID)
			if !ok || !activeAttemptState(attempt.State) {
				return
			}
			if attempt.RemoteJobID != "" {
				_ = manager.remote.Cancel(ctx, run.provider, attempt.RemoteJobID)
			}
			_, err := manager.persistTerminalContext(ctx, run, UpdateAttemptInput{
				State: AttemptCancelled, RemoteJobID: attempt.RemoteJobID, RemoteStatus: attempt.RemoteStatus,
				QueuePosition: attempt.QueuePosition, ResultAssetIDs: attempt.ResultAssetIDs,
				Error: AttemptError{Code: "shutdown", Message: "image manager is shutting down"},
			})
			if err != nil {
				persistErrors <- fmt.Errorf("persist shutdown state for attempt %s: %w", attempt.ID, err)
			}
		}(run)
	}
	finished := make(chan struct{})
	go func() {
		manager.wg.Wait()
		cancelWait.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		manager.doneOnce.Do(func() { close(manager.done) })
		close(persistErrors)
		var failures []error
		for err := range persistErrors {
			failures = append(failures, err)
		}
		return errors.Join(failures...)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) start(batch Batch, item Item, provider sdcpp.ImageProvider) (Attempt, error) {
	prepared, snapshot, err := manager.assembler.Build(batch, item, provider)
	if err != nil {
		failed, persistErr := manager.service.createFailedAttempt(batch.ID, item.ID, manager.preflightSnapshot(batch, item, provider), AttemptError{
			Code: "preflight_failed", Message: boundedErrorMessage(err.Error()),
		})
		if persistErr != nil {
			return Attempt{}, errors.Join(err, fmt.Errorf("persist failed image preflight: %w", persistErr))
		}
		manager.publish(batch.ID, AttemptEvent{Type: "state", Attempt: failed})
		return failed, err
	}
	attempt, err := manager.service.CreateAttempt(batch.ID, item.ID, CreateAttemptInput{State: AttemptQueued, Snapshot: snapshot})
	if err != nil {
		return Attempt{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &attemptRun{attemptID: attempt.ID, batchID: batch.ID, itemID: item.ID, provider: provider, prepared: prepared, ctx: ctx, cancel: cancel}
	manager.mu.Lock()
	manager.attempts[attempt.ID] = run
	providerSemaphore := manager.providerSemaphore(provider)
	batchSemaphore := manager.batchSemaphore(batch)
	queue := manager.batchQueue(batch)
	manager.wg.Add(1)
	manager.mu.Unlock()
	manager.publish(batch.ID, AttemptEvent{Type: "state", Attempt: attempt})
	queue.jobs <- scheduledAttempt{ctx: ctx, attempt: attempt, run: run, providerSem: providerSemaphore, batchSem: batchSemaphore}
	return attempt, nil
}

func (manager *Manager) preflightSnapshot(batch Batch, item Item, provider sdcpp.ImageProvider) Snapshot {
	params := json.RawMessage(`{}`)
	if merged, err := sdcpp.MergeImageParams(batch.BaseParams, item.ParamsOverride); err == nil {
		if encoded, err := json.Marshal(merged); err == nil {
			params = encoded
		}
	}
	provider.Headers = redactProviderHeaders(provider.Headers)
	assets := make([]AssetSnapshot, 0)
	for _, id := range inputAssetIDs(item.InputAssets) {
		stored, ok := manager.assets.Get(id)
		if !ok || !strings.HasPrefix(strings.ToLower(stored.MediaType), "image/") {
			continue
		}
		assets = append(assets, AssetSnapshot{
			ID: stored.ID, SHA256: stored.SHA256, MediaType: stored.MediaType, DisplayName: stored.DisplayName,
			Size: stored.Size, Width: stored.Width, Height: stored.Height,
		})
	}
	return Snapshot{Provider: provider, Params: params, Prompt: item.Prompt, NegativePrompt: item.NegativePrompt, InputAssets: assets}
}

func (manager *Manager) dispatch(queue *batchQueue) {
	for {
		select {
		case scheduled := <-queue.jobs:
			if !scheduled.batchSem.acquire(scheduled.ctx) {
				manager.cancelQueued(scheduled.attempt, scheduled.run, scheduled.ctx.Err())
				manager.finishRun(scheduled.run)
				manager.wg.Done()
				continue
			}
			if !scheduled.providerSem.acquire(scheduled.ctx) {
				scheduled.batchSem.release()
				manager.cancelQueued(scheduled.attempt, scheduled.run, scheduled.ctx.Err())
				manager.finishRun(scheduled.run)
				manager.wg.Done()
				continue
			}
			go manager.run(scheduled.ctx, scheduled.attempt, scheduled.run, scheduled.batchSem, scheduled.providerSem)
		case <-manager.done:
			return
		}
	}
}

func (manager *Manager) run(ctx context.Context, attempt Attempt, run *attemptRun, batchSemaphore, providerSemaphore *limiter) {
	defer manager.wg.Done()
	defer manager.finishRun(run)
	defer batchSemaphore.release()
	defer providerSemaphore.release()
	jobCtx, cancel := context.WithTimeout(ctx, run.prepared.JobTimeout)
	defer cancel()

	run.lifecycle.Lock()
	current, ok := manager.GetAttempt(attempt.ID)
	if !ok || terminalAttemptState(current.State) {
		run.lifecycle.Unlock()
		return
	}
	current, err := manager.updateAttempt(run.batchID, run.itemID, attempt.ID, UpdateAttemptInput{State: AttemptSubmitting})
	if err != nil {
		_, _ = manager.persistTerminal(run, UpdateAttemptInput{
			State: AttemptCancelled, Error: AttemptError{Code: "storage_failure", Message: boundedErrorMessage(err.Error())},
		})
		run.lifecycle.Unlock()
		return
	}
	run.lifecycle.Unlock()

	submission, err := manager.remote.Submit(jobCtx, run.provider, run.prepared.Body)
	run.lifecycle.Lock()
	current, ok = manager.GetAttempt(attempt.ID)
	if !ok || terminalAttemptState(current.State) {
		run.lifecycle.Unlock()
		if err == nil && submission.ID != "" {
			manager.bestEffortRemoteCancel(run, submission.ID)
		}
		return
	}
	if err != nil {
		if run.ctx.Err() == nil {
			manager.finishFromError(current, run, jobCtx, err)
		}
		run.lifecycle.Unlock()
		return
	}
	updated, err := manager.updateAttempt(run.batchID, run.itemID, attempt.ID, UpdateAttemptInput{
		State: AttemptPolling, RemoteJobID: submission.ID, RemoteStatus: submission.Status,
	})
	if err != nil {
		manager.bestEffortRemoteCancel(run, submission.ID)
		_, _ = manager.persistTerminal(run, UpdateAttemptInput{
			State: AttemptFailed, RemoteJobID: submission.ID, RemoteStatus: submission.Status,
			Error: AttemptError{Code: "storage_failure", Message: boundedErrorMessage(err.Error())},
		})
		run.lifecycle.Unlock()
		return
	}
	current = updated
	run.lifecycle.Unlock()

	for {
		job, err := manager.remote.Job(jobCtx, run.provider, submission.ID)
		run.lifecycle.Lock()
		current, ok = manager.GetAttempt(attempt.ID)
		if !ok || terminalAttemptState(current.State) {
			run.lifecycle.Unlock()
			return
		}
		if err != nil {
			if run.ctx.Err() == nil {
				manager.finishFromError(current, run, jobCtx, err)
			}
			run.lifecycle.Unlock()
			return
		}
		if jobCtx.Err() != nil {
			if run.ctx.Err() == nil {
				manager.finishFromError(current, run, jobCtx, jobCtx.Err())
			}
			run.lifecycle.Unlock()
			return
		}
		switch job.Status {
		case "queued", "generating":
			if current.RemoteStatus != job.Status || current.QueuePosition != job.QueuePosition {
				updated, updateErr := manager.updateAttempt(run.batchID, run.itemID, attempt.ID, UpdateAttemptInput{
					State: AttemptPolling, RemoteJobID: submission.ID, RemoteStatus: job.Status, QueuePosition: job.QueuePosition,
					ResultAssetIDs: current.ResultAssetIDs,
				})
				if updateErr == nil {
					current = updated
				}
				err = updateErr
			}
			if err != nil {
				_, _ = manager.persistTerminal(run, UpdateAttemptInput{
					State: AttemptFailed, RemoteJobID: current.RemoteJobID, RemoteStatus: current.RemoteStatus,
					QueuePosition: current.QueuePosition, ResultAssetIDs: current.ResultAssetIDs,
					Error: AttemptError{Code: "storage_failure", Message: boundedErrorMessage(err.Error())},
				})
				run.lifecycle.Unlock()
				return
			}
			run.lifecycle.Unlock()
			if !waitForPoll(jobCtx, run.prepared.PollInterval) {
				if run.ctx.Err() == nil {
					run.lifecycle.Lock()
					latest, exists := manager.GetAttempt(attempt.ID)
					if exists && activeAttemptState(latest.State) {
						manager.finishFromError(latest, run, jobCtx, jobCtx.Err())
					}
					run.lifecycle.Unlock()
				}
				return
			}
		case "failed":
			failure := AttemptError{Code: "remote_failed", Message: "image generation failed"}
			if job.Error != nil {
				failure = AttemptError{Code: job.Error.Code, Message: boundedErrorMessage(job.Error.Message)}
			}
			_, _ = manager.persistTerminal(run, UpdateAttemptInput{
				State: AttemptFailed, RemoteJobID: submission.ID, RemoteStatus: job.Status,
				QueuePosition: job.QueuePosition, ResultAssetIDs: current.ResultAssetIDs, Error: failure,
			})
			run.lifecycle.Unlock()
			return
		case "cancelled":
			_, _ = manager.persistTerminal(run, UpdateAttemptInput{
				State: AttemptCancelled, RemoteJobID: submission.ID, RemoteStatus: job.Status,
				QueuePosition: job.QueuePosition, ResultAssetIDs: current.ResultAssetIDs,
			})
			run.lifecycle.Unlock()
			return
		case "completed":
			current, importErr := manager.importResults(jobCtx, run, current, job)
			if importErr != nil {
				if errors.Is(importErr, context.Canceled) || errors.Is(importErr, context.DeadlineExceeded) {
					if run.ctx.Err() == nil {
						manager.finishFromError(current, run, jobCtx, importErr)
					}
					run.lifecycle.Unlock()
					return
				}
				_, _ = manager.persistTerminal(run, UpdateAttemptInput{
					State: AttemptFailed, RemoteJobID: submission.ID, RemoteStatus: job.Status,
					QueuePosition: job.QueuePosition, ResultAssetIDs: current.ResultAssetIDs,
					Error: AttemptError{Code: "invalid_result", Message: boundedErrorMessage(importErr.Error())},
				})
				run.lifecycle.Unlock()
				return
			}
			_, _ = manager.persistTerminal(run, UpdateAttemptInput{
				State: AttemptSucceeded, RemoteJobID: submission.ID, RemoteStatus: job.Status,
				QueuePosition: job.QueuePosition, ResultAssetIDs: current.ResultAssetIDs,
			})
			run.lifecycle.Unlock()
			return
		default:
			manager.finishFromError(current, run, jobCtx, fmt.Errorf("unknown remote image job state %q", job.Status))
			run.lifecycle.Unlock()
			return
		}
	}
}

func (manager *Manager) finishRun(run *attemptRun) {
	attempt, ok := manager.GetAttempt(run.attemptID)
	if ok && activeAttemptState(attempt.State) {
		return
	}
	manager.mu.Lock()
	if manager.attempts[run.attemptID] == run {
		delete(manager.attempts, run.attemptID)
	}
	manager.mu.Unlock()
}

func (manager *Manager) importResults(ctx context.Context, run *attemptRun, current Attempt, job sdcpp.Job) (Attempt, error) {
	if job.Result == nil || len(job.Result.Images) == 0 {
		return current, fmt.Errorf("completed image job has no images")
	}
	images := append([]sdcpp.JobImage(nil), job.Result.Images...)
	sort.SliceStable(images, func(left, right int) bool { return images[left].Index < images[right].Index })
	for _, remoteImage := range images {
		if err := ctx.Err(); err != nil {
			return current, err
		}
		contents, err := decodeResultImage(remoteImage.B64JSON, run.provider.MaxImageBytes)
		if err != nil {
			return current, fmt.Errorf("decode result image %d: %w", remoteImage.Index, err)
		}
		mediaType, extension, err := sniffResultImage(contents)
		if err != nil {
			return current, fmt.Errorf("inspect result image %d: %w", remoteImage.Index, err)
		}
		if !resultFormatMatches(job.Result.OutputFormat, mediaType) {
			return current, fmt.Errorf("result image %d format %q does not match %s", remoteImage.Index, job.Result.OutputFormat, mediaType)
		}
		created, err := manager.assets.Import(asset.ImportInput{
			Reader: bytes.NewReader(contents), DisplayName: fmt.Sprintf("%s-%s-%s-%d.%s", run.batchID, run.itemID, current.ID, remoteImage.Index, extension),
			MediaType: mediaType, Source: "imagegen:" + current.ID,
		})
		if err != nil {
			return current, fmt.Errorf("import result image %d: %w", remoteImage.Index, err)
		}
		updated, err := manager.attachResult(run.batchID, run.itemID, current.ID, created.ID)
		if err != nil {
			return current, errors.Join(fmt.Errorf("attach result image %d: %w", remoteImage.Index, err), manager.assets.Delete(created.ID))
		}
		current = updated
		manager.publish(run.batchID, AttemptEvent{Type: "state", Attempt: current})
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}
	return current, nil
}

func decodeResultImage(encoded string, maximum int64) ([]byte, error) {
	if encoded == "" || strings.ContainsAny(encoded, "\r\n\t ") {
		return nil, fmt.Errorf("base64 is empty or contains whitespace")
	}
	if int64(len(encoded)) > ((maximum+2)/3)*4+4 {
		return nil, ErrImageResultLimit
	}
	contents, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid base64: %w", err)
	}
	if int64(len(contents)) > maximum {
		return nil, ErrImageResultLimit
	}
	return contents, nil
}

func sniffResultImage(contents []byte) (mediaType, extension string, err error) {
	switch {
	case len(contents) >= 8 && bytes.Equal(contents[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png", "png", nil
	case len(contents) >= 3 && contents[0] == 0xff && contents[1] == 0xd8 && contents[2] == 0xff:
		return "image/jpeg", "jpg", nil
	case len(contents) >= 12 && bytes.Equal(contents[:4], []byte("RIFF")) && bytes.Equal(contents[8:12], []byte("WEBP")):
		return "image/webp", "webp", nil
	default:
		return "", "", fmt.Errorf("unsupported image signature")
	}
}

func resultFormatMatches(format, mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "png":
		return mediaType == "image/png"
	case "jpeg", "jpg":
		return mediaType == "image/jpeg"
	case "webp":
		return mediaType == "image/webp"
	default:
		return false
	}
}

func (manager *Manager) finishFromError(current Attempt, run *attemptRun, ctx context.Context, err error) error {
	state := AttemptFailed
	code := "request_failed"
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		state, code = AttemptCancelled, "cancelled"
	} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		code = "timeout"
	}
	_, persistErr := manager.persistTerminal(run, UpdateAttemptInput{
		State: state, RemoteJobID: current.RemoteJobID, RemoteStatus: current.RemoteStatus,
		QueuePosition: current.QueuePosition, ResultAssetIDs: current.ResultAssetIDs,
		Error: AttemptError{Code: code, Message: boundedErrorMessage(err.Error())},
	})
	return persistErr
}

func (manager *Manager) cancelQueued(attempt Attempt, run *attemptRun, err error) {
	message := "image attempt cancelled while queued"
	if err != nil {
		message = boundedErrorMessage(err.Error())
	}
	_, _ = manager.updateAttempt(run.batchID, run.itemID, attempt.ID, UpdateAttemptInput{
		State: AttemptCancelled, Error: AttemptError{Code: "cancelled", Message: message},
	})
}

func (manager *Manager) beginStart() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.accepting {
		return ErrImageManagerClosed
	}
	manager.starts.Add(1)
	return nil
}

func (manager *Manager) provider(providerID string) (sdcpp.ImageProvider, error) {
	for _, provider := range manager.config.Snapshot().Images.Providers {
		if provider.ID != providerID {
			continue
		}
		if !provider.Enabled {
			return sdcpp.ImageProvider{}, ErrImageProviderDisabled
		}
		return provider, nil
	}
	return sdcpp.ImageProvider{}, ErrImageProviderNotFound
}

func (manager *Manager) providerSemaphore(provider sdcpp.ImageProvider) *limiter {
	limiter := manager.providerSem[provider.ID]
	if limiter == nil {
		limiter = newLimiter(provider.MaxConcurrentJobs)
		manager.providerSem[provider.ID] = limiter
	} else {
		limiter.setLimit(provider.MaxConcurrentJobs)
	}
	return limiter
}

func (manager *Manager) batchQueue(batch Batch) *batchQueue {
	queue := manager.batchQueues[batch.ID]
	if queue == nil {
		queue = &batchQueue{jobs: make(chan scheduledAttempt, 4096)}
		manager.batchQueues[batch.ID] = queue
		go manager.dispatch(queue)
	}
	return queue
}

func (manager *Manager) batchSemaphore(batch Batch) *limiter {
	limiter := manager.batchSem[batch.ID]
	if limiter == nil {
		limiter = newLimiter(batch.Concurrency)
		manager.batchSem[batch.ID] = limiter
	} else {
		limiter.setLimit(batch.Concurrency)
	}
	return limiter
}

func itemHasActiveAttempt(item Item) bool {
	for _, attempt := range item.Attempts {
		if activeAttemptState(attempt.State) {
			return true
		}
	}
	return false
}

func waitForPoll(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func boundedErrorMessage(message string) string {
	if len(message) <= 4096 {
		return message
	}
	message = message[:4096]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}

func (manager *Manager) bestEffortRemoteCancel(run *attemptRun, jobID string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(run.provider.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	_ = manager.remote.Cancel(ctx, run.provider, jobID)
}

func (manager *Manager) persistTerminal(run *attemptRun, input UpdateAttemptInput) (Attempt, error) {
	return manager.persistTerminalContext(run.ctx, run, input)
}

func (manager *Manager) persistCommandTerminal(run *attemptRun, input UpdateAttemptInput) (Attempt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(run.provider.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	return manager.persistTerminalContext(ctx, run, input)
}

func (manager *Manager) persistTerminalContext(ctx context.Context, run *attemptRun, input UpdateAttemptInput) (Attempt, error) {
	delay := 25 * time.Millisecond
	for {
		attempt, err := manager.updateAttempt(run.batchID, run.itemID, run.attemptID, input)
		if err == nil {
			return attempt, nil
		}
		if latest, ok := manager.GetAttempt(run.attemptID); ok && terminalAttemptState(latest.State) {
			return latest, nil
		}
		if errors.Is(err, ErrBatchNotFound) || errors.Is(err, ErrItemNotFound) || errors.Is(err, ErrAttemptNotFound) || errors.Is(err, ErrInvalidAttemptTransition) {
			return Attempt{}, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
			if delay < time.Second {
				delay *= 2
				if delay > time.Second {
					delay = time.Second
				}
			}
		case <-ctx.Done():
			timer.Stop()
			return Attempt{}, errors.Join(err, ctx.Err())
		}
	}
}

func (manager *Manager) updateAttempt(batchID, itemID, attemptID string, input UpdateAttemptInput) (Attempt, error) {
	var (
		attempt Attempt
		err     error
	)
	for retry := range 3 {
		attempt, err = manager.persistAttempt(batchID, itemID, attemptID, input)
		if err == nil {
			manager.publish(batchID, AttemptEvent{Type: "state", Attempt: attempt})
			return attempt, nil
		}
		if retry < 2 {
			timer := time.NewTimer(time.Duration(1<<retry) * 10 * time.Millisecond)
			<-timer.C
		}
	}
	return attempt, err
}

func (manager *Manager) publish(batchID string, event AttemptEvent) {
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
