//go:build linux

package videogen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

const (
	tailSourceReferenceModule = "video_attempt"
	tailResultReferenceModule = "video_result"
)

// TailExtraction is the independently persisted lifecycle of one external
// tail-frame extraction.
type TailExtraction struct {
	ID            string       `json:"id"`
	SourceAssetID string       `json:"source_asset_id"`
	PresetID      string       `json:"preset_id"`
	State         AttemptState `json:"state"`
	OutputAssetID string       `json:"output_asset_id,omitempty"`
	PID           int          `json:"pid,omitempty"`
	Error         AttemptError `json:"error"`
	CreatedAt     time.Time    `json:"created_at"`
	StartedAt     *time.Time   `json:"started_at,omitempty"`
	CompletedAt   *time.Time   `json:"completed_at,omitempty"`
}

type tailRun struct {
	cancel    context.CancelFunc
	lifecycle sync.Mutex
	cancelled bool
	finished  bool
	err       error
}

// TailExtractor stages one static video into a fixed workspace, invokes the
// user-configured local command, and imports only its declared image output.
type TailExtractor struct {
	config        *config.Repository
	repository    *TailRepository
	assets        *asset.Repository
	executor      *CLIExecutor
	workspaceRoot string
	logRoot       string

	mu          sync.Mutex
	runs        map[string]*tailRun
	subscribers map[string]map[chan TailExtraction]struct{}
	failures    map[string]error
	pending     map[string]TailExtraction
	shutdown    bool
	wait        sync.WaitGroup
}

func NewTailExtractor(configuration *config.Repository, repository *TailRepository, assets *asset.Repository, executor *CLIExecutor, workspaceRoot, logRoot string) *TailExtractor {
	return &TailExtractor{config: configuration, repository: repository, assets: assets, executor: executor, workspaceRoot: workspaceRoot, logRoot: logRoot,
		runs: make(map[string]*tailRun), subscribers: make(map[string]map[chan TailExtraction]struct{}), failures: make(map[string]error), pending: make(map[string]TailExtraction)}
}

func (extractor *TailExtractor) Extract(ctx context.Context, sourceAssetID, presetID string) (TailExtraction, error) {
	if ctx == nil {
		return TailExtraction{}, fmt.Errorf("tail extraction context is nil")
	}
	if err := ctx.Err(); err != nil {
		return TailExtraction{}, err
	}
	if extractor == nil || extractor.config == nil || extractor.repository == nil || extractor.assets == nil || extractor.executor == nil || strings.TrimSpace(extractor.workspaceRoot) == "" {
		return TailExtraction{}, fmt.Errorf("tail extractor is not configured")
	}
	source, ok := extractor.assets.Get(sourceAssetID)
	if !ok {
		return TailExtraction{}, asset.ErrNotFound
	}
	if !strings.HasPrefix(strings.ToLower(source.MediaType), "video/") {
		return TailExtraction{}, fmt.Errorf("tail extraction input must be a static video")
	}
	preset, ok := findTailPreset(extractor.config.Snapshot().Videos.TailFramePresets, presetID)
	if !ok || !preset.Enabled {
		return TailExtraction{}, fmt.Errorf("tail-frame preset %q is unavailable", presetID)
	}
	id, err := randomID()
	if err != nil {
		return TailExtraction{}, err
	}
	now := time.Now().UTC()
	extraction := TailExtraction{ID: id, SourceAssetID: source.ID, PresetID: preset.ID, State: AttemptQueued, CreatedAt: now}
	if err := extractor.repository.Create(extraction); err != nil {
		return TailExtraction{}, fmt.Errorf("persist queued tail extraction: %w", err)
	}
	workspace, err := extractor.prepareWorkspace(extraction, source, preset)
	if err != nil {
		if persistErr := extractor.finishWithoutRun(extraction, AttemptFailed, "workspace", err); persistErr != nil {
			return extraction, persistErr
		}
		return extraction, nil
	}
	if _, err := extractor.assets.AddReference(source.ID, asset.Reference{Module: tailSourceReferenceModule, RecordID: extraction.ID}); err != nil {
		if persistErr := extractor.finishWithoutRun(extraction, AttemptFailed, "source_reference", err); persistErr != nil {
			return extraction, persistErr
		}
		return extraction, nil
	}
	command, err := expandTailTemplate(preset.CommandTemplate, map[string]string{
		"INPUT_VIDEO": workspace.inputPath, "OUTPUT_IMAGE": workspace.outputPath, "ASSET_ID": source.ID,
	})
	if err != nil {
		if persistErr := extractor.finishWithoutRun(extraction, AttemptFailed, "template", err); persistErr != nil {
			return extraction, persistErr
		}
		return extraction, nil
	}
	runContext, cancel := context.WithCancel(context.Background())
	extractor.mu.Lock()
	if extractor.shutdown {
		extractor.mu.Unlock()
		cancel()
		if persistErr := extractor.finishWithoutRun(extraction, AttemptCancelled, "shutdown", errors.New("tail extractor is shut down")); persistErr != nil {
			return extraction, persistErr
		}
		return extraction, nil
	}
	run := &tailRun{cancel: cancel}
	extractor.runs[extraction.ID] = run
	extractor.wait.Add(1)
	extractor.mu.Unlock()
	go extractor.execute(runContext, run, extraction, preset, workspace, command)
	return extraction, nil
}

func (extractor *TailExtractor) CancelExtraction(ctx context.Context, extractionID string) error {
	if ctx == nil {
		return fmt.Errorf("tail extraction cancel context is nil")
	}
	extractor.mu.Lock()
	run, exists := extractor.runs[extractionID]
	extractor.mu.Unlock()
	if !exists {
		if extraction, ok := extractor.repository.Get(extractionID); !ok {
			return ErrTailExtractionNotFound
		} else if terminalAttemptState(extraction.State) {
			return nil
		}
		return fmt.Errorf("tail extraction is not running")
	}
	run.lifecycle.Lock()
	if run.finished {
		err := run.err
		run.lifecycle.Unlock()
		return err
	}
	run.cancelled = true
	run.cancel()
	run.lifecycle.Unlock()
	err := extractor.executor.Stop(ctx, extractionID)
	if errors.Is(err, ErrCLIAttemptNotRunning) || errors.Is(err, ErrCLIExecutorShutdown) {
		return nil
	}
	return err
}

func (extractor *TailExtractor) SubscribeExtraction(extractionID string) (TailExtraction, <-chan TailExtraction, func(), error) {
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	extraction, ok := extractor.repository.Get(extractionID)
	if !ok {
		return TailExtraction{}, nil, nil, ErrTailExtractionNotFound
	}
	updates := make(chan TailExtraction, 8)
	if extractor.subscribers[extractionID] == nil {
		extractor.subscribers[extractionID] = make(map[chan TailExtraction]struct{})
	}
	extractor.subscribers[extractionID][updates] = struct{}{}
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			extractor.mu.Lock()
			if _, subscribed := extractor.subscribers[extractionID][updates]; subscribed {
				delete(extractor.subscribers[extractionID], updates)
				close(updates)
			}
			if len(extractor.subscribers[extractionID]) == 0 {
				delete(extractor.subscribers, extractionID)
			}
			extractor.mu.Unlock()
		})
	}
	return extraction, updates, cancel, nil
}

func (extractor *TailExtractor) SaveExtractionLog(extractionID string) (string, error) {
	if extractor == nil || strings.TrimSpace(extractor.logRoot) == "" || !validGeneratedID(extractionID) {
		return "", fmt.Errorf("tail extraction log destination is invalid")
	}
	if _, ok := extractor.repository.Get(extractionID); !ok {
		return "", ErrTailExtractionNotFound
	}
	if err := os.MkdirAll(extractor.logRoot, 0o700); err != nil {
		return "", fmt.Errorf("create tail extraction log directory: %w", err)
	}
	return extractor.executor.SaveLog(extractionID, filepath.Join(extractor.logRoot, extractionID+".log"))
}

func (extractor *TailExtractor) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("tail extraction shutdown context is nil")
	}
	extractor.mu.Lock()
	extractor.shutdown = true
	ids := make([]string, 0, len(extractor.runs))
	for id := range extractor.runs {
		ids = append(ids, id)
	}
	for extractionID, subscribers := range extractor.subscribers {
		for subscriber := range subscribers {
			close(subscriber)
		}
		delete(extractor.subscribers, extractionID)
	}
	extractor.mu.Unlock()
	var joined error
	for _, id := range ids {
		if err := extractor.CancelExtraction(ctx, id); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			joined = errors.Join(joined, err)
		}
	}
	done := make(chan struct{})
	go func() { extractor.wait.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		joined = errors.Join(joined, ctx.Err())
	}
	extractor.mu.Lock()
	for _, err := range extractor.failures {
		joined = errors.Join(joined, err)
	}
	extractor.mu.Unlock()
	return joined
}

type tailWorkspace struct {
	root, inputPath, outputDir, outputPath string
}

func (extractor *TailExtractor) prepareWorkspace(extraction TailExtraction, source asset.Asset, preset videoconfig.TailFramePreset) (tailWorkspace, error) {
	base := filepath.Clean(extractor.workspaceRoot)
	root := filepath.Join(base, "tail-"+extraction.ID)
	if !filepath.IsAbs(base) || filepath.Dir(root) != base {
		return tailWorkspace{}, fmt.Errorf("tail workspace root is invalid")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return tailWorkspace{}, fmt.Errorf("create tail workspace root: %w", err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return tailWorkspace{}, fmt.Errorf("create tail workspace: %w", err)
	}
	workspace := tailWorkspace{root: root, outputDir: filepath.Join(root, "outputs")}
	if err := os.Mkdir(filepath.Join(root, "inputs"), 0o700); err != nil {
		return tailWorkspace{}, err
	}
	if err := os.Mkdir(workspace.outputDir, 0o700); err != nil {
		return tailWorkspace{}, err
	}
	workspace.inputPath = filepath.Join(root, "inputs", "input"+tailInputExtension(source.MediaType))
	workspace.outputPath = filepath.Join(workspace.outputDir, "tail"+preset.OutputExtension)
	sourceFile, current, err := extractor.assets.OpenContent(source.ID)
	if err != nil {
		return tailWorkspace{}, fmt.Errorf("open tail source video: %w", err)
	}
	defer sourceFile.Close()
	if current.SHA256 != source.SHA256 || current.MediaType != source.MediaType || current.Size != source.Size {
		return tailWorkspace{}, fmt.Errorf("tail source video changed before staging")
	}
	if err := os.Link(sourceFile.Name(), workspace.inputPath); err == nil {
		return workspace, nil
	}
	if _, err := sourceFile.Seek(0, io.SeekStart); err != nil {
		return tailWorkspace{}, err
	}
	target, err := os.OpenFile(workspace.inputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return tailWorkspace{}, err
	}
	_, copyErr := io.Copy(target, sourceFile)
	if copyErr == nil {
		copyErr = target.Sync()
	}
	closeErr := target.Close()
	if copyErr != nil {
		return tailWorkspace{}, copyErr
	}
	if closeErr != nil {
		return tailWorkspace{}, closeErr
	}
	return workspace, nil
}

func (extractor *TailExtractor) execute(ctx context.Context, run *tailRun, extraction TailExtraction, preset videoconfig.TailFramePreset, workspace tailWorkspace, command string) {
	defer extractor.wait.Done()
	defer func() {
		run.lifecycle.Lock()
		finished := run.finished
		run.lifecycle.Unlock()
		extractor.mu.Lock()
		_, pending := extractor.pending[extraction.ID]
		if finished && !pending {
			delete(extractor.runs, extraction.ID)
		}
		extractor.mu.Unlock()
	}()
	now := time.Now().UTC()
	extraction.State, extraction.StartedAt = AttemptRunning, &now
	if err := extractor.updateAndPublish(extraction); err != nil {
		run.lifecycle.Lock()
		extraction.State = AttemptFailed
		extraction.Error = AttemptError{Code: "state_persistence", Message: boundedVideoError(err.Error())}
		extractor.finishTailRun(extraction.ID, run, extraction)
		run.lifecycle.Unlock()
		return
	}
	request := CLIRunRequest{AttemptID: extraction.ID, Command: command,
		WorkspaceRoot: workspace.root, WorkDir: workspace.root, Env: map[string]string{"INPUT_VIDEO": workspace.inputPath, "OUTPUT_IMAGE": workspace.outputPath, "ASSET_ID": extraction.SourceAssetID},
		Timeout: time.Duration(preset.TimeoutSeconds) * time.Second, StopGrace: time.Duration(preset.StopGraceSeconds) * time.Second,
		LogBufferBytes: 64 << 10, OutputDir: workspace.outputDir, OutputPath: workspace.outputPath, OutputMediaType: tailMediaType(preset.OutputExtension), OutputExtension: preset.OutputExtension, MaxOutputBytes: preset.MaxImageBytes}
	monitorDone := make(chan struct{})
	go extractor.monitorPID(extraction.ID, monitorDone)
	result, err := extractor.executor.Run(ctx, request)
	close(monitorDone)
	run.lifecycle.Lock()
	defer run.lifecycle.Unlock()
	cancelled := run.cancelled
	if cancelled || result.State == CLIStateStopped {
		extraction.State = AttemptCancelled
		extraction.Error = AttemptError{Code: "cancelled", Message: "tail extraction cancelled"}
		extractor.finishTailRun(extraction.ID, run, extraction)
		return
	}
	if err != nil || result.State != CLIStateSucceeded {
		extraction.State = AttemptFailed
		extraction.Error = AttemptError{Code: "command_failed", Message: boundedVideoError(errorText(err, result.Error))}
		extractor.finishTailRun(extraction.ID, run, extraction)
		return
	}
	file, err := openCLIOutputNoFollow(workspace.root, filepath.Join("outputs", filepath.Base(workspace.outputPath)))
	if err != nil {
		extraction.State, extraction.Error = AttemptFailed, AttemptError{Code: "output_invalid", Message: boundedVideoError(err.Error())}
		extractor.finishTailRun(extraction.ID, run, extraction)
		return
	}
	defer file.Close()
	if _, err := validateTailImageFile(file, preset); err != nil {
		extraction.State, extraction.Error = AttemptFailed, AttemptError{Code: "output_invalid", Message: boundedVideoError(err.Error())}
		extractor.finishTailRun(extraction.ID, run, extraction)
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		extraction.State, extraction.Error = AttemptFailed, AttemptError{Code: "output_read", Message: boundedVideoError(err.Error())}
		extractor.finishTailRun(extraction.ID, run, extraction)
		return
	}
	output, err := extractor.assets.Import(asset.ImportInput{Reader: file, DisplayName: "tail-" + extraction.ID + preset.OutputExtension, MediaType: tailMediaType(preset.OutputExtension), Source: "video-tail:" + extraction.ID})
	if err != nil {
		extraction.State, extraction.Error = AttemptFailed, AttemptError{Code: "output_import", Message: boundedVideoError(err.Error())}
		extractor.finishTailRun(extraction.ID, run, extraction)
		return
	}
	if _, err := extractor.assets.AddReference(output.ID, asset.Reference{Module: tailResultReferenceModule, RecordID: extraction.ID}); err != nil {
		extraction.State, extraction.Error = AttemptFailed, AttemptError{Code: "result_reference", Message: boundedVideoError(err.Error())}
		extractor.finishTailRun(extraction.ID, run, extraction)
		return
	}
	extraction.State, extraction.OutputAssetID, extraction.Error = AttemptSucceeded, output.ID, AttemptError{}
	extractor.finishTailRun(extraction.ID, run, extraction)
}

func (extractor *TailExtractor) monitorPID(extractionID string, done <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := extractor.executor.Status(extractionID)
		if err == nil && status.PID > 0 {
			if current, ok := extractor.repository.Get(extractionID); ok && current.State == AttemptRunning && current.PID != status.PID {
				current.PID = status.PID
				_ = extractor.updateAndPublish(current)
			}
		}
		select {
		case <-done:
			return
		case <-ticker.C:
		}
	}
}

func (extractor *TailExtractor) finishWithoutRun(extraction TailExtraction, state AttemptState, code string, cause error) error {
	extraction.State = state
	extraction.Error = AttemptError{Code: code, Message: boundedVideoError(cause.Error())}
	now := time.Now().UTC()
	extraction.CompletedAt = &now
	return extractor.updateAndPublish(extraction)
}

func (extractor *TailExtractor) complete(extraction *TailExtraction) error {
	now := time.Now().UTC()
	extraction.PID, extraction.CompletedAt = 0, &now
	return extractor.updateAndPublish(*extraction)
}

func (extractor *TailExtractor) finishTailRun(id string, run *tailRun, extraction TailExtraction) {
	if err := extractor.complete(&extraction); err != nil {
		run.err, run.finished = err, true
		extractor.mu.Lock()
		extractor.failures[id] = err
		extractor.pending[id] = cloneTailExtraction(extraction)
		extractor.mu.Unlock()
		go extractor.retryPendingTerminal(id, run)
		return
	}
	run.finished = true
}

func (extractor *TailExtractor) retryPendingTerminal(id string, run *tailRun) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		extractor.mu.Lock()
		if extractor.shutdown {
			extractor.mu.Unlock()
			return
		}
		extraction, pending := extractor.pending[id]
		extractor.mu.Unlock()
		if !pending {
			return
		}
		if err := extractor.updateAndPublish(extraction); err != nil {
			extractor.mu.Lock()
			extractor.failures[id] = err
			extractor.mu.Unlock()
			continue
		}
		run.lifecycle.Lock()
		run.err, run.finished = nil, true
		run.lifecycle.Unlock()
		extractor.mu.Lock()
		delete(extractor.pending, id)
		delete(extractor.failures, id)
		if extractor.runs[id] == run {
			delete(extractor.runs, id)
		}
		extractor.mu.Unlock()
		return
	}
}

func (extractor *TailExtractor) updateAndPublish(extraction TailExtraction) error {
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	if err := extractor.repository.Update(extraction); err != nil {
		return err
	}
	for subscriber := range extractor.subscribers[extraction.ID] {
		select {
		case subscriber <- cloneTailExtraction(extraction):
		default:
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- cloneTailExtraction(extraction):
			default:
			}
		}
	}
	return nil
}

func findTailPreset(presets []videoconfig.TailFramePreset, id string) (videoconfig.TailFramePreset, bool) {
	for _, preset := range presets {
		if preset.ID == id {
			return preset, true
		}
	}
	return videoconfig.TailFramePreset{}, false
}

func expandTailTemplate(template string, values map[string]string) (string, error) {
	allowed := map[string]struct{}{"INPUT_VIDEO": {}, "OUTPUT_IMAGE": {}, "ASSET_ID": {}}
	if len(template) > maximumExpandedCLITemplateBytes {
		return "", fmt.Errorf("tail command template result exceeds %d bytes", maximumExpandedCLITemplateBytes)
	}
	var expanded strings.Builder
	for offset := 0; offset < len(template); {
		open := strings.Index(template[offset:], "{{")
		close := strings.Index(template[offset:], "}}")
		if open < 0 && close < 0 {
			if err := appendExpanded(&expanded, template[offset:]); err != nil {
				return "", err
			}
			break
		}
		if close >= 0 && (open < 0 || close < open) {
			return "", fmt.Errorf("tail command template has an unmatched closing token")
		}
		start := offset + open
		if err := appendExpanded(&expanded, template[offset:start]); err != nil {
			return "", err
		}
		endRelative := strings.Index(template[start+2:], "}}")
		if endRelative < 0 {
			return "", fmt.Errorf("tail command template has an unmatched opening token")
		}
		end := start + 2 + endRelative
		name := template[start+2 : end]
		if _, ok := allowed[name]; !ok || strings.Contains(name, "{{") {
			return "", fmt.Errorf("tail command template token %q is not allowed", name)
		}
		value, ok := values[name]
		if !ok {
			return "", fmt.Errorf("tail command template token %q is missing", name)
		}
		if err := appendExpanded(&expanded, shellQuote(value)); err != nil {
			return "", err
		}
		offset = end + 2
	}
	return expanded.String(), nil
}

func tailInputExtension(mediaType string) string {
	switch strings.ToLower(mediaType) {
	case "video/webm":
		return ".webm"
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "video/avi", "video/x-msvideo":
		return ".avi"
	default:
		return ".video"
	}
}

func tailMediaType(extension string) string {
	switch strings.ToLower(extension) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

func validateTailImageFile(file *os.File, preset videoconfig.TailFramePreset) (int64, error) {
	mediaType := tailMediaType(preset.OutputExtension)
	_, magic := cliOutputFormat(mediaType)
	if file == nil || mediaType == "" || magic == nil || preset.MaxImageBytes < 1 {
		return 0, fmt.Errorf("tail image declaration is invalid")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 0, fmt.Errorf("tail output must be a regular non-symlink file")
	}
	limited := &io.LimitedReader{R: file, N: preset.MaxImageBytes + 1}
	header := make([]byte, 12)
	read, readErr := io.ReadFull(limited, header)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return 0, fmt.Errorf("read tail output: %w", readErr)
	}
	rest, err := io.Copy(io.Discard, limited)
	if err != nil {
		return 0, err
	}
	total := int64(read) + rest
	if total == 0 || total > preset.MaxImageBytes {
		return 0, fmt.Errorf("tail output size is invalid")
	}
	if !magic(header[:read]) {
		return 0, fmt.Errorf("tail output magic does not match %q", mediaType)
	}
	return total, nil
}

func errorText(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	if fallback != "" {
		return fallback
	}
	return "tail command failed"
}
