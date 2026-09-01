package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/imagegen"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
)

func TestImageAttemptAPIExecutesGetsAndCancelsIdempotently(t *testing.T) {
	fixture := newImageExecutionWebFixture(t)
	batch := fixture.createBatch(t, "one")
	invalid := fixture.request(http.MethodPost, "/api/v1/images/batches/"+batch.ID+"/execute", []byte(`null`))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("null execute status = %d, body = %s", invalid.Code, invalid.Body.String())
	}
	invalid = fixture.request(http.MethodPost, "/api/v1/images/batches/"+batch.ID+"/execute", nil)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("empty execute status = %d, body = %s", invalid.Code, invalid.Body.String())
	}
	execute := fixture.request(http.MethodPost, "/api/v1/images/batches/"+batch.ID+"/execute", []byte(`{}`))
	if execute.Code != http.StatusAccepted {
		t.Fatalf("execute status = %d, body = %s", execute.Code, execute.Body.String())
	}
	var accepted struct {
		Attempts []imagegen.Attempt `json:"attempts"`
	}
	if err := json.NewDecoder(execute.Body).Decode(&accepted); err != nil || len(accepted.Attempts) != 1 {
		t.Fatalf("attempts = %#v, error = %v", accepted, err)
	}
	fixture.waitRemoteID(t, accepted.Attempts[0].ID)
	retry := fixture.request(http.MethodPost, "/api/v1/images/batches/"+batch.ID+"/items/"+batch.Items[0].ID+"/execute", []byte(`{}`))
	if retry.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, body = %s", retry.Code, retry.Body.String())
	}
	get := fixture.request(http.MethodGet, "/api/v1/images/attempts/"+accepted.Attempts[0].ID, nil)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), accepted.Attempts[0].ID) {
		t.Fatalf("get status = %d, body = %s", get.Code, get.Body.String())
	}
	cancel := fixture.request(http.MethodPost, "/api/v1/images/attempts/"+accepted.Attempts[0].ID+"/cancel", []byte(`{}`))
	if cancel.Code != http.StatusAccepted || fixture.remote.cancelCount() != 1 {
		t.Fatalf("cancel status = %d, body = %s, remote calls = %d", cancel.Code, cancel.Body.String(), fixture.remote.cancelCount())
	}
	cancel = fixture.request(http.MethodPost, "/api/v1/images/attempts/"+accepted.Attempts[0].ID+"/cancel", []byte(`{}`))
	if cancel.Code != http.StatusOK || fixture.remote.cancelCount() != 1 {
		t.Fatalf("idempotent status = %d, body = %s, remote calls = %d", cancel.Code, cancel.Body.String(), fixture.remote.cancelCount())
	}
	single := fixture.request(http.MethodPost, "/api/v1/images/batches/"+batch.ID+"/items/"+batch.Items[0].ID+"/execute", []byte(`{}`))
	var singleAccepted struct {
		Attempts []imagegen.Attempt `json:"attempts"`
	}
	if err := json.NewDecoder(single.Body).Decode(&singleAccepted); single.Code != http.StatusAccepted || err != nil || len(singleAccepted.Attempts) != 1 {
		t.Fatalf("single execute status = %d, body = %s, error = %v", single.Code, single.Body.String(), err)
	}
}

func TestImageCancelAPIMapsUpstreamErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "http", err: &sdcpp.HTTPError{StatusCode: 500, Body: "failed"}, status: http.StatusBadGateway},
		{name: "timeout", err: context.DeadlineExceeded, status: http.StatusGatewayTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newImageExecutionWebFixture(t)
			fixture.remote.cancelErr = test.err
			batch := fixture.createBatch(t, "one")
			attempt, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			fixture.waitRemoteID(t, attempt.ID)
			response := fixture.request(http.MethodPost, "/api/v1/images/attempts/"+attempt.ID+"/cancel", []byte(`{}`))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestImageItemExecuteReturnsPersistedPreflightFailure(t *testing.T) {
	fixture := newImageExecutionWebFixture(t)
	input := importWebImage(t, fixture.assets, "missing.png")
	batch, err := fixture.service.CreateBatch(imagegen.CreateBatchInput{Title: "Draw", ProviderID: "sdcpp-local", Concurrency: 1, BaseParams: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	items, err := fixture.service.CreateItems(batch.ID, []imagegen.CreateItemInput{{Prompt: "one", InputAssets: imagegen.InputAssets{InitImageID: input.ID}}})
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
	response := fixture.request(http.MethodPost, "/api/v1/images/batches/"+batch.ID+"/items/"+items[0].ID+"/execute", []byte(`{}`))
	var accepted struct {
		Attempts []imagegen.Attempt `json:"attempts"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); response.Code != http.StatusAccepted || err != nil || len(accepted.Attempts) != 1 || accepted.Attempts[0].State != imagegen.AttemptFailed {
		t.Fatalf("status = %d, body = %s, attempts = %#v, error = %v", response.Code, response.Body.String(), accepted, err)
	}
}

func TestImageAttemptSSEWritesSnapshotHeartbeatAndDisconnects(t *testing.T) {
	fixture := newImageExecutionWebFixture(t)
	batch := fixture.createBatch(t, "one")
	attempt, err := fixture.manager.StartItem(batch.ID, batch.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitRemoteID(t, attempt.ID)
	handler := imageAttemptHandler{manager: fixture.manager, maxBody: fixture.config.Snapshot().MaxUploadBytes, heartbeat: 5 * time.Millisecond}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		handler.events(response, request, batch.ID)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	event, _ := reader.ReadString('\n')
	data, _ := reader.ReadString('\n')
	_, _ = reader.ReadString('\n')
	if event != "event: snapshot\n" || !strings.Contains(data, attempt.ID) {
		t.Fatalf("snapshot = %q%q", event, data)
	}
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("read heartbeat: %v", readErr)
		}
		if line == ": heartbeat\n" {
			if blank, blankErr := reader.ReadString('\n'); blankErr != nil || blank != "\n" {
				t.Fatalf("heartbeat terminator = %q, error = %v", blank, blankErr)
			}
			break
		}
		if !strings.HasPrefix(line, "event: ") {
			t.Fatalf("unexpected SSE line before heartbeat: %q", line)
		}
		if _, readErr = reader.ReadString('\n'); readErr != nil {
			t.Fatalf("read intervening SSE data: %v", readErr)
		}
		if blank, blankErr := reader.ReadString('\n'); blankErr != nil || blank != "\n" {
			t.Fatalf("intervening SSE terminator = %q, error = %v", blank, blankErr)
		}
	}
	cancel()
	_ = response.Body.Close()
}

type blockingImageRemote struct {
	mu          sync.Mutex
	cancelCalls int
	cancelErr   error
}

func (remote *blockingImageRemote) Submit(_ context.Context, _ sdcpp.ImageProvider, _ []byte) (sdcpp.Submission, error) {
	return sdcpp.Submission{ID: "job-web", Kind: "img_gen", Status: "queued"}, nil
}

func (remote *blockingImageRemote) Job(ctx context.Context, _ sdcpp.ImageProvider, _ string) (sdcpp.Job, error) {
	<-ctx.Done()
	return sdcpp.Job{}, ctx.Err()
}

func (remote *blockingImageRemote) Cancel(context.Context, sdcpp.ImageProvider, string) error {
	remote.mu.Lock()
	remote.cancelCalls++
	err := remote.cancelErr
	remote.mu.Unlock()
	return err
}

func (remote *blockingImageRemote) cancelCount() int {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	return remote.cancelCalls
}

type imageExecutionWebFixture struct {
	handler http.Handler
	manager *imagegen.Manager
	service *imagegen.Service
	config  *config.Repository
	remote  *blockingImageRemote
	assets  *asset.Repository
}

func newImageExecutionWebFixture(t *testing.T) imageExecutionWebFixture {
	t.Helper()
	root := t.TempDir()
	configuration, _ := config.OpenRepository(filepath.Join(root, "config.json"))
	assets, _ := asset.OpenRepository(filepath.Join(root, "assets", "index.json"), filepath.Join(root, "assets", "files"))
	repository, _ := imagegen.OpenRepository(filepath.Join(root, "images", "batches"))
	service := imagegen.NewService(repository, assets)
	remote := &blockingImageRemote{}
	manager := imagegen.NewManager(configuration, service, imagegen.NewAssembler(assets), assets, remote)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	handler := NewHandler(Options{
		Version: "test", Config: configuration.Snapshot(), ConfigRepository: configuration,
		AssetRepository: assets, ImageService: service, ImageManager: manager,
	})
	return imageExecutionWebFixture{handler: handler, manager: manager, service: service, config: configuration, remote: remote, assets: assets}
}

func (fixture imageExecutionWebFixture) createBatch(t *testing.T, prompts ...string) imagegen.Batch {
	t.Helper()
	batch, err := fixture.service.CreateBatch(imagegen.CreateBatchInput{Title: "Draw", ProviderID: "sdcpp-local", Concurrency: 1, BaseParams: json.RawMessage(`{"output_format":"png"}`)})
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]imagegen.CreateItemInput, len(prompts))
	for index, prompt := range prompts {
		inputs[index] = imagegen.CreateItemInput{Prompt: prompt}
	}
	if _, err := fixture.service.CreateItems(batch.ID, inputs); err != nil {
		t.Fatal(err)
	}
	batch, _ = fixture.service.Get(batch.ID)
	return batch
}

func (fixture imageExecutionWebFixture) request(method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}

func (fixture imageExecutionWebFixture) waitRemoteID(t *testing.T, attemptID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if attempt, ok := fixture.manager.GetAttempt(attemptID); ok && attempt.RemoteJobID != "" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("remote job ID was not persisted")
}

var _ imagegen.RemoteClient = (*blockingImageRemote)(nil)
