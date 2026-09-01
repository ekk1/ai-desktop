package videogen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

// This fails if attempts in separate batches that use one HTTP preset can
// bypass that preset's shared concurrency limit.
func TestManagerSharesPresetConcurrencyAcrossVideoBatches(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	first := fixture.createBatch("one", "http-preset", 1, 1)
	second := fixture.createBatch("two", "http-preset", 1, 1)
	fixture.remote.blockSubmissions()

	firstAttempts, err := fixture.manager.StartBatch(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondAttempts, err := fixture.manager.StartBatch(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.remote.waitForActive(t, 1)
	fixture.remote.releaseAll()
	fixture.waitTerminal(append(firstAttempts, secondAttempts...))
	if got := fixture.remote.maximumActive(); got != 1 {
		t.Fatalf("maximum active submissions = %d, want 1", got)
	}
}

// This fails if a completed scheduled run releases a shared preset permit more
// than once and lets a later third batch overlap the configured limit.
func TestManagerKeepsPresetLimitAcrossMoreThanTwoBatches(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	fixture.remote.blockSubmissions()
	var attempts []Attempt
	for _, title := range []string{"one", "two", "three"} {
		batch := fixture.createBatch(title, "http-preset", 1, 1)
		started, err := fixture.manager.StartBatch(batch.ID)
		if err != nil {
			t.Fatal(err)
		}
		attempts = append(attempts, started...)
	}
	fixture.remote.waitForActive(t, 1)
	fixture.remote.releaseAll()
	fixture.waitTerminal(attempts)
	if got := fixture.remote.maximumActive(); got != 1 {
		t.Fatalf("maximum active submissions = %d, want preset limit 1", got)
	}
}

// This fails if a batch can run more jobs than its own lower concurrency
// limit merely because its preset permits more concurrent work.
func TestManagerRespectsLowerBatchConcurrency(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 2)
	batch := fixture.createBatch("one", "http-preset", 1, 2)
	fixture.remote.blockSubmissions()

	attempts, err := fixture.manager.StartBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.remote.waitForActive(t, 1)
	fixture.remote.releaseAll()
	fixture.waitTerminal(attempts)
	if got := fixture.remote.maximumActive(); got != 1 {
		t.Fatalf("maximum active submissions = %d, want batch limit 1", got)
	}
}

// This fails if a missing or disabled preset can create an attempt and reach
// the remote executor.
func TestManagerRejectsDisabledAndMissingPresets(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	disabled := fixture.createBatch("disabled", "http-preset", 1, 1)
	missing := fixture.createBatch("missing", "does-not-exist", 1, 1)

	videos := fixture.config.Snapshot().Videos
	videos.HTTPProviders[0].Enabled = false
	if _, err := fixture.config.UpdateVideos(videos); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.StartItem(disabled.ID, disabled.Items[0].ID); !errors.Is(err, ErrVideoPresetDisabled) {
		t.Fatalf("disabled preset error = %v, want ErrVideoPresetDisabled", err)
	}

	videos.HTTPProviders[0].Enabled = true
	if _, err := fixture.config.UpdateVideos(videos); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.StartItem(missing.ID, missing.Items[0].ID); !errors.Is(err, ErrVideoPresetNotFound) {
		t.Fatalf("missing preset error = %v, want ErrVideoPresetNotFound", err)
	}
	if got := fixture.remote.submitCount(); got != 0 {
		t.Fatalf("remote submissions = %d, want none", got)
	}
}

// This fails if a second active attempt for an Item is accepted before the
// first one reaches a terminal state.
func TestManagerRejectsSecondActiveAttemptForItem(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	fixture.remote.blockSubmissions()

	first, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.remote.waitForActive(t, 1)
	if _, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID); !errors.Is(err, ErrActiveAttempt) {
		t.Fatalf("second start error = %v, want ErrActiveAttempt", err)
	}
	fixture.remote.releaseAll()
	fixture.waitTerminal([]Attempt{first})
}

// This fails if Run Batch creates queued attempts for Items that the user
// deliberately disabled.
func TestManagerSkipsDisabledItems(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	input := CreateItemInput{Prompt: "disabled", Enabled: false}
	if _, err := fixture.service.CreateItems(batch.ID, []CreateItemInput{input}); err != nil {
		t.Fatal(err)
	}

	attempts, err := fixture.manager.StartBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitTerminal(attempts)
	if got, want := len(attempts), 1; got != want {
		t.Fatalf("attempt count = %d, want %d", got, want)
	}
}

// This fails if a batch's queue does not preserve Item order while its lower
// concurrency limit serializes execution.
func TestManagerRunsBatchItemsInOrder(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 3)
	batch := fixture.createBatch("one", "http-preset", 1, 3)
	fixture.remote.blockSubmissions()

	attempts, err := fixture.manager.StartBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.remote.waitForActive(t, 1)
	fixture.remote.releaseAll()
	fixture.waitTerminal(attempts)
	if got, want := fixture.remote.submittedPrompts(), []string{"prompt-0", "prompt-1", "prompt-2"}; !equalStrings(got, want) {
		t.Fatalf("submitted prompts = %#v, want %#v", got, want)
	}
}

// This fails if shutdown accepts a new start after it has stopped scheduling.
func TestManagerRejectsStartsAfterShutdown(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	if err := fixture.manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID); !errors.Is(err, ErrVideoManagerClosed) {
		t.Fatalf("start after shutdown error = %v, want ErrVideoManagerClosed", err)
	}
}

// This fails if a 409 from the official remote cancel endpoint turns a still
// generating job into a false local cancellation instead of keeping it live.
func TestManagerKeepsPollingWhenRemoteCancelIsConflict(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	fixture.remote.setJobStatus("generating", 4)
	fixture.remote.setCancelError(&sdcpp.HTTPError{StatusCode: 409, Body: "cannot_cancel_generating"})

	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitForState(created.ID, AttemptPolling)
	if err := fixture.manager.Cancel(created.ID); err != nil {
		t.Fatal(err)
	}
	polling, ok := fixture.manager.GetAttempt(created.ID)
	if !ok || polling.State != AttemptPolling || polling.RemoteStatus != "generating" || polling.QueuePosition != 4 || polling.Error.Code != "remote_cannot_cancel" {
		t.Fatalf("attempt after conflict = %#v", polling)
	}
	fixture.remote.completeVideo(validWebM(), "video/webm", 16, 33)
	fixture.waitTerminal([]Attempt{created})
	finished, _ := fixture.manager.GetAttempt(created.ID)
	if finished.State != AttemptSucceeded || finished.ActualFrameCount != 33 {
		t.Fatalf("finished attempt = %#v", finished)
	}
}

// This fails if a completed HTTP result is not imported as an archived Asset,
// attached to the Attempt, and annotated with the server's actual frame count.
func TestManagerImportsHTTPVideoAndRecordsActualFrames(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	fixture.remote.completeVideo(validWebM(), "video/webm", 16, 33)
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitTerminal([]Attempt{created})
	attempt, _ := fixture.manager.GetAttempt(created.ID)
	if attempt.State != AttemptSucceeded || attempt.ActualFrameCount != 33 || attempt.OutputAssetID == "" {
		t.Fatalf("completed HTTP attempt = %#v", attempt)
	}
	stored, ok := fixture.assets.Get(attempt.OutputAssetID)
	if !ok || stored.State != asset.StateArchive || stored.Source != "videogen:"+attempt.ID {
		t.Fatalf("imported HTTP Asset = %#v", stored)
	}
}

// This fails if a successfully archived result is deleted when the separate
// durable attempt attachment fails after import. A deliberately conflicting
// backup destination makes that final atomic batch save fail without faking
// Asset.Import.
func TestManagerRetainsImportedHTTPAssetWhenAttachmentFails(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	fixture.remote.setJobStatus("generating", 1)
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitForState(created.ID, AttemptPolling)
	jobEntered, releaseJob := fixture.remote.blockCompletedVideo(validWebM(), "video/webm", 16, 2)
	t.Cleanup(releaseJob)
	select {
	case <-jobEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("manager did not read the completed video job")
	}
	backupPath := filepath.Join(fixture.service.repository.root, batch.ID, "batch.json.bak")
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Mkdir(backupPath, 0o700); err != nil {
		t.Fatal(err)
	}
	releaseJob()
	deadline := time.Now().Add(3 * time.Second)
	imported := false
	for {
		for _, stored := range fixture.assets.List(asset.Filter{}) {
			if stored.Source == "videogen:"+created.ID {
				if stored.State != asset.StateArchive {
					t.Fatalf("retained partial result = %#v", stored)
				}
				imported = true
				break
			}
		}
		if imported {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("imported video asset was rolled back after attachment failure")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Let the terminal write exhaust its bounded retries while storage is
	// unavailable, then restore storage. A new start must recover the pending
	// terminal transition instead of treating the dead run as active forever.
	deadline = time.Now().Add(time.Second)
	for {
		pending, _ := fixture.manager.GetAttempt(created.ID)
		if pending.Error.Code == "storage_failure" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal persistence failure was not observable")
		}
		time.Sleep(5 * time.Millisecond)
	}
	events, unsubscribe, err := fixture.manager.SubscribeBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := <-events
	unsubscribe()
	if len(snapshot.Attempts) != 1 || snapshot.Attempts[0].Error.Code != "storage_failure" {
		t.Fatalf("pending persistence snapshot = %#v", snapshot)
	}
	if err := os.RemoveAll(backupPath); err != nil {
		t.Fatal(err)
	}
	retry, err := fixture.manager.Retry(created.ID)
	if err != nil {
		t.Fatalf("start after terminal persistence recovery: %v", err)
	}
	fixture.waitTerminal([]Attempt{retry})
	recovered, _ := fixture.manager.GetAttempt(created.ID)
	if recovered.State != AttemptFailed || recovered.Error.Code != "invalid_result" {
		t.Fatalf("recovered terminal attempt = %#v", recovered)
	}
}

// This fails if Manager trusts the remote DTO's claimed MIME without applying
// strict base64 decoding before writing an Asset to the local repository.
func TestManagerRejectsMalformedHTTPVideoResult(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	fixture.remote.setRawResult("%%%")
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitTerminal([]Attempt{created})
	attempt, _ := fixture.manager.GetAttempt(created.ID)
	if attempt.State != AttemptFailed || attempt.Error.Code != "invalid_result" || attempt.OutputAssetID != "" {
		t.Fatalf("malformed result attempt = %#v", attempt)
	}
}

// This fails if the streaming result importer accepts a decoded video that is
// one byte above MaxVideoBytes or trusts an output MIME that disagrees with its
// signature.
func TestManagerRejectsOversizeAndMismatchedHTTPVideos(t *testing.T) {
	for name, configure := range map[string]func(*videoManagerFixture){
		"oversize": func(fixture *videoManagerFixture) {
			videos := fixture.config.Snapshot().Videos
			videos.HTTPProviders[0].MaxVideoBytes = 4
			if _, err := fixture.config.UpdateVideos(videos); err != nil {
				panic(err)
			}
			fixture.remote.completeVideo(append(validWebM(), 'x'), "video/webm", 16, 2)
		},
		"MIME mismatch": func(fixture *videoManagerFixture) {
			fixture.remote.completeVideo(validWebM(), "image/webp", 16, 2)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newVideoManagerFixture(t, "http", 1)
			configure(fixture)
			batch := fixture.createBatch("one", "http-preset", 1, 1)
			created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			fixture.waitTerminal([]Attempt{created})
			attempt, _ := fixture.manager.GetAttempt(created.ID)
			if attempt.State != AttemptFailed || attempt.OutputAssetID != "" || attempt.Error.Code != "invalid_result" {
				t.Fatalf("invalid HTTP result attempt = %#v", attempt)
			}
		})
	}
}

// This fails if the bounded base64 reader rejects an exactly-sized result, or
// if a valid base64 prefix followed by trailing garbage reaches Asset.Import.
func TestManagerEnforcesExactHTTPResultLimitAndStrictBase64(t *testing.T) {
	for name, configure := range map[string]func(*videoManagerFixture){
		"exact limit": func(fixture *videoManagerFixture) {
			videos := fixture.config.Snapshot().Videos
			videos.HTTPProviders[0].MaxVideoBytes = 4
			if _, err := fixture.config.UpdateVideos(videos); err != nil {
				t.Fatal(err)
			}
			fixture.remote.completeVideo(validWebM()[:4], "video/webm", 16, 2)
		},
		"trailing base64": func(fixture *videoManagerFixture) {
			fixture.remote.setRawResult(base64.StdEncoding.EncodeToString(validWebM()) + "!")
		},
		"invalid magic": func(fixture *videoManagerFixture) {
			fixture.remote.completeVideo([]byte("not-a-video"), "video/webm", 16, 2)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newVideoManagerFixture(t, "http", 1)
			configure(fixture)
			batch := fixture.createBatch("one", "http-preset", 1, 1)
			created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			fixture.waitTerminal([]Attempt{created})
			attempt, _ := fixture.manager.GetAttempt(created.ID)
			if name == "exact limit" {
				if attempt.State != AttemptSucceeded || attempt.OutputAssetID == "" {
					t.Fatalf("exact-limit result = %#v", attempt)
				}
				return
			}
			if attempt.State != AttemptFailed || attempt.OutputAssetID != "" || attempt.Error.Code != "invalid_result" {
				t.Fatalf("invalid result = %#v", attempt)
			}
		})
	}
}

// This fails if cancellation of an item still waiting behind a batch limit
// contacts the remote service rather than ending only the local queued record.
func TestManagerCancelsQueuedAttemptWithoutRemoteRequest(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	batch := fixture.createBatch("one", "http-preset", 1, 2)
	fixture.remote.blockSubmissions()
	attempts, err := fixture.manager.StartBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.remote.waitForActive(t, 1)
	if err := fixture.manager.Cancel(attempts[1].ID); err != nil {
		t.Fatal(err)
	}
	cancelled, _ := fixture.manager.GetAttempt(attempts[1].ID)
	if cancelled.State != AttemptCancelled || fixture.remote.cancelCount() != 0 {
		t.Fatalf("queued cancellation = %#v, remote cancels = %d", cancelled, fixture.remote.cancelCount())
	}
	fixture.remote.releaseAll()
	fixture.waitTerminal([]Attempt{attempts[0]})
}

// This fails if dispatcher-side queued cancellation bypasses the per-run
// lifecycle lock and can race the public Cancel transition.
func TestManagerSerializesQueuedCancellation(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	batch := fixture.createBatch("one", "http-preset", 1, 2)
	fixture.remote.blockSubmissions()
	attempts, err := fixture.manager.StartBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.remote.waitForActive(t, 1)
	fixture.manager.mu.RLock()
	run := fixture.manager.attempts[attempts[1].ID]
	fixture.manager.mu.RUnlock()
	if run == nil {
		t.Fatal("queued run is missing")
	}
	run.lifecycle.Lock()
	run.cancel()
	time.Sleep(50 * time.Millisecond)
	blocked, _ := fixture.manager.GetAttempt(attempts[1].ID)
	if blocked.State != AttemptQueued {
		run.lifecycle.Unlock()
		t.Fatalf("queued cancellation bypassed lifecycle lock: %#v", blocked)
	}
	run.lifecycle.Unlock()
	fixture.waitTerminal([]Attempt{attempts[1]})
	fixture.remote.releaseAll()
	fixture.waitTerminal([]Attempt{attempts[0]})
}

// This fails if an ordinary remote cancellation transport error converts a
// still-running job into failed instead of retaining truthful polling state.
func TestManagerRetainsPollingAfterRemoteCancelTransportFailure(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	fixture.remote.setJobStatus("generating", 2)
	fixture.remote.setCancelError(errors.New("temporary cancel network failure"))
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitForState(created.ID, AttemptPolling)
	if err := fixture.manager.Cancel(created.ID); err == nil {
		t.Fatal("Cancel accepted a remote transport failure")
	}
	current, _ := fixture.manager.GetAttempt(created.ID)
	if current.State != AttemptPolling || current.RemoteStatus != "generating" {
		t.Fatalf("attempt after cancel transport failure = %#v", current)
	}
	fixture.remote.setCancelError(nil)
	fixture.remote.completeVideo(validWebM(), "video/webm", 16, 2)
	fixture.waitTerminal([]Attempt{created})
}

// This fails if cancellation inherits the provider's potentially day-long
// connection timeout, or loses the caller's cancelled request context.
func TestManagerCancelContextStopsBlockedRemoteCancellationPromptly(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	fixture.remote.setJobStatus("generating", 1)
	fixture.remote.setCancelDelay(time.Minute)
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitForState(created.ID, AttemptPolling)
	cancelEntered := fixture.remote.observeCancel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fixture.manager.CancelContext(ctx, created.ID) }()
	select {
	case <-cancelEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("remote cancellation was not attempted")
	}
	started := time.Now()
	cancel()
	select {
	case err = <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("CancelContext did not stop promptly after request cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CancelContext error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("CancelContext blocked for %s after request cancellation", elapsed)
	}
	if fixture.remote.cancelCount() != 1 {
		t.Fatalf("remote cancel count = %d, want 1", fixture.remote.cancelCount())
	}
}

// This fails if a completion path holding the lifecycle mutex can retain a
// cancelled HTTP handler before the cancellation-specific timeout begins.
func TestManagerCancelContextDoesNotWaitForLifecycleLockAfterRequestCancellation(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	fixture.remote.setJobStatus("generating", 1)
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitForState(created.ID, AttemptPolling)
	fixture.manager.mu.RLock()
	run := fixture.manager.attempts[created.ID]
	fixture.manager.mu.RUnlock()
	if run == nil {
		t.Fatal("run is missing")
	}
	run.lifecycle.Lock()
	defer run.lifecycle.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fixture.manager.CancelContext(ctx, created.ID) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CancelContext error = %v, want context.Canceled", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("CancelContext waited for the lifecycle mutex after request cancellation")
	}
}

// This fails if StartBatch silently skips an enabled Item with an active
// Attempt; the caller must receive the conflict before new work is scheduled.
func TestManagerRejectsBatchWithActiveEnabledItem(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	fixture.remote.blockSubmissions()
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.remote.waitForActive(t, 1)
	if _, err := fixture.manager.StartBatch(batch.ID); !errors.Is(err, ErrActiveAttempt) {
		t.Fatalf("StartBatch active item error = %v, want ErrActiveAttempt", err)
	}
	fixture.remote.releaseAll()
	fixture.waitTerminal([]Attempt{created})
}

// This fails if a CLI template failure panics its scheduler goroutine or loses
// the original expansion error while persisting a terminal attempt.
func TestManagerRecordsCLIExpansionFailureWithoutPanic(t *testing.T) {
	fixture := newVideoManagerFixture(t, "local_cli", 1)
	videos := fixture.config.Snapshot().Videos
	videos.CLIPresets[0].CommandTemplate = "{{EXTRA_ARGS_RAW}}"
	if _, err := fixture.config.UpdateVideos(videos); err != nil {
		t.Fatal(err)
	}
	batch := fixture.createBatch("one", "local-cli", 1, 1)
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitTerminal([]Attempt{created})
	failed, _ := fixture.manager.GetAttempt(created.ID)
	if failed.State != AttemptFailed || failed.Error.Code != "template_failed" || failed.Error.Message == "" {
		t.Fatalf("template failure attempt = %#v", failed)
	}
}

// This fails if the CLI scheduler does not prepare the fixed workspace, run
// the declared output command, and import exactly that video as an archive.
func TestManagerRunsCLIWorkspaceAndImportsDeclaredVideo(t *testing.T) {
	fixture := newVideoManagerFixture(t, "local_cli", 1)
	batch := fixture.createBatch("one", "local-cli", 1, 1)
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitTerminal([]Attempt{created})
	attempt, ok := fixture.manager.GetAttempt(created.ID)
	if !ok || attempt.State != AttemptSucceeded || attempt.WorkspaceRelativePath != attempt.ID || attempt.OutputAssetID == "" {
		t.Fatalf("CLI attempt = %#v", attempt)
	}
	stored, ok := fixture.assets.Get(attempt.OutputAssetID)
	if !ok || stored.State != asset.StateArchive || stored.Source != "videogen:"+attempt.ID {
		t.Fatalf("stored CLI result = %#v", stored)
	}
	contents, err := os.ReadFile(filepath.Join(fixture.workspace.root, attempt.ID, "manifest.json"))
	if err != nil || strings.Contains(string(contents), "prompt") {
		t.Fatalf("workspace manifest = %q, err = %v", contents, err)
	}
}

// This fails if a workspace preparation failure leaves an attempt active or a
// bad declared CLI output is imported after the executor rejects its format.
func TestManagerFailsCLIWorkspaceAndOutputValidation(t *testing.T) {
	for name, configure := range map[string]func(*videoManagerFixture){
		"workspace": func(fixture *videoManagerFixture) { fixture.workspace.root = "" },
		"output": func(fixture *videoManagerFixture) {
			videos := fixture.config.Snapshot().Videos
			videos.CLIPresets[0].CommandTemplate = "printf not-a-webm > {{OUTPUT_PATH}}"
			if _, err := fixture.config.UpdateVideos(videos); err != nil {
				panic(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newVideoManagerFixture(t, "local_cli", 1)
			configure(fixture)
			batch := fixture.createBatch("one", "local-cli", 1, 1)
			created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			fixture.waitTerminal([]Attempt{created})
			attempt, _ := fixture.manager.GetAttempt(created.ID)
			if attempt.State != AttemptFailed || attempt.OutputAssetID != "" {
				t.Fatalf("invalid CLI attempt = %#v", attempt)
			}
		})
	}
}

// This fails if result import follows a process-supplied path instead of the
// exact output path that was reserved before the CLI was started.
func TestManagerRejectsCLIRunResultWithDifferentOutputPath(t *testing.T) {
	fixture := newVideoManagerFixture(t, "local_cli", 1)
	_, err := fixture.manager.importCLIResult(&videoAttemptRun{}, Attempt{}, CLIRunResult{
		OutputPath: filepath.Join(t.TempDir(), "other.webm"),
		Request: CLIRunRequest{
			WorkspaceRoot: t.TempDir(), OutputPath: filepath.Join(t.TempDir(), "outputs", "result.webm"),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "different") {
		t.Fatalf("mismatched CLI output error = %v", err)
	}
}

// This fails if cancelling a local CLI Attempt leaves the persisted state
// running while its process group is being stopped.
func TestManagerCancelsCLIProcessAttempt(t *testing.T) {
	fixture := newVideoManagerFixture(t, "local_cli", 1)
	videos := fixture.config.Snapshot().Videos
	videos.CLIPresets[0].CommandTemplate = "trap 'exit 0' TERM; while :; do sleep 1; done"
	if _, err := fixture.config.UpdateVideos(videos); err != nil {
		t.Fatal(err)
	}
	batch := fixture.createBatch("one", "local-cli", 1, 1)
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitForState(created.ID, AttemptRunning)
	if err := fixture.manager.Cancel(created.ID); err != nil {
		t.Fatal(err)
	}
	fixture.waitTerminal([]Attempt{created})
	attempt, _ := fixture.manager.GetAttempt(created.ID)
	if attempt.State != AttemptCancelled {
		t.Fatalf("cancelled CLI attempt = %#v", attempt)
	}
}

func TestManagerPersistsLongRunningCLIPID(t *testing.T) {
	fixture := newVideoManagerFixture(t, "local_cli", 1)
	videos := fixture.config.Snapshot().Videos
	videos.CLIPresets[0].CommandTemplate = "trap 'exit 0' TERM; while :; do sleep 1; done"
	if _, err := fixture.config.UpdateVideos(videos); err != nil {
		t.Fatal(err)
	}
	batch := fixture.createBatch("pid", "local-cli", 1, 1)
	attempt, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, ok := fixture.manager.GetAttempt(attempt.ID)
		if ok && current.State == AttemptRunning && current.PID > 0 {
			if err := fixture.manager.Cancel(attempt.ID); err != nil {
				t.Fatal(err)
			}
			fixture.waitTerminal([]Attempt{attempt})
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("long-running CLI PID was not persisted: %#v", attempt)
}

// This fails if a local CLI timeout is persisted as a generic failure rather
// than the explicit timeout terminal state exposed to a Batch client.
func TestManagerRecordsCLITimeout(t *testing.T) {
	fixture := newVideoManagerFixture(t, "local_cli", 1)
	videos := fixture.config.Snapshot().Videos
	videos.CLIPresets[0].CommandTemplate = "while :; do sleep 1; done"
	videos.CLIPresets[0].TimeoutSeconds = 1
	if _, err := fixture.config.UpdateVideos(videos); err != nil {
		t.Fatal(err)
	}
	batch := fixture.createBatch("one", "local-cli", 1, 1)
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitTerminal([]Attempt{created})
	attempt, _ := fixture.manager.GetAttempt(created.ID)
	if attempt.State != AttemptFailed || attempt.Error.Code != "timeout" {
		t.Fatalf("timed out CLI attempt = %#v", attempt)
	}
}

// This fails if preset-controlled environment values can override the stable
// workspace paths, or if selected Asset metadata is not deterministic JSON.
func TestManagerBuildsReservedCLIEnvironmentAndSelectedAssetsJSON(t *testing.T) {
	fixture := newVideoManagerFixture(t, "local_cli", 1)
	preset := videoManagerCLIPreset()
	preset.Env = map[string]string{"OUTPUT_PATH": "unsafe", "WORKSPACE_DIR": "unsafe"}
	run := &videoAttemptRun{attemptID: "attempt", preset: videoPreset{kind: videoconfig.ExecutionLocalCLI, cli: &preset}}
	root := t.TempDir()
	workspace := Workspace{Root: root, InputDir: filepath.Join(root, "inputs"), OutputDir: filepath.Join(root, "outputs"), ManifestPath: filepath.Join(root, "manifest.json"), OutputPath: filepath.Join(root, "outputs", "result.webm"), Inputs: []StagedInput{
		{AssetID: "asset-control", Role: "control", Order: 0, Path: filepath.Join(root, "inputs", "control.png")},
		{AssetID: "asset-second", Role: "reference_video", Order: 1, Path: filepath.Join(root, "inputs", "second.mp4")},
		{AssetID: "asset-first", Role: "reference_image", Order: 2, Path: filepath.Join(root, "inputs", "first.png")},
	}}
	snapshot := Snapshot{Params: json.RawMessage(`{"extra_args_raw":"--trusted raw"}`), Timing: ResolvedTiming{FPS: 16, RequestedFrames: 24}, InputAssets: []AssetSnapshot{
		{ID: "asset-control", Role: "control", Order: 0}, {ID: "asset-second", Role: "reference_video", Order: 1}, {ID: "asset-first", Role: "reference_image", Order: 2},
	}}
	request, err := fixture.manager.cliRequest(run, workspace, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if request.Env["OUTPUT_PATH"] != workspace.OutputPath || request.Env["WORKSPACE_DIR"] != workspace.Root || request.Env["EXTRA_ARGS_RAW"] != "--trusted raw" {
		t.Fatalf("reserved CLI environment = %#v", request.Env)
	}
	var selected []struct {
		AssetID string `json:"asset_id"`
		Role    string `json:"role"`
		Order   int    `json:"order"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(request.Env["SELECTED_ASSETS_JSON"]), &selected); err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[0].AssetID != "asset-second" || selected[0].Order != 1 || selected[0].Path != workspace.Inputs[1].Path || selected[1].AssetID != "asset-first" || selected[1].Order != 2 {
		t.Fatalf("selected assets JSON = %#v", selected)
	}
}

// This fails if a batch subscription omits the current item snapshot before
// later state events, forcing reconnecting clients to guess existing state.
func TestManagerSubscribesCurrentSnapshotBeforeStateEvents(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	batch := fixture.createBatch("one", "http-preset", 1, 2)
	fixture.remote.blockSubmissions()
	created, err := fixture.manager.StartBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe, err := fixture.manager.SubscribeBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	select {
	case event := <-events:
		if event.Type != "snapshot" || event.BatchID != batch.ID || event.Attempt.ID != created[0].ID || len(event.Attempts) != 2 || event.Attempts[0].ID != created[0].ID || event.Attempts[1].ID != created[1].ID {
			t.Fatalf("first event = %#v", event)
		}
		select {
		case extra := <-events:
			if extra.Type == "snapshot" {
				t.Fatalf("subscription split atomic snapshot into multiple events: %#v", extra)
			}
		case <-time.After(30 * time.Millisecond):
		}
	case <-time.After(time.Second):
		t.Fatal("subscription did not receive its snapshot")
	}
	fixture.remote.releaseAll()
	fixture.waitTerminal(created)
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if event.Type == "state" {
				if event.BatchID != batch.ID || event.Attempt.ID == "" || len(event.Attempts) != 0 {
					t.Fatalf("state event = %#v", event)
				}
				return
			}
		case <-deadline:
			t.Fatal("subscription did not receive a BatchID-bearing state event")
		}
	}
}

// This fails if manual CLI-log saving or terminal workspace cleanup can reach
// paths outside the video workspace, or if a valid completed attempt is denied.
func TestManagerSavesCLILogAndCleansTerminalWorkspace(t *testing.T) {
	fixture := newVideoManagerFixture(t, "local_cli", 1)
	batch := fixture.createBatch("one", "local-cli", 1, 1)
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitTerminal([]Attempt{created})
	path, err := fixture.manager.SaveCLILog(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, filepath.Dir(fixture.workspace.root)+string(filepath.Separator)) {
		t.Fatalf("saved log path escaped data root: %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("saved log missing: %v", err)
	}
	snapshot, chunks, unsubscribe, err := fixture.manager.SubscribeCLILog(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	unsubscribe()
	if len(chunks) != 0 || !strings.Contains(string(snapshot.Data), "cli-log") {
		t.Fatalf("CLI log snapshot = %#v", snapshot)
	}
	if err := fixture.manager.CleanupWorkspace(created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fixture.workspace.root, created.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace remains after cleanup: %v", err)
	}
}

// This fails if shutdown only cancels its local context for a known HTTP job
// instead of sending the server's official cancellation request and recording
// the terminal local result.
func TestManagerShutdownCancelsKnownHTTPJob(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	fixture.remote.setJobStatus("generating", 2)
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitForState(created.ID, AttemptPolling)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := fixture.manager.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if got := fixture.remote.cancelCount(); got != 1 {
		t.Fatalf("remote cancel requests = %d, want 1", got)
	}
	stopped, _ := fixture.manager.GetAttempt(created.ID)
	if stopped.State != AttemptCancelled {
		t.Fatalf("attempt after shutdown = %#v", stopped)
	}
}

// This fails if Shutdown's bounded 409 path closes its manager before
// cancelling local poll contexts, which leaves a detached polling goroutine.
func TestManagerShutdownStopsPollingAfterRemoteCancelConflict(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	fixture.remote.setJobStatus("generating", 2)
	fixture.remote.setCancelError(&sdcpp.HTTPError{StatusCode: 409, Body: "cannot_cancel_generating"})
	created, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitForState(created.ID, AttemptPolling)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := fixture.manager.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	polling, _ := fixture.manager.GetAttempt(created.ID)
	if polling.State != AttemptPolling {
		t.Fatalf("shutdown falsely terminalized 409 attempt: %#v", polling)
	}
	before := fixture.remote.jobCount()
	time.Sleep(150 * time.Millisecond)
	if after := fixture.remote.jobCount(); after != before {
		t.Fatalf("HTTP polling continued after shutdown: before=%d after=%d", before, after)
	}
}

// This fails if Shutdown serializes per-attempt remote cancellation using
// independent background timeouts instead of honoring its caller deadline.
func TestManagerShutdownBoundsParallelRemoteCancellation(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 3)
	batch := fixture.createBatch("one", "http-preset", 3, 3)
	fixture.remote.setJobStatus("generating", 1)
	attempts, err := fixture.manager.StartBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		fixture.waitForState(attempt.ID, AttemptPolling)
	}
	fixture.remote.setCancelDelay(250 * time.Millisecond)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = fixture.manager.Shutdown(shutdownCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("shutdown exceeded global deadline: %v", elapsed)
	}
}

// This covers the Manager-level recovery boundary: Repository.Open converts
// an abandoned active record to interrupted, and the new Manager can retry it.
func TestManagerReopenExposesInterruptedAttemptAndPermitsRetry(t *testing.T) {
	fixture := newVideoManagerFixture(t, "http", 1)
	batch := fixture.createBatch("one", "http-preset", 1, 1)
	preset, err := fixture.manager.lookupPreset(batch)
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := fixture.manager.preflight(batch, batch.Items[0], preset)
	if err != nil {
		t.Fatal(err)
	}
	abandoned, err := fixture.service.CreateAttempt(batch.ID, batch.Items[0].ID, CreateAttemptInput{State: AttemptQueued, Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := OpenRepository(fixture.service.repository.root)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(repository, fixture.assets)
	manager := NewManager(fixture.config, service, NewHTTPAssembler(fixture.assets), newVideoRemoteFake(), fixture.workspace, NewCLIExecutor(), fixture.assets)
	defer func() { _ = manager.Shutdown(context.Background()) }()
	got, ok := manager.GetAttempt(abandoned.ID)
	if !ok || got.State != AttemptInterrupted {
		t.Fatalf("reopened attempt = %#v, exists=%v", got, ok)
	}
	retried, err := manager.Retry(abandoned.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, ok := manager.GetAttempt(retried.ID)
		if ok && terminalAttemptState(got.State) {
			if got.State != AttemptSucceeded {
				t.Fatalf("retried attempt = %#v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retried attempt did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type videoManagerFixture struct {
	config    *config.Repository
	assets    *asset.Repository
	service   *Service
	manager   *Manager
	remote    *videoRemoteFake
	workspace *WorkspaceManager
}

func newVideoManagerFixture(t *testing.T, kind string, maximumConcurrent int) *videoManagerFixture {
	t.Helper()
	root := t.TempDir()
	assets, err := asset.OpenRepository(filepath.Join(root, "assets.json"), filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := OpenRepository(filepath.Join(root, "batches"))
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	provider := videoconfig.Default().HTTPProviders[0]
	provider.ID, provider.Name, provider.BaseURL = "http-preset", "HTTP preset", "http://127.0.0.1:1234"
	provider.MaxConcurrentJobs = maximumConcurrent
	provider.PollIntervalMilliseconds = 100
	videos := videoconfig.Config{HTTPProviders: []videoconfig.HTTPProvider{provider}}
	if kind == "local_cli" {
		videos.HTTPProviders = nil
		videos.CLIPresets = []videoconfig.CLIPreset{videoManagerCLIPreset()}
	}
	if _, err := configuration.UpdateVideos(videos); err != nil {
		t.Fatal(err)
	}
	service := NewService(repository, assets)
	remote := newVideoRemoteFake()
	workspace := NewWorkspaceManager(filepath.Join(root, "video-workspace"), assets)
	manager := NewManager(configuration, service, NewHTTPAssembler(assets), remote, workspace, NewCLIExecutor(), assets)
	t.Cleanup(func() {
		remote.releaseAll()
		_ = manager.Shutdown(context.Background())
	})
	return &videoManagerFixture{config: configuration, assets: assets, service: service, manager: manager, remote: remote, workspace: workspace}
}

func (fixture *videoManagerFixture) createBatch(title, presetID string, concurrency, count int) Batch {
	fixture.manager.mu.RLock()
	// The fixture's manager has no bearing on construction; this only makes
	// accidental concurrent fixture use obvious under the race detector.
	fixture.manager.mu.RUnlock()
	kind := videoconfig.ExecutionHTTP
	if presetID == "local-cli" {
		kind = videoconfig.ExecutionLocalCLI
	}
	batch, err := fixture.service.CreateBatch(CreateBatchInput{
		Title: title, Folder: "test", PresetID: presetID, ExecutionKind: kind, Concurrency: concurrency,
		CommonParams: json.RawMessage(`{}`), Timing: TimingInput{Mode: "frames", VideoFrames: 2, FPS: 16},
	})
	if err != nil {
		panic(fmt.Sprintf("create batch %q: %v", title, err))
	}
	items := make([]CreateItemInput, count)
	for index := range items {
		items[index] = CreateItemInput{Prompt: fmt.Sprintf("prompt-%d", index), Enabled: true}
	}
	if _, err := fixture.service.CreateItems(batch.ID, items); err != nil {
		panic(fmt.Sprintf("create batch items: %v", err))
	}
	created, ok := fixture.service.Get(batch.ID)
	if !ok {
		panic("created batch is missing")
	}
	return created
}

func (fixture *videoManagerFixture) waitTerminal(attempts []Attempt) {
	deadline := time.Now().Add(3 * time.Second)
	for _, created := range attempts {
		for {
			attempt, ok := fixture.manager.GetAttempt(created.ID)
			if ok && terminalAttemptState(attempt.State) {
				break
			}
			if time.Now().After(deadline) {
				panic(fmt.Sprintf("attempt %s did not finish", created.ID))
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func (fixture *videoManagerFixture) waitForState(attemptID string, want AttemptState) {
	deadline := time.Now().Add(3 * time.Second)
	for {
		attempt, ok := fixture.manager.GetAttempt(attemptID)
		if ok && attempt.State == want {
			return
		}
		if time.Now().After(deadline) {
			panic(fmt.Sprintf("attempt %s did not reach %s", attemptID, want))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func videoManagerCLIPreset() videoconfig.CLIPreset {
	return videoconfig.CLIPreset{
		ID: "local-cli", Name: "Local CLI", Enabled: true, ExecutionKind: videoconfig.ExecutionLocalCLI,
		CommandTemplate: "printf cli-log; printf '\\x1a\\x45\\xdf\\xa3fixture' > {{OUTPUT_PATH}}", WorkDir: "/tmp", Env: map[string]string{},
		TimeoutSeconds: 2, StopGraceSeconds: 0, LogBufferBytes: 1024, OutputRelativePath: "outputs/result.webm",
		OutputMediaType: "video/webm", OutputExtension: ".webm", MaxOutputBytes: 1024, DefaultParams: json.RawMessage(`{}`),
	}
}

type videoRemoteFake struct {
	mu          sync.Mutex
	block       bool
	release     chan struct{}
	active      int
	maximum     int
	submits     int
	prompts     []string
	completed   sdcpp.VideoJob
	cancelErr   error
	cancelDelay time.Duration
	cancels     int
	cancelEntry chan struct{}
	jobs        int
	jobEntered  chan struct{}
	jobRelease  chan struct{}
}

func (remote *videoRemoteFake) setJobStatus(status string, position int) {
	remote.mu.Lock()
	remote.completed.Status, remote.completed.QueuePosition, remote.completed.Result = status, position, nil
	remote.mu.Unlock()
}

func (remote *videoRemoteFake) completeVideo(contents []byte, mediaType string, fps, frames int) {
	remote.mu.Lock()
	remote.completed = completedVideoJob("job", contents, mediaType, fps, frames)
	remote.mu.Unlock()
}

func (remote *videoRemoteFake) setRawResult(value string) {
	remote.mu.Lock()
	remote.completed.Result.B64JSON = value
	remote.mu.Unlock()
}

func (remote *videoRemoteFake) setCancelError(err error) {
	remote.mu.Lock()
	remote.cancelErr = err
	remote.mu.Unlock()
}

func (remote *videoRemoteFake) setCancelDelay(delay time.Duration) {
	remote.mu.Lock()
	remote.cancelDelay = delay
	remote.mu.Unlock()
}

func (remote *videoRemoteFake) observeCancel() <-chan struct{} {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	remote.cancelEntry = make(chan struct{}, 1)
	return remote.cancelEntry
}

func newVideoRemoteFake() *videoRemoteFake {
	return &videoRemoteFake{release: make(chan struct{}), completed: completedVideoJob("job", validWebM(), "video/webm", 16, 2)}
}

func (remote *videoRemoteFake) Submit(ctx context.Context, _ videoconfig.HTTPProvider, body []byte) (sdcpp.VideoSubmission, error) {
	var request struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return sdcpp.VideoSubmission{}, err
	}
	remote.mu.Lock()
	remote.active++
	if remote.active > remote.maximum {
		remote.maximum = remote.active
	}
	remote.submits++
	jobID := fmt.Sprintf("job-%d", remote.submits)
	remote.prompts = append(remote.prompts, request.Prompt)
	block, release := remote.block, remote.release
	remote.mu.Unlock()
	if block {
		select {
		case <-release:
		case <-ctx.Done():
			remote.finishSubmit()
			return sdcpp.VideoSubmission{}, ctx.Err()
		}
	}
	remote.finishSubmit()
	return sdcpp.VideoSubmission{ID: jobID, Kind: "vid_gen", Status: "queued"}, nil
}

func (remote *videoRemoteFake) Job(_ context.Context, _ videoconfig.HTTPProvider, jobID string) (sdcpp.VideoJob, error) {
	remote.mu.Lock()
	remote.jobs++
	job := remote.completed
	job.ID = jobID
	entered, release := remote.jobEntered, remote.jobRelease
	remote.mu.Unlock()
	if release != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	return job, nil
}

func (remote *videoRemoteFake) blockCompletedVideo(contents []byte, mediaType string, fps, frames int) (<-chan struct{}, func()) {
	remote.mu.Lock()
	remote.completed = completedVideoJob("job", contents, mediaType, fps, frames)
	remote.jobEntered = make(chan struct{}, 1)
	remote.jobRelease = make(chan struct{})
	entered, release := remote.jobEntered, remote.jobRelease
	remote.mu.Unlock()
	var once sync.Once
	return entered, func() { once.Do(func() { close(release) }) }
}

func (remote *videoRemoteFake) jobCount() int {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	return remote.jobs
}

func (remote *videoRemoteFake) Cancel(ctx context.Context, _ videoconfig.HTTPProvider, _ string) error {
	remote.mu.Lock()
	remote.cancels++
	delay, err, entry := remote.cancelDelay, remote.cancelErr, remote.cancelEntry
	remote.mu.Unlock()
	if entry != nil {
		select {
		case entry <- struct{}{}:
		default:
		}
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (remote *videoRemoteFake) cancelCount() int {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	return remote.cancels
}

func (remote *videoRemoteFake) blockSubmissions() {
	remote.mu.Lock()
	remote.block = true
	remote.mu.Unlock()
}

func (remote *videoRemoteFake) releaseAll() {
	remote.mu.Lock()
	select {
	case <-remote.release:
	default:
		close(remote.release)
	}
	remote.mu.Unlock()
}

func (remote *videoRemoteFake) waitForActive(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		remote.mu.Lock()
		got := remote.active
		remote.mu.Unlock()
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("active submissions = %d, want at least %d", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (remote *videoRemoteFake) finishSubmit() {
	remote.mu.Lock()
	remote.active--
	remote.mu.Unlock()
}

func (remote *videoRemoteFake) maximumActive() int {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	return remote.maximum
}

func (remote *videoRemoteFake) submitCount() int {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	return remote.submits
}

func (remote *videoRemoteFake) submittedPrompts() []string {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	return append([]string(nil), remote.prompts...)
}

func completedVideoJob(id string, contents []byte, mediaType string, fps, frames int) sdcpp.VideoJob {
	return sdcpp.VideoJob{ID: id, Kind: "vid_gen", Status: "completed", Result: &sdcpp.VideoJobResult{
		OutputFormat: "webm", MIMEType: mediaType, FPS: fps, FrameCount: frames, B64JSON: base64.StdEncoding.EncodeToString(contents),
	}}
}

func validWebM() []byte {
	return []byte{0x1a, 0x45, 0xdf, 0xa3, 0x93, 0x42, 0x82, 0x88, 'w', 'e', 'b', 'm'}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
