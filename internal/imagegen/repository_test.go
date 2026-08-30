package imagegen

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ekk1/ai-desktop/internal/sdcpp"
)

func TestRepositoryPersistsOrderedBatchItems(t *testing.T) {
	root := t.TempDir()
	repository, err := OpenRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := repository.CreateBatch(CreateBatchInput{
		Title: "Draw", Folder: "ideas", ProviderID: "sdcpp-local", Concurrency: 1,
		BaseParams: json.RawMessage(`{"width":768}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := repository.CreateItems(batch.ID, []CreateItemInput{
		{Prompt: "one", ParamsOverride: json.RawMessage(`{}`)},
		{Prompt: "two", ParamsOverride: json.RawMessage(`{"seed":2}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MoveItem(batch.ID, items[1].ID, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateItem(batch.ID, items[0].ID, UpdateItemInput{
		Prompt: "one edited", NegativePrompt: "blur", ParamsOverride: json.RawMessage(`{"seed":3}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateBatch(batch.ID, UpdateBatchInput{
		Title: "Draw edited", Folder: "work", ProviderID: "gpu", Concurrency: 2,
		BaseParams: json.RawMessage(`{"width":1024}`),
	}); err != nil {
		t.Fatal(err)
	}

	got, ok := repository.Get(batch.ID)
	if !ok {
		t.Fatal("batch missing")
	}
	if got.Title != "Draw edited" || got.Folder != "work" || got.ProviderID != "gpu" || got.Concurrency != 2 {
		t.Fatalf("batch metadata = %#v", got)
	}
	if got.Items[0].Prompt != "two" || got.Items[0].Order != 0 || got.Items[1].Prompt != "one edited" || got.Items[1].Order != 1 {
		t.Fatalf("items = %#v", got.Items)
	}
	if got.Items[1].NegativePrompt != "blur" || string(got.Items[1].ParamsOverride) != `{"seed":3}` {
		t.Fatalf("updated item = %#v", got.Items[1])
	}

	listed := repository.List(Filter{Query: "edited", Folder: "work", ProviderID: "gpu"})
	if len(listed) != 1 || listed[0].ID != got.ID {
		t.Fatalf("list = %#v", listed)
	}
	reopened, err := OpenRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := reopened.Get(batch.ID)
	if !ok || !reflect.DeepEqual(got, persisted) {
		t.Fatalf("reopened = %#v, ok = %v", persisted, ok)
	}
	if err := reopened.DeleteItem(batch.ID, items[1].ID); err != nil {
		t.Fatal(err)
	}
	afterDelete, _ := reopened.Get(batch.ID)
	if len(afterDelete.Items) != 1 || afterDelete.Items[0].Order != 0 || afterDelete.Items[0].ID != items[0].ID {
		t.Fatalf("after item delete = %#v", afterDelete.Items)
	}
	if err := reopened.DeleteBatch(batch.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Get(batch.ID); ok {
		t.Fatal("deleted batch remains")
	}
}

func TestRepositoryRejectsInvalidBatchAndItemInputs(t *testing.T) {
	validBatch := validBatchInput()
	tests := []struct {
		name  string
		input CreateBatchInput
	}{
		{name: "empty title", input: replaceBatch(validBatch, func(input *CreateBatchInput) { input.Title = " " })},
		{name: "missing provider", input: replaceBatch(validBatch, func(input *CreateBatchInput) { input.ProviderID = "" })},
		{name: "zero concurrency", input: replaceBatch(validBatch, func(input *CreateBatchInput) { input.Concurrency = 0 })},
		{name: "too much concurrency", input: replaceBatch(validBatch, func(input *CreateBatchInput) { input.Concurrency = 17 })},
		{name: "array params", input: replaceBatch(validBatch, func(input *CreateBatchInput) { input.BaseParams = json.RawMessage(`[]`) })},
		{name: "invalid params", input: replaceBatch(validBatch, func(input *CreateBatchInput) { input.BaseParams = json.RawMessage(`{`) })},
	}
	for _, key := range []string{"prompt", "negative_prompt", "init_image", "ref_images", "mask_image", "control_image", "ip_adapter_image"} {
		tests = append(tests, struct {
			name  string
			input CreateBatchInput
		}{name: "reserved " + key, input: replaceBatch(validBatch, func(input *CreateBatchInput) {
			input.BaseParams = json.RawMessage(`{"` + key + `":"managed"}`)
		})})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, err := OpenRepository(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repository.CreateBatch(test.input); err == nil {
				t.Fatal("CreateBatch succeeded")
			}
			if got := repository.List(Filter{}); len(got) != 0 {
				t.Fatalf("invalid batch persisted: %#v", got)
			}
		})
	}

	repository, _ := OpenRepository(t.TempDir())
	batch, _ := repository.CreateBatch(validBatch)
	for _, params := range []json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`{"prompt":"managed"}`)} {
		if _, err := repository.CreateItems(batch.ID, []CreateItemInput{{Prompt: "p", ParamsOverride: params}}); err == nil {
			t.Fatalf("CreateItems accepted %s", params)
		}
	}
	if got, _ := repository.Get(batch.ID); len(got.Items) != 0 {
		t.Fatalf("invalid items persisted: %#v", got.Items)
	}
}

func TestRepositoryRejectsMissingItemAndOutOfBoundsMove(t *testing.T) {
	repository, _ := OpenRepository(t.TempDir())
	batch, _ := repository.CreateBatch(validBatchInput())
	items, _ := repository.CreateItems(batch.ID, []CreateItemInput{{Prompt: "one"}, {Prompt: "two"}})
	before, _ := repository.Get(batch.ID)

	if _, err := repository.UpdateItem(batch.ID, "00000000000000000000000000000000", UpdateItemInput{}); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("UpdateItem error = %v", err)
	}
	if _, err := repository.MoveItem(batch.ID, items[0].ID, -1); !errors.Is(err, ErrMoveBoundary) {
		t.Fatalf("MoveItem error = %v", err)
	}
	if _, err := repository.MoveItem(batch.ID, items[1].ID, 1); !errors.Is(err, ErrMoveBoundary) {
		t.Fatalf("MoveItem error = %v", err)
	}
	if _, err := repository.MoveItem(batch.ID, "00000000000000000000000000000000", 1); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("missing MoveItem error = %v", err)
	}
	after, _ := repository.Get(batch.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("batch mutated: before=%#v after=%#v", before, after)
	}
}

func TestRepositoryKeepsAttemptHistoryAndInterruptsActiveOnReopen(t *testing.T) {
	root := t.TempDir()
	repository, _ := OpenRepository(root)
	batch, _ := repository.CreateBatch(validBatchInput())
	items, _ := repository.CreateItems(batch.ID, []CreateItemInput{{Prompt: "cat"}})
	first, err := repository.CreateAttempt(batch.ID, items[0].ID, CreateAttemptInput{State: AttemptQueued, Snapshot: validSnapshot()})
	if err != nil {
		t.Fatal(err)
	}
	if first.CreatedAt.IsZero() || first.Snapshot.CreatedAt.IsZero() || len(first.ResultAssetIDs) != 0 {
		t.Fatalf("created attempt = %#v", first)
	}
	if _, err := repository.CreateAttempt(batch.ID, items[0].ID, CreateAttemptInput{State: AttemptQueued, Snapshot: validSnapshot()}); !errors.Is(err, ErrActiveAttempt) {
		t.Fatalf("second active attempt error = %v", err)
	}
	if _, err := repository.UpdateAttempt(batch.ID, items[0].ID, first.ID, UpdateAttemptInput{State: AttemptSubmitting, RemoteJobID: "job-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateAttempt(batch.ID, items[0].ID, first.ID, UpdateAttemptInput{State: AttemptPolling, RemoteJobID: "job-a", RemoteStatus: "generating", QueuePosition: 2}); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reopened.Get(batch.ID)
	if len(got.Items[0].Attempts) != 1 || got.Items[0].Attempts[0].State != AttemptInterrupted || got.Items[0].Attempts[0].CompletedAt.IsZero() {
		t.Fatalf("recovered batch = %#v", got)
	}
	second, err := reopened.CreateAttempt(batch.ID, items[0].ID, CreateAttemptInput{State: AttemptQueued, Snapshot: validSnapshot()})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("attempt ID reused")
	}
	after, _ := reopened.Get(batch.ID)
	if len(after.Items[0].Attempts) != 2 || after.Items[0].Attempts[0].ID != first.ID || after.Items[0].Attempts[1].ID != second.ID {
		t.Fatalf("attempt history = %#v", after.Items[0].Attempts)
	}
}

func TestRepositoryEnforcesAttemptTransitionsAndCopiesValues(t *testing.T) {
	repository, _ := OpenRepository(t.TempDir())
	batch, _ := repository.CreateBatch(validBatchInput())
	items, _ := repository.CreateItems(batch.ID, []CreateItemInput{{Prompt: "cat"}})
	snapshot := validSnapshot()
	attempt, err := repository.CreateAttempt(batch.ID, items[0].ID, CreateAttemptInput{State: AttemptQueued, Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Provider.Headers["X-Late"] = "mutation"
	snapshot.Params[0] = '['

	if _, err := repository.UpdateAttempt(batch.ID, items[0].ID, attempt.ID, UpdateAttemptInput{State: AttemptSucceeded}); !errors.Is(err, ErrInvalidAttemptTransition) {
		t.Fatalf("skipped transition error = %v", err)
	}
	if _, err := repository.UpdateAttempt(batch.ID, items[0].ID, attempt.ID, UpdateAttemptInput{State: AttemptSubmitting}); err != nil {
		t.Fatal(err)
	}
	resultIDs := []string{"11111111111111111111111111111111", "11111111111111111111111111111111", "22222222222222222222222222222222"}
	finished, err := repository.UpdateAttempt(batch.ID, items[0].ID, attempt.ID, UpdateAttemptInput{
		State: AttemptFailed, RemoteJobID: "job-a", RemoteStatus: "failed", ResultAssetIDs: resultIDs,
		Error: AttemptError{Code: "remote", Message: "failed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultIDs[0] = "33333333333333333333333333333333"
	if finished.StartedAt.IsZero() || finished.CompletedAt.IsZero() || len(finished.ResultAssetIDs) != 2 {
		t.Fatalf("finished attempt = %#v", finished)
	}
	if finished.Snapshot.Provider.Headers["X-Late"] != "" || string(finished.Snapshot.Params) != `{"steps":20}` {
		t.Fatalf("snapshot alias retained: %#v", finished.Snapshot)
	}
	if _, err := repository.UpdateAttempt(batch.ID, items[0].ID, attempt.ID, UpdateAttemptInput{State: AttemptCancelled}); !errors.Is(err, ErrInvalidAttemptTransition) {
		t.Fatalf("terminal transition error = %v", err)
	}
}

func TestRepositoryRejectsInvalidAttemptInputsWithoutMutation(t *testing.T) {
	repository, _ := OpenRepository(t.TempDir())
	batch, _ := repository.CreateBatch(validBatchInput())
	items, _ := repository.CreateItems(batch.ID, []CreateItemInput{{Prompt: "cat"}})
	if _, err := repository.CreateAttempt(batch.ID, items[0].ID, CreateAttemptInput{State: AttemptFailed, Snapshot: validSnapshot()}); err == nil {
		t.Fatal("created terminal attempt")
	}
	attempt, _ := repository.CreateAttempt(batch.ID, items[0].ID, CreateAttemptInput{State: AttemptQueued, Snapshot: validSnapshot()})
	before, _ := repository.Get(batch.ID)
	if _, err := repository.UpdateAttempt(batch.ID, items[0].ID, attempt.ID, UpdateAttemptInput{
		State: AttemptSubmitting, Error: AttemptError{Message: strings.Repeat("x", 4097)},
	}); err == nil {
		t.Fatal("accepted oversized error")
	}
	if _, err := repository.UpdateAttempt(batch.ID, items[0].ID, "00000000000000000000000000000000", UpdateAttemptInput{State: AttemptSubmitting}); !errors.Is(err, ErrAttemptNotFound) {
		t.Fatalf("missing attempt error = %v", err)
	}
	after, _ := repository.Get(batch.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("invalid updates mutated batch: before=%#v after=%#v", before, after)
	}
}

func validBatchInput() CreateBatchInput {
	return CreateBatchInput{
		Title: "Draw", Folder: "ideas", ProviderID: "sdcpp-local", Concurrency: 1,
		BaseParams: json.RawMessage(`{"width":768}`),
	}
}

func replaceBatch(input CreateBatchInput, replace func(*CreateBatchInput)) CreateBatchInput {
	input.BaseParams = append(json.RawMessage(nil), input.BaseParams...)
	replace(&input)
	return input
}

func validSnapshot() Snapshot {
	provider := sdcpp.DefaultImageConfig().Providers[0]
	provider.Headers["X-Test"] = "value"
	return Snapshot{
		Provider: provider, Params: json.RawMessage(`{"steps":20}`), Prompt: "cat",
		InputAssets: []AssetSnapshot{{
			ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SHA256: strings.Repeat("b", 64), MediaType: "image/png",
			DisplayName: "input.png", Size: 42, Width: 2, Height: 3,
		}},
	}
}
