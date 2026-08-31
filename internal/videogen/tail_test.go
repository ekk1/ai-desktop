//go:build linux

package videogen

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

func TestTailExtractorImportsArchiveImageAndReferencesSource(t *testing.T) {
	extractor, assets := tailFixture(t, `printf '\x89PNG\r\n\x1a\nfixture' > "$OUTPUT_IMAGE"`)
	source := importFixtureAsset(t, assets, "video/webm")
	extraction, err := extractor.Extract(context.Background(), source.ID, "tail-local")
	if err != nil {
		t.Fatal(err)
	}
	got := waitTailTerminal(t, extractor, extraction.ID)
	if got.State != AttemptSucceeded || got.OutputAssetID == "" {
		t.Fatalf("extraction = %#v", got)
	}
	output, ok := assets.Get(got.OutputAssetID)
	if !ok || output.State != asset.StateArchive || output.Source != "video-tail:"+got.ID {
		t.Fatalf("output = %#v, found = %v", output, ok)
	}
	assertTailReference(t, assets, source.ID, asset.Reference{Module: "video_attempt", RecordID: got.ID}, true)
	assertTailReference(t, assets, output.ID, asset.Reference{Module: "video_result", RecordID: got.ID}, true)
}

func TestTailExtractorAcceptsArchivedRetainedVideo(t *testing.T) {
	extractor, assets := tailFixture(t, `printf '\x89PNG\r\n\x1a\nfixture' > "$OUTPUT_IMAGE"`)
	source := importFixtureAsset(t, assets, "video/webm")
	if _, err := assets.SetState(source.ID, asset.StateArchive); err != nil {
		t.Fatal(err)
	}
	extraction, err := extractor.Extract(context.Background(), source.ID, "tail-local")
	if err != nil {
		t.Fatal(err)
	}
	if got := waitTailTerminal(t, extractor, extraction.ID); got.State != AttemptSucceeded {
		t.Fatalf("extraction = %#v", got)
	}
}

func TestTailExtractorImportsJPEGAlias(t *testing.T) {
	extractor, assets := tailFixture(t, `printf '\xff\xd8\xff\xe0fixture' > "$OUTPUT_IMAGE"`)
	videos := extractor.config.Snapshot().Videos
	videos.TailFramePresets[0].OutputExtension = ".jpeg"
	if _, err := extractor.config.UpdateVideos(videos); err != nil {
		t.Fatal(err)
	}
	source := importFixtureAsset(t, assets, "video/webm")
	extraction, err := extractor.Extract(context.Background(), source.ID, "tail-local")
	if err != nil {
		t.Fatal(err)
	}
	got := waitTailTerminal(t, extractor, extraction.ID)
	output, ok := assets.Get(got.OutputAssetID)
	if got.State != AttemptSucceeded || !ok || output.MediaType != "image/jpeg" {
		t.Fatalf("extraction = %#v, output = %#v, found = %v", got, output, ok)
	}
}

func TestTailExtractorRejectsNonStaticVideoInputsAndDisabledPreset(t *testing.T) {
	extractor, assets := tailFixture(t, `printf '\x89PNG\r\n\x1a\nfixture' > "$OUTPUT_IMAGE"`)
	for _, mediaType := range []string{"image/png", "image/webp", "application/octet-stream"} {
		source := importFixtureAsset(t, assets, mediaType)
		if _, err := extractor.Extract(context.Background(), source.ID, "tail-local"); err == nil {
			t.Fatalf("Extract accepted %q", mediaType)
		}
	}
	configuration := extractor.config.Snapshot().Videos
	configuration.TailFramePresets[0].Enabled = false
	if _, err := extractor.config.UpdateVideos(configuration); err != nil {
		t.Fatal(err)
	}
	source := importFixtureAsset(t, assets, "video/webm")
	if _, err := extractor.Extract(context.Background(), source.ID, "tail-local"); err == nil {
		t.Fatal("Extract accepted a disabled tail preset")
	}
}

func TestTailExtractorFailsForCommandAndInvalidOutputWithoutChangingSource(t *testing.T) {
	cases := []struct {
		name    string
		command string
	}{
		{name: "command", command: "exit 7"},
		{name: "missing", command: "true"},
		{name: "empty", command: `: > "$OUTPUT_IMAGE"`},
		{name: "over limit", command: `printf '\x89PNG\r\n\x1a\n1234567890' > "$OUTPUT_IMAGE"`},
		{name: "wrong magic", command: `printf not-an-image > "$OUTPUT_IMAGE"`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			extractor, assets := tailFixture(t, test.command)
			if test.name == "over limit" {
				videos := extractor.config.Snapshot().Videos
				videos.TailFramePresets[0].MaxImageBytes = 8
				if _, err := extractor.config.UpdateVideos(videos); err != nil {
					t.Fatal(err)
				}
			}
			source := importFixtureAsset(t, assets, "video/webm")
			extraction, err := extractor.Extract(context.Background(), source.ID, "tail-local")
			if err != nil {
				t.Fatal(err)
			}
			got := waitTailTerminal(t, extractor, extraction.ID)
			if got.State != AttemptFailed || got.Error.Code == "" {
				t.Fatalf("extraction = %#v", got)
			}
			current, _ := assets.Get(source.ID)
			if current.State != asset.StateActive {
				t.Fatalf("source changed to %q", current.State)
			}
		})
	}
}

func TestTailExtractorCancelsProcessGroupAndSavesLogOnlyOnRequest(t *testing.T) {
	extractor, assets := tailFixture(t, `printf 'tail-log'; trap 'exit 0' TERM; while :; do sleep 1; done`)
	source := importFixtureAsset(t, assets, "video/webm")
	extraction, err := extractor.Extract(context.Background(), source.ID, "tail-local")
	if err != nil {
		t.Fatal(err)
	}
	waitTailState(t, extractor, extraction.ID, AttemptRunning)
	waitTailProcess(t, extractor, extraction.ID)
	if current, ok := extractor.repository.Get(extraction.ID); !ok || current.PID <= 0 {
		t.Fatalf("running extraction did not persist its PID: %#v, found = %v", current, ok)
	}
	waitTailLog(t, extractor, extraction.ID, "tail-log")
	if err := extractor.CancelExtraction(context.Background(), extraction.ID); err != nil {
		t.Fatal(err)
	}
	if got := waitTailTerminal(t, extractor, extraction.ID); got.State != AttemptCancelled {
		t.Fatalf("extraction = %#v", got)
	}
	path, err := extractor.SaveExtractionLog(extraction.ID)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(contents), "tail-log") {
		t.Fatalf("saved log = %q, error = %v", contents, err)
	}
}

func TestTailExtractorSerializesCancellationWithPostRunImport(t *testing.T) {
	extractor, assets := tailFixture(t, `printf '\x89PNG\r\n\x1a\nfixture' > "$OUTPUT_IMAGE"`)
	source := importFixtureAsset(t, assets, "video/webm")
	extraction, err := extractor.Extract(context.Background(), source.ID, "tail-local")
	if err != nil {
		t.Fatal(err)
	}
	extractor.mu.Lock()
	run := extractor.runs[extraction.ID]
	extractor.mu.Unlock()
	if run == nil {
		t.Fatal("tail extraction run is missing")
	}
	run.lifecycle.Lock()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if status, statusErr := extractor.executor.Status(extraction.ID); statusErr == nil && !status.Running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancelled := make(chan error, 1)
	go func() { cancelled <- extractor.CancelExtraction(context.Background(), extraction.ID) }()
	select {
	case err := <-cancelled:
		t.Fatalf("CancelExtraction returned across the post-run lifecycle boundary: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	run.lifecycle.Unlock()
	if err := <-cancelled; err != nil {
		t.Fatal(err)
	}
	if got := waitTailTerminal(t, extractor, extraction.ID); got.State != AttemptSucceeded && got.State != AttemptCancelled {
		t.Fatalf("serialized terminal extraction = %#v", got)
	}
}

func TestTailExtractorSubscriptionHasInitialSnapshotAndNoTerminalGap(t *testing.T) {
	extractor, assets := tailFixture(t, `printf '\x89PNG\r\n\x1a\nfixture' > "$OUTPUT_IMAGE"`)
	source := importFixtureAsset(t, assets, "video/webm")
	extraction, err := extractor.Extract(context.Background(), source.ID, "tail-local")
	if err != nil {
		t.Fatal(err)
	}
	initial, updates, cancel, err := extractor.SubscribeExtraction(extraction.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if initial.ID != extraction.ID {
		t.Fatalf("initial = %#v", initial)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case update := <-updates:
			if terminalAttemptState(update.State) {
				return
			}
		case <-deadline:
			t.Fatal("subscription missed terminal extraction update")
		}
	}
}

func TestTailExtractorShutdownLeavesSharedExecutorAvailable(t *testing.T) {
	extractor, _ := tailFixture(t, `true`)
	if err := extractor.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	request := fixtureCLIRunRequest(t, "", `printf '\x1a\x45\xdf\xa3' > "$OUTPUT_PATH"`)
	if result, err := extractor.executor.Run(context.Background(), request); err != nil || result.State != CLIStateSucceeded {
		t.Fatalf("shared executor result = %#v, error = %v", result, err)
	}
}

func TestTailExtractorShutdownClosesSubscriptions(t *testing.T) {
	extractor, assets := tailFixture(t, `while :; do sleep 1; done`)
	source := importFixtureAsset(t, assets, "video/webm")
	extraction, err := extractor.Extract(context.Background(), source.ID, "tail-local")
	if err != nil {
		t.Fatal(err)
	}
	_, updates, cancel, err := extractor.SubscribeExtraction(extraction.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if err := extractor.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case _, open := <-updates:
		if open {
			t.Fatal("tail subscription remains open after shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("tail shutdown did not close subscription")
	}
}

func TestTailRepositoryOpenInterruptsActiveRecordsAtomically(t *testing.T) {
	root := t.TempDir()
	repository, err := OpenTailRepository(filepath.Join(root, "tail-extractions.json"))
	if err != nil {
		t.Fatal(err)
	}
	created := TailExtraction{ID: strings.Repeat("a", 32), SourceAssetID: strings.Repeat("b", 32), PresetID: "tail-local", State: AttemptQueued, CreatedAt: time.Now().UTC()}
	if err := repository.Create(created); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenTailRepository(filepath.Join(root, "tail-extractions.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get(created.ID)
	if !ok || got.State != AttemptInterrupted || got.Error.Code != "workbench_restarted" || got.CompletedAt == nil {
		t.Fatalf("recovered = %#v, found = %v", got, ok)
	}
}

func TestTailRepositoryRejectsInvalidStatesAndMutation(t *testing.T) {
	repository, err := OpenTailRepository(filepath.Join(t.TempDir(), "tail-extractions.json"))
	if err != nil {
		t.Fatal(err)
	}
	created := TailExtraction{ID: strings.Repeat("c", 32), SourceAssetID: strings.Repeat("d", 32), PresetID: "tail-local", State: AttemptQueued, CreatedAt: time.Now().UTC()}
	if err := repository.Create(created); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*TailExtraction){
		func(item *TailExtraction) { item.State = AttemptSubmitting },
		func(item *TailExtraction) { item.SourceAssetID = strings.Repeat("e", 32) },
		func(item *TailExtraction) { item.State = AttemptSucceeded },
	} {
		candidate := created
		change(&candidate)
		if err := repository.Update(candidate); err == nil {
			t.Fatalf("Update accepted %#v", candidate)
		}
	}
	created.State = AttemptRunning
	if err := repository.Update(created); err != nil {
		t.Fatal(err)
	}
	created.State = AttemptSucceeded
	created.OutputAssetID = strings.Repeat("f", 32)
	if err := repository.Update(created); err != nil {
		t.Fatal(err)
	}
}

func TestTailExtractorRetriesTerminalPersistenceFailure(t *testing.T) {
	extractor, assets := tailFixture(t, `sleep 0.05; printf '\x89PNG\r\n\x1a\nfixture' > "$OUTPUT_IMAGE"`)
	source := importFixtureAsset(t, assets, "video/webm")
	extraction, err := extractor.Extract(context.Background(), source.ID, "tail-local")
	if err != nil {
		t.Fatal(err)
	}
	waitTailProcess(t, extractor, extraction.ID)
	extractor.mu.Lock()
	run := extractor.runs[extraction.ID]
	extractor.mu.Unlock()
	if run == nil {
		t.Fatal("tail extraction run is missing")
	}
	run.lifecycle.Lock()
	badPath := filepath.Join(t.TempDir(), "repository-directory")
	if err := os.Mkdir(badPath, 0o700); err != nil {
		t.Fatal(err)
	}
	extractor.repository.mu.Lock()
	originalPath := extractor.repository.path
	extractor.repository.path = badPath
	extractor.repository.mu.Unlock()
	defer func() {
		extractor.repository.mu.Lock()
		extractor.repository.path = originalPath
		extractor.repository.mu.Unlock()
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if status, statusErr := extractor.executor.Status(extraction.ID); statusErr == nil && !status.Running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	run.lifecycle.Unlock()
	deadline = time.Now().Add(time.Second)
	sawFailure := false
	for time.Now().Before(deadline) {
		extractor.mu.Lock()
		failure := extractor.failures[extraction.ID]
		extractor.mu.Unlock()
		if failure != nil {
			sawFailure = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawFailure {
		t.Fatal("terminal persistence failure was not observed")
	}
	if err := extractor.CancelExtraction(context.Background(), extraction.ID); err == nil {
		t.Fatal("CancelExtraction hid a pending terminal persistence failure")
	}
	extractor.repository.mu.Lock()
	extractor.repository.path = originalPath
	extractor.repository.mu.Unlock()
	if got := waitTailTerminal(t, extractor, extraction.ID); got.State != AttemptSucceeded || got.OutputAssetID == "" || got.CompletedAt == nil {
		t.Fatalf("retried extraction = %#v", got)
	}
}

type tailFixtureResult struct {
	config  *config.Repository
	assets  *asset.Repository
	service *TailExtractor
}

func tailFixture(t *testing.T, command string) (*TailExtractor, *asset.Repository) {
	t.Helper()
	root := t.TempDir()
	assets, err := asset.OpenRepository(filepath.Join(root, "assets.json"), filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	videos := videoconfig.Config{TailFramePresets: []videoconfig.TailFramePreset{{
		ID: "tail-local", Name: "Tail", Enabled: true, CommandTemplate: command,
		TimeoutSeconds: 2, StopGraceSeconds: 0, MaxImageBytes: 1024, OutputExtension: ".png",
	}}}
	if _, err := configuration.UpdateVideos(videos); err != nil {
		t.Fatal(err)
	}
	repository, err := OpenTailRepository(filepath.Join(root, "videos", "tail-extractions.json"))
	if err != nil {
		t.Fatal(err)
	}
	extractor := NewTailExtractor(configuration, repository, assets, NewCLIExecutor(), filepath.Join(root, "video-workspace"), filepath.Join(root, "logs"))
	t.Cleanup(func() { _ = extractor.Shutdown(context.Background()) })
	return extractor, assets
}

func waitTailTerminal(t *testing.T, extractor *TailExtractor, id string) TailExtraction {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := extractor.repository.Get(id); ok && terminalAttemptState(got.State) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("tail extraction did not reach a terminal state")
	return TailExtraction{}
}

func waitTailState(t *testing.T, extractor *TailExtractor, id string, want AttemptState) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got, ok := extractor.repository.Get(id); ok && got.State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("tail extraction %q did not reach %s", id, want)
}

func waitTailProcess(t *testing.T, extractor *TailExtractor, id string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, ok := extractor.repository.Get(id)
		if status, err := extractor.executor.Status(id); err == nil && status.PID > 0 && ok && current.PID == status.PID {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("tail extraction %q did not start a process", id)
}

func waitTailLog(t *testing.T, extractor *TailExtractor, id, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if snapshot, err := extractor.executor.SnapshotLog(id); err == nil && strings.Contains(string(snapshot.Data), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("tail extraction %q did not write %q to its log", id, want)
}

func assertTailReference(t *testing.T, assets *asset.Repository, id string, want asset.Reference, present bool) {
	t.Helper()
	item, ok := assets.Get(id)
	if !ok {
		t.Fatalf("asset %q missing", id)
	}
	for _, reference := range item.References {
		if reference == want {
			if !present {
				t.Fatalf("unexpected reference %#v", want)
			}
			return
		}
	}
	if present {
		t.Fatalf("missing reference %#v from %#v", want, item.References)
	}
}

var _ = bytes.NewReader
var _ = errors.Is
