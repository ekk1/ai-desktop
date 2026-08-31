package videogen

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

// These tests fail if items are not persisted in their displayed order, if timing
// choices can be ambiguous, or if callers can mutate repository-owned state.
func TestRepositoryPersistsOrderedVideoItems(t *testing.T) {
	root := t.TempDir()
	repository, err := OpenRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := repository.CreateBatch(validBatchInput())
	if err != nil {
		t.Fatal(err)
	}
	items, err := repository.CreateItems(batch.ID, []CreateItemInput{{Prompt: "one", Enabled: true}, {Prompt: "two", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MoveItem(batch.ID, items[1].ID, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateItem(batch.ID, items[0].ID, UpdateItemInput{Prompt: "one edited", NegativePrompt: "blur", Enabled: false, ParamsOverride: json.RawMessage(`{"guidance":4}`), TimingOverride: &TimingInput{Mode: "frames", VideoFrames: 24, FPS: 12}, ControlFrameIDs: []string{assetID(3)}, SelectedAssets: []SelectedAsset{{AssetID: assetID(4), Role: "reference", Order: 0}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateBatch(batch.ID, UpdateBatchInput{Title: "Shots edited", Folder: "work", ExecutionKind: videoconfig.ExecutionLocalCLI, PresetID: "local-cli", Concurrency: 2, CommonParams: json.RawMessage(`{"seed":2}`), Timing: TimingInput{Mode: "frames", VideoFrames: 32, FPS: 16}}); err != nil {
		t.Fatal(err)
	}

	got, ok := repository.Get(batch.ID)
	if !ok {
		t.Fatal("batch missing")
	}
	if got.Title != "Shots edited" || got.ExecutionKind != videoconfig.ExecutionLocalCLI || got.Items[0].Prompt != "two" || got.Items[0].Order != 0 || got.Items[1].Prompt != "one edited" || got.Items[1].Order != 1 {
		t.Fatalf("batch = %#v", got)
	}
	got.Items[1].ControlFrameIDs[0] = assetID(9)
	got.CommonParams[0] = '['
	if stored, _ := repository.Get(batch.ID); stored.Items[1].ControlFrameIDs[0] != assetID(3) || string(stored.CommonParams) != `{"seed":2}` {
		t.Fatalf("Get exposed aliases: %#v", stored)
	}

	reopened, err := OpenRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := reopened.Get(batch.ID)
	if !ok || !reflect.DeepEqual(storedBatch(t, repository, batch.ID), persisted) {
		t.Fatalf("reopened = %#v", persisted)
	}
}

func TestRepositoryRejectsAmbiguousTiming(t *testing.T) {
	input := validBatchInput()
	input.Timing = TimingInput{Mode: "duration", DurationSeconds: 2, FPS: 0, VideoFrames: 33}
	if _, err := newRepositoryFixture(t).CreateBatch(input); err == nil {
		t.Fatal("ambiguous timing accepted")
	}
}

func TestRepositoryValidatesManagedParamsAndItemAssetsWithoutMutation(t *testing.T) {
	repository := newRepositoryFixture(t)
	for _, key := range []string{"prompt", "negative_prompt", "fps", "video_frames", "init_image", "end_image", "control_frames", "selected_assets"} {
		input := validBatchInput()
		input.CommonParams = json.RawMessage(`{"` + key + `":"managed"}`)
		if _, err := repository.CreateBatch(input); err == nil {
			t.Fatalf("accepted managed batch param %q", key)
		}
	}
	batch, err := repository.CreateBatch(validBatchInput())
	if err != nil {
		t.Fatal(err)
	}
	before, _ := repository.Get(batch.ID)
	if _, err := repository.CreateItems(batch.ID, []CreateItemInput{{Prompt: "p", ParamsOverride: json.RawMessage(`[]`)}}); err == nil {
		t.Fatal("accepted array params")
	}
	if _, err := repository.CreateItems(batch.ID, []CreateItemInput{{Prompt: "p", InitImageID: "not-an-id"}}); err == nil {
		t.Fatal("accepted invalid asset ID")
	}
	after, _ := repository.Get(batch.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("invalid items mutated batch: before=%#v after=%#v", before, after)
	}
}

func TestRepositoryInterruptsPersistedActiveAttemptsOnOpen(t *testing.T) {
	root := t.TempDir()
	repository, batchID, itemID := attemptRepositoryFixture(t, root)
	attempt, err := repository.CreateAttempt(batchID, itemID, CreateAttemptInput{State: AttemptQueued, Snapshot: validVideoSnapshot()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateAttempt(batchID, itemID, attempt.ID, UpdateAttemptInput{State: AttemptRunning, PID: 1234, WorkspaceRelativePath: attempt.ID}); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reopened.Get(batchID)
	if got.Items[0].Attempts[0].State != AttemptInterrupted || got.Items[0].Attempts[0].CompletedAt == nil {
		t.Fatalf("recovered attempt = %#v", got.Items[0].Attempts[0])
	}
	if _, err := OpenRepository(root); err != nil {
		t.Fatalf("second reopen rejected recovered attempt: %v", err)
	}
}

func TestRepositoryPersistsInterruptedQueuedAttemptWithoutStartTime(t *testing.T) {
	root := t.TempDir()
	repository, batchID, itemID := attemptRepositoryFixture(t, root)
	if _, err := repository.CreateAttempt(batchID, itemID, CreateAttemptInput{State: AttemptQueued, Snapshot: validVideoSnapshot()}); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRepository(root); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRepository(root); err != nil {
		t.Fatalf("second reopen rejected recovered queued attempt: %v", err)
	}
}

func TestRepositoryEnforcesAttemptTransitionsSnapshotsAndResultIdentity(t *testing.T) {
	repository, batchID, itemID := attemptRepositoryFixture(t, t.TempDir())
	snapshot := validVideoSnapshot()
	attempt, err := repository.CreateAttempt(batchID, itemID, CreateAttemptInput{State: AttemptQueued, Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Params[0] = '['
	snapshot.HTTPProvider.Headers["X-Late"] = "mutated"
	snapshot.InputAssets[0].DisplayName = "mutated"
	if _, err := repository.CreateAttempt(batchID, itemID, CreateAttemptInput{State: AttemptQueued, Snapshot: validVideoSnapshot()}); !errors.Is(err, ErrActiveAttempt) {
		t.Fatalf("second active = %v", err)
	}
	if _, err := repository.UpdateAttempt(batchID, itemID, attempt.ID, UpdateAttemptInput{State: AttemptSucceeded}); !errors.Is(err, ErrInvalidAttemptTransition) {
		t.Fatalf("skipped transition = %v", err)
	}
	if _, err := repository.UpdateAttempt(batchID, itemID, attempt.ID, UpdateAttemptInput{State: AttemptSubmitting, RemoteJobID: "job"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateAttempt(batchID, itemID, attempt.ID, UpdateAttemptInput{State: AttemptPolling, RemoteJobID: "job", RemoteStatus: "working", QueuePosition: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateAttempt(batchID, itemID, attempt.ID, UpdateAttemptInput{State: AttemptRunning, PID: 44, WorkspaceRelativePath: attempt.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AttachResult(batchID, itemID, attempt.ID, assetID(8)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AttachResult(batchID, itemID, attempt.ID, assetID(8)); err != nil {
		t.Fatal(err)
	}
	finished, err := repository.UpdateAttempt(batchID, itemID, attempt.ID, UpdateAttemptInput{State: AttemptSucceeded, ActualFrameCount: 32, OutputAssetID: assetID(8)})
	if err != nil {
		t.Fatal(err)
	}
	if finished.StartedAt == nil || finished.CompletedAt == nil || string(finished.Snapshot.Params) != `{"steps":20}` || finished.Snapshot.HTTPProvider.Headers["X-Late"] != "" || finished.Snapshot.InputAssets[0].DisplayName != "input.png" {
		t.Fatalf("finished attempt = %#v", finished)
	}
	if _, err := repository.UpdateAttempt(batchID, itemID, attempt.ID, UpdateAttemptInput{State: AttemptCancelled}); !errors.Is(err, ErrInvalidAttemptTransition) {
		t.Fatalf("terminal transition = %v", err)
	}
	got, _ := repository.Get(batchID)
	if got.Items[0].Attempts[0].OutputAssetID != assetID(8) {
		t.Fatalf("result asset = %#v", got.Items[0].Attempts[0])
	}
}

func TestRepositoryRejectsResultAssetAttachedToAnotherAttempt(t *testing.T) {
	repository, batchID, itemID := attemptRepositoryFixture(t, t.TempDir())
	first, err := repository.CreateAttempt(batchID, itemID, CreateAttemptInput{State: AttemptQueued, Snapshot: validVideoSnapshot()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AttachResult(batchID, itemID, first.ID, assetID(6)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateAttempt(batchID, itemID, first.ID, UpdateAttemptInput{State: AttemptCancelled}); err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreateAttempt(batchID, itemID, CreateAttemptInput{State: AttemptQueued, Snapshot: validVideoSnapshot()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AttachResult(batchID, itemID, second.ID, assetID(6)); err == nil {
		t.Fatal("accepted result asset attached to another attempt")
	}
}

func TestRepositoryRejectsInvalidAttemptUpdatesWithoutMutation(t *testing.T) {
	repository, batchID, itemID := attemptRepositoryFixture(t, t.TempDir())
	attempt, _ := repository.CreateAttempt(batchID, itemID, CreateAttemptInput{State: AttemptQueued, Snapshot: validVideoSnapshot()})
	before, _ := repository.Get(batchID)
	if _, err := repository.UpdateAttempt(batchID, itemID, attempt.ID, UpdateAttemptInput{State: AttemptSubmitting, Error: AttemptError{Code: strings.Repeat("c", 121)}}); err == nil {
		t.Fatal("accepted long error code")
	}
	if _, err := repository.UpdateAttempt(batchID, itemID, attempt.ID, UpdateAttemptInput{State: AttemptSubmitting, Error: AttemptError{Message: strings.Repeat("m", 4097)}}); err == nil {
		t.Fatal("accepted long error message")
	}
	after, _ := repository.Get(batchID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("invalid updates mutated batch: before=%#v after=%#v", before, after)
	}
}

func TestRepositoryRejectsAttemptSnapshotForDifferentExecutionKind(t *testing.T) {
	repository, batchID, itemID := attemptRepositoryFixture(t, t.TempDir())
	if _, err := repository.CreateAttempt(batchID, itemID, CreateAttemptInput{State: AttemptQueued, Snapshot: validCLISnapshot()}); err == nil {
		t.Fatal("accepted attempt snapshot for a different execution kind")
	}
}

func newRepositoryFixture(t *testing.T) *Repository {
	t.Helper()
	repository, err := OpenRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return repository
}
func attemptRepositoryFixture(t *testing.T, root string) (*Repository, string, string) {
	t.Helper()
	repository, err := OpenRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := repository.CreateBatch(validBatchInput())
	if err != nil {
		t.Fatal(err)
	}
	items, err := repository.CreateItems(batch.ID, []CreateItemInput{{Prompt: "cat", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	return repository, batch.ID, items[0].ID
}
func storedBatch(t *testing.T, repository *Repository, batchID string) Batch {
	t.Helper()
	batch, ok := repository.Get(batchID)
	if !ok {
		t.Fatal("batch missing")
	}
	return batch
}
func assetID(digit int) string { return strings.Repeat(string(rune('0'+digit)), 32) }
func validBatchInput() CreateBatchInput {
	return CreateBatchInput{Title: "Shots", Folder: "film", ExecutionKind: videoconfig.ExecutionHTTP, PresetID: "gpu", Concurrency: 1, CommonParams: json.RawMessage(`{}`), Timing: TimingInput{Mode: "duration", DurationSeconds: 2.5, FPS: 16}}
}
func validVideoSnapshot() Snapshot {
	provider := videoconfig.Default().HTTPProviders[0]
	provider.ID = "gpu"
	provider.Headers = map[string]string{"Authorization": "secret"}
	return Snapshot{ExecutionKind: videoconfig.ExecutionHTTP, HTTPProvider: &provider, Params: json.RawMessage(`{"steps":20}`), Prompt: "cat", Timing: ResolvedTiming{InputMode: "duration", DurationSeconds: 2.5, FPS: 16, RequestedFrames: 40, AlgorithmVersion: "v1"}, InputAssets: []AssetSnapshot{{ID: assetID(1), SHA256: strings.Repeat("b", 64), MediaType: "image/png", DisplayName: "input.png", Role: "init", Order: 0, Size: 42}}}
}

func validCLISnapshot() Snapshot {
	preset := videoconfig.CLIPreset{ID: "local-cli", Name: "Local CLI", Enabled: true, ExecutionKind: videoconfig.ExecutionLocalCLI, CommandTemplate: "generate-video", WorkDir: "/tmp", Env: map[string]string{"TOKEN": "secret"}, TimeoutSeconds: 60, StopGraceSeconds: 5, LogBufferBytes: 1024, OutputRelativePath: "outputs/result.webm", OutputMediaType: "video/webm", OutputExtension: ".webm", MaxOutputBytes: 1, DefaultParams: json.RawMessage(`{}`)}
	return Snapshot{ExecutionKind: videoconfig.ExecutionLocalCLI, CLIPreset: &preset, Params: json.RawMessage(`{"steps":20}`), Prompt: "cat", Timing: ResolvedTiming{InputMode: "duration", DurationSeconds: 2.5, FPS: 16, RequestedFrames: 40, AlgorithmVersion: "v1"}}
}
