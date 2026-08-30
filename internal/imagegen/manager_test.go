package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
)

func TestManagerRunsBatchInOrderWithinProviderConcurrency(t *testing.T) {
	fixture := newManagerFixture(t, 1)
	batch := fixture.batchWithPrompts(t, 3, "one", "two", "three")
	attempts, err := fixture.manager.StartBatch(batch.ID)
	if err != nil || len(attempts) != 3 {
		t.Fatalf("attempts = %#v, error = %v", attempts, err)
	}
	fixture.remote.release(3)
	fixture.waitTerminal(t, attempts)
	if got := fixture.remote.submittedPrompts(); !reflect.DeepEqual(got, []string{"one", "two", "three"}) {
		t.Fatalf("submitted prompts = %#v", got)
	}
	if fixture.remote.maximumActive() != 1 {
		t.Fatalf("maximum active = %d", fixture.remote.maximumActive())
	}
}

func TestManagerSharesProviderSemaphoreAcrossBatches(t *testing.T) {
	fixture := newManagerFixture(t, 1)
	first := fixture.batchWithPrompts(t, 2, "one", "two")
	second := fixture.batchWithPrompts(t, 2, "three", "four")
	firstAttempts, err := fixture.manager.StartBatch(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondAttempts, err := fixture.manager.StartBatch(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.remote.release(4)
	fixture.waitTerminal(t, append(firstAttempts, secondAttempts...))
	if fixture.remote.maximumActive() != 1 {
		t.Fatalf("maximum active = %d", fixture.remote.maximumActive())
	}
}

func TestManagerHonorsLowerBatchConcurrency(t *testing.T) {
	fixture := newManagerFixture(t, 3)
	batch := fixture.batchWithPrompts(t, 1, "one", "two", "three")
	attempts, err := fixture.manager.StartBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.remote.release(3)
	fixture.waitTerminal(t, attempts)
	if fixture.remote.maximumActive() != 1 {
		t.Fatalf("maximum active = %d", fixture.remote.maximumActive())
	}
}

func TestManagerAppliesProviderConcurrencyUpdateWithoutReplacingActiveLimit(t *testing.T) {
	fixture := newManagerFixture(t, 1)
	first := fixture.batchWithPrompts(t, 1, "one")
	firstAttempts, err := fixture.manager.StartBatch(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.remote.waitSubmitted(t, 1)

	images := fixture.config.Snapshot().Images
	images.Providers[0].MaxConcurrentJobs = 2
	if _, err := fixture.config.UpdateImages(images); err != nil {
		t.Fatal(err)
	}
	second := fixture.batchWithPrompts(t, 2, "two", "three")
	secondAttempts, err := fixture.manager.StartBatch(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.remote.waitSubmitted(t, 1)
	time.Sleep(25 * time.Millisecond)
	if got := fixture.remote.maximumActive(); got != 2 {
		t.Fatalf("maximum active before release = %d", got)
	}

	fixture.remote.release(1)
	fixture.remote.waitSubmitted(t, 1)
	fixture.remote.release(2)
	fixture.waitTerminal(t, append(firstAttempts, secondAttempts...))
	if got := fixture.remote.maximumActive(); got != 2 {
		t.Fatalf("maximum active = %d", got)
	}
}

func TestManagerRejectsDisabledMissingProviderAndActiveItem(t *testing.T) {
	t.Run("disabled provider", func(t *testing.T) {
		fixture := newManagerFixture(t, 1)
		images := fixture.config.Snapshot().Images
		images.Providers[0].Enabled = false
		if _, err := fixture.config.UpdateImages(images); err != nil {
			t.Fatal(err)
		}
		batch := fixture.batchWithPrompts(t, 1, "one")
		if _, err := fixture.manager.StartBatch(batch.ID); !errors.Is(err, ErrImageProviderDisabled) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("missing provider", func(t *testing.T) {
		fixture := newManagerFixture(t, 1)
		batch, err := fixture.service.CreateBatch(CreateBatchInput{
			Title: "Missing", ProviderID: "missing", Concurrency: 1, BaseParams: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "one"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.manager.StartBatch(batch.ID); !errors.Is(err, ErrImageProviderNotFound) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("active item", func(t *testing.T) {
		fixture := newManagerFixture(t, 1)
		batch := fixture.batchWithPrompts(t, 1, "one")
		itemID := batch.Items[0].ID
		attempt, err := fixture.manager.StartItem(batch.ID, itemID)
		if err != nil {
			t.Fatal(err)
		}
		fixture.remote.waitSubmitted(t, 1)
		if _, err := fixture.manager.StartItem(batch.ID, itemID); !errors.Is(err, ErrActiveAttempt) {
			t.Fatalf("error = %v", err)
		}
		fixture.remote.release(1)
		fixture.waitTerminal(t, []Attempt{attempt})
	})
}

func TestManagerPersistsFailedPreflightAttempt(t *testing.T) {
	fixture := newManagerFixture(t, 1)
	input := importImage(t, fixture.assets, "input.png")
	batch, err := fixture.service.CreateBatch(validBatchInput())
	if err != nil {
		t.Fatal(err)
	}
	items, err := fixture.service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "cat", InputAssets: InputAssets{InitImageID: input.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	file, _, err := fixture.assets.OpenContent(input.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	_ = file.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	attempt, err := fixture.manager.StartItem(batch.ID, items[0].ID)
	if err == nil || attempt.State != AttemptFailed || attempt.Error.Code != "preflight_failed" {
		t.Fatalf("attempt = %#v, error = %v", attempt, err)
	}
	persisted, ok := fixture.manager.GetAttempt(attempt.ID)
	if !ok || persisted.State != AttemptFailed || persisted.Snapshot.Provider.Headers["Authorization"] != "" {
		t.Fatalf("persisted = %#v, ok = %v", persisted, ok)
	}
}

func TestManagerStartBatchContinuesAfterFailedPreflight(t *testing.T) {
	fixture := newManagerFixture(t, 1)
	input := importImage(t, fixture.assets, "input.png")
	batch, _ := fixture.service.CreateBatch(validBatchInput())
	items, _ := fixture.service.CreateItems(batch.ID, []CreateItemInput{
		{Prompt: "broken", InputAssets: InputAssets{InitImageID: input.ID}},
		{Prompt: "healthy"},
	})
	file, _, _ := fixture.assets.OpenContent(input.ID)
	path := file.Name()
	_ = file.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	attempts, err := fixture.manager.StartBatch(batch.ID)
	if err != nil || len(attempts) != 2 || attempts[0].State != AttemptFailed || attempts[0].Snapshot.Prompt != items[0].Prompt {
		t.Fatalf("attempts = %#v, error = %v", attempts, err)
	}
	fixture.remote.release(1)
	terminal := fixture.waitTerminal(t, attempts)
	if len(terminal) != 2 {
		t.Fatalf("terminal = %#v", terminal)
	}
}

func TestManagerImportsEveryCompletedImageAsArchiveAsset(t *testing.T) {
	remote := &resultRemote{format: "png", images: []sdcpp.JobImage{
		{Index: 0, B64JSON: base64.StdEncoding.EncodeToString(validPNG(t, color.RGBA{R: 255, A: 255}))},
		{Index: 1, B64JSON: base64.StdEncoding.EncodeToString(validPNG(t, color.RGBA{G: 255, A: 255}))},
	}}
	fixture := newManagerFixtureWithRemote(t, 1, remote)
	attempt := fixture.startOneAndWait(t)
	if attempt.State != AttemptSucceeded || len(attempt.ResultAssetIDs) != 2 {
		t.Fatalf("attempt = %#v", attempt)
	}
	for _, id := range attempt.ResultAssetIDs {
		item, ok := fixture.assets.Get(id)
		if !ok || item.State != asset.StateArchive || item.Source != "imagegen:"+attempt.ID || item.MediaType != "image/png" {
			t.Fatalf("result asset = %#v, ok = %v", item, ok)
		}
		assertReference(t, fixture.assets, id, "image_attempt", attempt.ID, true)
	}
}

func TestManagerImportsPNGJPEGAndWebPResults(t *testing.T) {
	tests := []struct {
		name, format, mediaType string
		contents                []byte
	}{
		{name: "png", format: "png", mediaType: "image/png", contents: validPNG(t, color.RGBA{R: 255, A: 255})},
		{name: "jpeg", format: "jpeg", mediaType: "image/jpeg", contents: validJPEG(t)},
		{name: "webp", format: "webp", mediaType: "image/webp", contents: validWebP()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &resultRemote{format: test.format, images: []sdcpp.JobImage{{Index: 0, B64JSON: base64.StdEncoding.EncodeToString(test.contents)}}}
			fixture := newManagerFixtureWithRemote(t, 1, remote)
			attempt := fixture.startOneAndWait(t)
			if attempt.State != AttemptSucceeded || len(attempt.ResultAssetIDs) != 1 {
				t.Fatalf("attempt = %#v", attempt)
			}
			result, _ := fixture.assets.Get(attempt.ResultAssetIDs[0])
			if result.MediaType != test.mediaType {
				t.Fatalf("media type = %q", result.MediaType)
			}
		})
	}
}

func TestManagerRejectsInvalidResult(t *testing.T) {
	tests := []struct {
		name, format, encoded string
		limit                 int64
	}{
		{name: "invalid base64", format: "png", encoded: "%%%"},
		{name: "unknown bytes", format: "png", encoded: base64.StdEncoding.EncodeToString([]byte("not an image"))},
		{name: "format mismatch", format: "png", encoded: base64.StdEncoding.EncodeToString(validJPEG(t))},
		{name: "single image limit", format: "png", encoded: base64.StdEncoding.EncodeToString(validPNG(t, color.RGBA{B: 255, A: 255})), limit: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &resultRemote{format: test.format, images: []sdcpp.JobImage{{Index: 0, B64JSON: test.encoded}}}
			fixture := newManagerFixtureWithRemote(t, 1, remote)
			if test.limit > 0 {
				images := fixture.config.Snapshot().Images
				images.Providers[0].MaxImageBytes = test.limit
				if _, err := fixture.config.UpdateImages(images); err != nil {
					t.Fatal(err)
				}
			}
			attempt := fixture.startOneAndWait(t)
			if attempt.State != AttemptFailed || len(attempt.ResultAssetIDs) != 0 || attempt.Error.Code == "" {
				t.Fatalf("attempt = %#v", attempt)
			}
		})
	}
}

func TestManagerRetainsPartialResultWhenLaterImageFails(t *testing.T) {
	remote := &resultRemote{format: "png", images: []sdcpp.JobImage{
		{Index: 0, B64JSON: base64.StdEncoding.EncodeToString(validPNG(t, color.RGBA{R: 255, A: 255}))},
		{Index: 1, B64JSON: "%%%"},
	}}
	fixture := newManagerFixtureWithRemote(t, 1, remote)
	attempt := fixture.startOneAndWait(t)
	if attempt.State != AttemptFailed || len(attempt.ResultAssetIDs) != 1 {
		t.Fatalf("attempt = %#v", attempt)
	}
	if _, ok := fixture.assets.Get(attempt.ResultAssetIDs[0]); !ok {
		t.Fatal("first imported asset missing")
	}
}

func TestManagerPreservesPartialResultsWhenAttachFails(t *testing.T) {
	remote := &resultRemote{format: "png", images: []sdcpp.JobImage{
		{Index: 0, B64JSON: base64.StdEncoding.EncodeToString(validPNG(t, color.RGBA{R: 255, A: 255}))},
		{Index: 1, B64JSON: base64.StdEncoding.EncodeToString(validPNG(t, color.RGBA{G: 255, A: 255}))},
	}}
	fixture := newManagerFixtureWithRemote(t, 1, remote)
	original := fixture.manager.attachResult
	attachments := 0
	fixture.manager.attachResult = func(batchID, itemID, attemptID, assetID string) (Attempt, error) {
		attachments++
		if attachments == 2 {
			return Attempt{}, errors.New("injected attach failure")
		}
		return original(batchID, itemID, attemptID, assetID)
	}
	attempt := fixture.startOneAndWait(t)
	if attempt.State != AttemptFailed || len(attempt.ResultAssetIDs) != 1 {
		t.Fatalf("attempt = %#v", attempt)
	}
	assertReference(t, fixture.assets, attempt.ResultAssetIDs[0], "image_attempt", attempt.ID, true)
}

func TestManagerRetriesTerminalPersistenceUntilStorageRecovers(t *testing.T) {
	remote := &resultRemote{format: "png", images: []sdcpp.JobImage{{Index: 0, B64JSON: base64.StdEncoding.EncodeToString(validPNG(t, color.RGBA{R: 255, A: 255}))}}}
	fixture := newManagerFixtureWithRemote(t, 1, remote)
	original := fixture.manager.persistAttempt
	failures := 4
	fixture.manager.persistAttempt = func(batchID, itemID, attemptID string, input UpdateAttemptInput) (Attempt, error) {
		if input.State == AttemptSucceeded && failures > 0 {
			failures--
			return Attempt{}, errors.New("injected storage failure")
		}
		return original(batchID, itemID, attemptID, input)
	}
	attempt := fixture.startOneAndWait(t)
	if attempt.State != AttemptSucceeded || failures != 0 {
		t.Fatalf("attempt = %#v, failures remaining = %d", attempt, failures)
	}
}

func TestManagerPreservesAttemptWhenPollingPersistenceFails(t *testing.T) {
	fixture := newManagerFixtureWithRemote(t, 1, &pollingFailureRemote{})
	original := fixture.manager.persistAttempt
	fixture.manager.persistAttempt = func(batchID, itemID, attemptID string, input UpdateAttemptInput) (Attempt, error) {
		if input.State == AttemptPolling && input.RemoteStatus == "generating" {
			return Attempt{}, errors.New("injected polling persistence failure")
		}
		return original(batchID, itemID, attemptID, input)
	}
	attempt := fixture.startOneAndWait(t)
	if attempt.State != AttemptFailed || attempt.ID == "" || attempt.Error.Code == "" {
		t.Fatalf("attempt = %#v", attempt)
	}
}

func TestManagerCancelCallsRemoteAndPublishesTerminalState(t *testing.T) {
	remote := newCancellableRemote(nil)
	fixture := newManagerFixtureWithRemote(t, 1, remote)
	batch := fixture.batchWithPrompts(t, 1, "one")
	attempt, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitRemoteID(t, attempt.ID)
	events, unsubscribe, err := fixture.manager.SubscribeBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if err := fixture.manager.Cancel(attempt.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := waitAttemptEventState(t, events, AttemptCancelled)
	if cancelled.ID != attempt.ID || remote.cancelCount() != 1 {
		t.Fatalf("cancelled = %#v, remote calls = %d", cancelled, remote.cancelCount())
	}
}

func TestManagerSerializesConcurrentCancellation(t *testing.T) {
	remote := newCancellableRemote(nil)
	fixture := newManagerFixtureWithRemote(t, 1, remote)
	batch := fixture.batchWithPrompts(t, 1, "one")
	attempt, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitRemoteID(t, attempt.ID)

	errorsSeen := make(chan error, 2)
	var callers sync.WaitGroup
	for range 2 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			errorsSeen <- fixture.manager.Cancel(attempt.ID)
		}()
	}
	callers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	terminal := fixture.waitTerminal(t, []Attempt{attempt})[0]
	if terminal.State != AttemptCancelled || remote.cancelCount() != 1 {
		t.Fatalf("attempt = %#v, cancel calls = %d", terminal, remote.cancelCount())
	}
}

func TestManagerRetriesCancellationPersistenceAfterWorkerStops(t *testing.T) {
	remote := newCancellableRemote(nil)
	fixture := newManagerFixtureWithRemote(t, 1, remote)
	batch := fixture.batchWithPrompts(t, 1, "one")
	attempt, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitRemoteID(t, attempt.ID)
	original := fixture.manager.persistAttempt
	failures := 4
	fixture.manager.persistAttempt = func(batchID, itemID, attemptID string, input UpdateAttemptInput) (Attempt, error) {
		if input.State == AttemptCancelled && failures > 0 {
			failures--
			return Attempt{}, errors.New("injected cancellation persistence failure")
		}
		return original(batchID, itemID, attemptID, input)
	}
	if err := fixture.manager.Cancel(attempt.ID); err != nil {
		t.Fatal(err)
	}
	terminal := fixture.waitTerminal(t, []Attempt{attempt})[0]
	if terminal.State != AttemptCancelled || failures != 0 {
		t.Fatalf("attempt = %#v, failures remaining = %d", terminal, failures)
	}
}

func TestManagerCancelQueuedAttemptWithoutRemoteCall(t *testing.T) {
	fixture := newManagerFixture(t, 1)
	batch := fixture.batchWithPrompts(t, 2, "one", "two", "three")
	attempts, err := fixture.manager.StartBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.remote.waitSubmitted(t, 1)
	if err := fixture.manager.Cancel(attempts[2].ID); err != nil {
		t.Fatal(err)
	}
	immediate, _ := fixture.manager.GetAttempt(attempts[2].ID)
	if immediate.State != AttemptCancelled {
		t.Fatalf("queued cancellation was not persisted immediately: %#v", immediate)
	}
	fixture.remote.release(2)
	terminal := fixture.waitTerminal(t, attempts)
	states := map[string]AttemptState{}
	for _, attempt := range terminal {
		states[attempt.ID] = attempt.State
	}
	if states[attempts[2].ID] != AttemptCancelled || fixture.remote.cancelCount() != 0 {
		t.Fatalf("states = %#v, cancel calls = %d", states, fixture.remote.cancelCount())
	}
}

func TestManagerCancelConflictAllowsCompletedJobImport(t *testing.T) {
	completed := sdcpp.Job{
		ID: "job-cancel", Kind: "img_gen", Status: "completed",
		Result: &sdcpp.JobResult{OutputFormat: "png", Images: []sdcpp.JobImage{{Index: 0, B64JSON: base64.StdEncoding.EncodeToString(validPNG(t, color.RGBA{R: 255, A: 255}))}}},
	}
	for _, status := range []int{404, 409, 410} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			remote := newCancellableRemote(&sdcpp.HTTPError{StatusCode: status, Body: "cancel race"})
			remote.result = completed
			remote.releaseOnCancel = true
			fixture := newManagerFixtureWithRemote(t, 1, remote)
			batch := fixture.batchWithPrompts(t, 1, "one")
			attempt, _ := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
			fixture.waitRemoteID(t, attempt.ID)
			if err := fixture.manager.Cancel(attempt.ID); err != nil {
				t.Fatal(err)
			}
			terminal := fixture.waitTerminal(t, []Attempt{attempt})[0]
			if terminal.State != AttemptSucceeded || len(terminal.ResultAssetIDs) != 1 {
				t.Fatalf("attempt = %#v", terminal)
			}
		})
	}
}

func TestManagerSubscribeBatchStartsWithLatestAttemptsInItemOrder(t *testing.T) {
	fixture := newManagerFixture(t, 1)
	batch := fixture.batchWithPrompts(t, 1, "one", "two", "three")
	attempts, err := fixture.manager.StartBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe, err := fixture.manager.SubscribeBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	for index, want := range attempts {
		select {
		case event := <-events:
			if event.Type != "snapshot" || event.Attempt.ID != want.ID {
				t.Fatalf("event %d = %#v", index, event)
			}
		case <-time.After(time.Second):
			t.Fatal("snapshot event missing")
		}
	}
	fixture.remote.release(3)
	fixture.waitTerminal(t, attempts)
}

func TestManagerSubscribeBatchDoesNotBlockOnSlowConsumer(t *testing.T) {
	remote := &resultRemote{format: "png", images: []sdcpp.JobImage{{Index: 0, B64JSON: base64.StdEncoding.EncodeToString(validPNG(t, color.RGBA{R: 255, A: 255}))}}}
	fixture := newManagerFixtureWithRemote(t, 1, remote)
	batch := fixture.batchWithPrompts(t, 1, "one")
	_, unsubscribe, err := fixture.manager.SubscribeBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	attempt, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	terminal := fixture.waitTerminal(t, []Attempt{attempt})[0]
	if terminal.State != AttemptSucceeded {
		t.Fatalf("attempt = %#v", terminal)
	}
}

func TestManagerShutdownRefusesNewJobsWaitsAndCancelsActive(t *testing.T) {
	remote := newCancellableRemote(nil)
	remote.releaseOnCancel = true
	remote.ignoreJobContext = false
	remote.result = sdcpp.Job{
		ID: "job-cancel", Kind: "img_gen", Status: "completed",
		Result: &sdcpp.JobResult{OutputFormat: "png", Images: []sdcpp.JobImage{{Index: 0, B64JSON: base64.StdEncoding.EncodeToString(validPNG(t, color.RGBA{R: 255, A: 255}))}}},
	}
	fixture := newManagerFixtureWithRemote(t, 1, remote)
	batch := fixture.batchWithPrompts(t, 1, "one")
	attempt, _ := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	fixture.waitRemoteID(t, attempt.ID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := fixture.manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID); !errors.Is(err, ErrImageManagerClosed) {
		t.Fatalf("start after shutdown error = %v", err)
	}
	got, ok := fixture.manager.GetAttempt(attempt.ID)
	if !ok || got.State != AttemptCancelled || remote.cancelCount() != 1 {
		t.Fatalf("attempt = %#v, ok = %v, cancel calls = %d", got, ok, remote.cancelCount())
	}
}

func TestManagerShutdownWaitsForAcceptedStartToFinish(t *testing.T) {
	fixture := newManagerFixture(t, 1)
	if err := fixture.manager.beginStart(); err != nil {
		t.Fatal(err)
	}
	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdownDone <- fixture.manager.Shutdown(ctx)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		fixture.manager.mu.RLock()
		accepting := fixture.manager.accepting
		fixture.manager.mu.RUnlock()
		if !accepting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shutdown did not stop accepting starts")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before accepted start finished: %v", err)
	default:
	}
	fixture.manager.starts.Done()
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
}

func TestManagerShutdownRetriesTerminalPersistence(t *testing.T) {
	remote := newCancellableRemote(nil)
	fixture := newManagerFixtureWithRemote(t, 1, remote)
	batch := fixture.batchWithPrompts(t, 1, "one")
	attempt, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitRemoteID(t, attempt.ID)
	original := fixture.manager.persistAttempt
	failures := 4
	fixture.manager.persistAttempt = func(batchID, itemID, attemptID string, input UpdateAttemptInput) (Attempt, error) {
		if input.State == AttemptCancelled && input.Error.Code == "shutdown" && failures > 0 {
			failures--
			return Attempt{}, errors.New("injected shutdown persistence failure")
		}
		return original(batchID, itemID, attemptID, input)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := fixture.manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	terminal, ok := fixture.manager.GetAttempt(attempt.ID)
	if !ok || terminal.State != AttemptCancelled || failures != 0 {
		t.Fatalf("attempt = %#v, ok = %v, failures remaining = %d", terminal, ok, failures)
	}
}

func TestManagerBoundsUnicodeErrorsAsValidUTF8(t *testing.T) {
	got := boundedErrorMessage(strings.Repeat("界", 2000))
	if len(got) > 4096 || !utf8.ValidString(got) {
		t.Fatalf("bounded error has %d bytes and valid=%v", len(got), utf8.ValidString(got))
	}
}

type managerFixture struct {
	manager *Manager
	remote  *blockingRemote
	config  *config.Repository
	service *Service
	assets  *asset.Repository
}

func newManagerFixture(t *testing.T, providerConcurrency int) managerFixture {
	t.Helper()
	remote := newBlockingRemote()
	fixture := newManagerFixtureWithRemote(t, providerConcurrency, remote)
	fixture.remote = remote
	return fixture
}

func newManagerFixtureWithRemote(t *testing.T, providerConcurrency int, remote RemoteClient) managerFixture {
	t.Helper()
	root := t.TempDir()
	configuration, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	images := configuration.Snapshot().Images
	images.Providers[0].MaxConcurrentJobs = providerConcurrency
	images.Providers[0].PollIntervalMilliseconds = 100
	if _, err := configuration.UpdateImages(images); err != nil {
		t.Fatal(err)
	}
	repository, err := OpenRepository(filepath.Join(root, "batches"))
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.OpenRepository(filepath.Join(root, "assets", "index.json"), filepath.Join(root, "assets", "files"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(repository, assets)
	manager := NewManager(configuration, service, NewAssembler(assets), assets, remote)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	return managerFixture{manager: manager, config: configuration, service: service, assets: assets}
}

func (fixture managerFixture) batchWithPrompts(t *testing.T, concurrency int, prompts ...string) Batch {
	t.Helper()
	batch, err := fixture.service.CreateBatch(CreateBatchInput{
		Title: "Draw", ProviderID: "sdcpp-local", Concurrency: concurrency, BaseParams: json.RawMessage(`{"output_format":"png"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]CreateItemInput, len(prompts))
	for index, prompt := range prompts {
		inputs[index] = CreateItemInput{Prompt: prompt}
	}
	if _, err := fixture.service.CreateItems(batch.ID, inputs); err != nil {
		t.Fatal(err)
	}
	batch, _ = fixture.service.Get(batch.ID)
	return batch
}

func (fixture managerFixture) waitTerminal(t *testing.T, attempts []Attempt) []Attempt {
	t.Helper()
	remaining := make(map[string]struct{}, len(attempts))
	for _, attempt := range attempts {
		remaining[attempt.ID] = struct{}{}
	}
	result := make([]Attempt, 0, len(attempts))
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for len(remaining) > 0 {
		select {
		case <-ticker.C:
			for id := range remaining {
				attempt, ok := fixture.manager.GetAttempt(id)
				if ok && terminalAttemptState(attempt.State) {
					result = append(result, attempt)
					delete(remaining, id)
				}
			}
		case <-timer.C:
			t.Fatalf("attempts did not finish: %#v", remaining)
		}
	}
	return result
}

func (fixture managerFixture) startOneAndWait(t *testing.T) Attempt {
	t.Helper()
	batch := fixture.batchWithPrompts(t, 1, "one")
	attempt, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return fixture.waitTerminal(t, []Attempt{attempt})[0]
}

func (fixture managerFixture) waitRemoteID(t *testing.T, attemptID string) Attempt {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ticker.C:
			attempt, ok := fixture.manager.GetAttempt(attemptID)
			if ok && attempt.RemoteJobID != "" {
				return attempt
			}
		case <-timer.C:
			t.Fatal("remote job ID not persisted")
		}
	}
}

func waitAttemptEventState(t *testing.T, events <-chan AttemptEvent, state AttemptState) Attempt {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("event stream closed")
			}
			if event.Attempt.State == state {
				return event.Attempt
			}
		case <-timer.C:
			t.Fatalf("event state %q not received", state)
		}
	}
}

type blockingRemote struct {
	mu          sync.Mutex
	releaseJobs chan struct{}
	submitted   chan struct{}
	prompts     []string
	active      int
	maxActive   int
	cancelCalls int
}

func newBlockingRemote() *blockingRemote {
	return &blockingRemote{releaseJobs: make(chan struct{}, 128), submitted: make(chan struct{}, 128)}
}

func (remote *blockingRemote) Submit(ctx context.Context, _ sdcpp.ImageProvider, body []byte) (sdcpp.Submission, error) {
	var request struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return sdcpp.Submission{}, err
	}
	remote.mu.Lock()
	remote.prompts = append(remote.prompts, request.Prompt)
	remote.active++
	if remote.active > remote.maxActive {
		remote.maxActive = remote.active
	}
	remote.mu.Unlock()
	remote.submitted <- struct{}{}
	select {
	case <-remote.releaseJobs:
	case <-ctx.Done():
		remote.finishActive()
		return sdcpp.Submission{}, ctx.Err()
	}
	remote.finishActive()
	return sdcpp.Submission{ID: "job-" + request.Prompt, Kind: "img_gen", Status: "queued"}, nil
}

func (remote *blockingRemote) Job(context.Context, sdcpp.ImageProvider, string) (sdcpp.Job, error) {
	return sdcpp.Job{
		ID: "unused", Kind: "img_gen", Status: "failed",
		Error: &sdcpp.RemoteError{Code: "fixture", Message: "finished"},
	}, nil
}

func (remote *blockingRemote) Cancel(context.Context, sdcpp.ImageProvider, string) error {
	remote.mu.Lock()
	remote.cancelCalls++
	remote.mu.Unlock()
	return nil
}

func (remote *blockingRemote) finishActive() {
	remote.mu.Lock()
	remote.active--
	remote.mu.Unlock()
}

func (remote *blockingRemote) release(count int) {
	for range count {
		remote.releaseJobs <- struct{}{}
	}
}

func (remote *blockingRemote) waitSubmitted(t *testing.T, count int) {
	t.Helper()
	for range count {
		select {
		case <-remote.submitted:
		case <-time.After(time.Second):
			t.Fatal("remote submission did not start")
		}
	}
}

func (remote *blockingRemote) submittedPrompts() []string {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	return append([]string(nil), remote.prompts...)
}

func (remote *blockingRemote) maximumActive() int {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	return remote.maxActive
}

func (remote *blockingRemote) cancelCount() int {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	return remote.cancelCalls
}

var _ RemoteClient = (*blockingRemote)(nil)

func (remote *blockingRemote) String() string {
	return fmt.Sprintf("blockingRemote{%v}", remote.submittedPrompts())
}

type resultRemote struct {
	format string
	images []sdcpp.JobImage
}

func (remote *resultRemote) Submit(context.Context, sdcpp.ImageProvider, []byte) (sdcpp.Submission, error) {
	return sdcpp.Submission{ID: "job-result", Kind: "img_gen", Status: "queued"}, nil
}

func (remote *resultRemote) Job(context.Context, sdcpp.ImageProvider, string) (sdcpp.Job, error) {
	return sdcpp.Job{
		ID: "job-result", Kind: "img_gen", Status: "completed",
		Result: &sdcpp.JobResult{OutputFormat: remote.format, Images: append([]sdcpp.JobImage(nil), remote.images...)},
	}, nil
}

func (remote *resultRemote) Cancel(context.Context, sdcpp.ImageProvider, string) error { return nil }

type pollingFailureRemote struct {
	mu    sync.Mutex
	reads int
}

func (remote *pollingFailureRemote) Submit(context.Context, sdcpp.ImageProvider, []byte) (sdcpp.Submission, error) {
	return sdcpp.Submission{ID: "job-persistence", Kind: "img_gen", Status: "queued"}, nil
}

func (remote *pollingFailureRemote) Job(context.Context, sdcpp.ImageProvider, string) (sdcpp.Job, error) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	remote.reads++
	if remote.reads == 1 {
		return sdcpp.Job{ID: "job-persistence", Kind: "img_gen", Status: "generating"}, nil
	}
	return sdcpp.Job{ID: "job-persistence", Kind: "img_gen", Status: "failed"}, nil
}

func (remote *pollingFailureRemote) Cancel(context.Context, sdcpp.ImageProvider, string) error {
	return nil
}

func validPNG(t *testing.T, pixel color.RGBA) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	canvas.Set(0, 0, pixel)
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func validJPEG(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	canvas.Set(0, 0, color.RGBA{R: 255, G: 128, A: 255})
	var output bytes.Buffer
	if err := jpeg.Encode(&output, canvas, nil); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func validWebP() []byte {
	return []byte{'R', 'I', 'F', 'F', 4, 0, 0, 0, 'W', 'E', 'B', 'P', 'V', 'P', '8', ' '}
}

type cancellableRemote struct {
	mu               sync.Mutex
	jobStarted       chan struct{}
	releaseJob       chan struct{}
	cancelErr        error
	cancelCalls      int
	releaseOnCancel  bool
	ignoreJobContext bool
	result           sdcpp.Job
	startOnce        sync.Once
	releaseOnce      sync.Once
}

func newCancellableRemote(cancelErr error) *cancellableRemote {
	return &cancellableRemote{
		jobStarted: make(chan struct{}), releaseJob: make(chan struct{}), cancelErr: cancelErr,
		result: sdcpp.Job{ID: "job-cancel", Kind: "img_gen", Status: "cancelled", Error: &sdcpp.RemoteError{Code: "cancelled", Message: "cancelled"}},
	}
}

func (remote *cancellableRemote) Submit(context.Context, sdcpp.ImageProvider, []byte) (sdcpp.Submission, error) {
	return sdcpp.Submission{ID: "job-cancel", Kind: "img_gen", Status: "queued"}, nil
}

func (remote *cancellableRemote) Job(ctx context.Context, _ sdcpp.ImageProvider, _ string) (sdcpp.Job, error) {
	remote.startOnce.Do(func() { close(remote.jobStarted) })
	if remote.ignoreJobContext {
		<-remote.releaseJob
		return remote.result, nil
	}
	select {
	case <-remote.releaseJob:
		return remote.result, nil
	case <-ctx.Done():
		return sdcpp.Job{}, ctx.Err()
	}
}

func (remote *cancellableRemote) Cancel(context.Context, sdcpp.ImageProvider, string) error {
	remote.mu.Lock()
	remote.cancelCalls++
	release := remote.releaseOnCancel
	err := remote.cancelErr
	remote.mu.Unlock()
	if release {
		remote.releaseOnce.Do(func() { close(remote.releaseJob) })
	}
	return err
}

func (remote *cancellableRemote) cancelCount() int {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	return remote.cancelCalls
}
