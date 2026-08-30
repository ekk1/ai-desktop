package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
)

func TestImageConfigAPIUpdatesCompleteConfiguration(t *testing.T) {
	fixture := newImageConfigWebFixture(t, capabilityClientStub{})
	response := fixture.request(http.MethodGet, "/api/v1/images/config", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", response.Code, response.Body.String())
	}
	configuration := fixture.repository.Snapshot().Images
	configuration.Providers[0].ID = "gpu"
	configuration.Providers[0].Name = "GPU"
	encoded, _ := json.Marshal(configuration)
	response = fixture.request(http.MethodPut, "/api/v1/images/config", encoded)
	if response.Code != http.StatusOK || fixture.repository.Snapshot().Images.Providers[0].ID != "gpu" {
		t.Fatalf("PUT status = %d, body = %s", response.Code, response.Body.String())
	}
	response = fixture.request(http.MethodPut, "/api/v1/images/config", []byte(`{"providers":[],"unknown":true}`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestImageCapabilitiesAPIUsesEnabledSavedProvider(t *testing.T) {
	calls := []sdcpp.ImageProvider{}
	client := capabilityClientStub{result: sdcpp.Capabilities{CurrentMode: "img_gen", OutputFormats: []string{"png"}}, calls: &calls}
	fixture := newImageConfigWebFixture(t, client)
	response := fixture.request(http.MethodGet, "/api/v1/images/providers/sdcpp-local/capabilities", nil)
	if response.Code != http.StatusOK || len(calls) != 1 || calls[0].ID != "sdcpp-local" {
		t.Fatalf("status = %d, body = %s, calls = %#v", response.Code, response.Body.String(), calls)
	}
}

func TestImageCapabilitiesAPIMapsProviderAndUpstreamErrors(t *testing.T) {
	tests := []struct {
		name, providerID string
		disable          bool
		err              error
		status           int
	}{
		{name: "missing", providerID: "missing", status: http.StatusNotFound},
		{name: "disabled", providerID: "sdcpp-local", disable: true, status: http.StatusBadRequest},
		{name: "timeout", providerID: "sdcpp-local", err: context.DeadlineExceeded, status: http.StatusGatewayTimeout},
		{name: "upstream", providerID: "sdcpp-local", err: &sdcpp.HTTPError{StatusCode: 500, Body: "failed"}, status: http.StatusBadGateway},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newImageConfigWebFixture(t, capabilityClientStub{err: test.err})
			if test.disable {
				images := fixture.repository.Snapshot().Images
				images.Providers[0].Enabled = false
				if _, err := fixture.repository.UpdateImages(images); err != nil {
					t.Fatal(err)
				}
			}
			response := fixture.request(http.MethodGet, "/api/v1/images/providers/"+test.providerID+"/capabilities", nil)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestImageConfigAPIRedactsStorageFailure(t *testing.T) {
	fixture := newImageConfigWebFixture(t, capabilityClientStub{})
	if err := os.Chmod(fixture.root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(fixture.root, 0o700) })
	configuration, _ := json.Marshal(fixture.repository.Snapshot().Images)
	response := fixture.request(http.MethodPut, "/api/v1/images/config", configuration)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), fixture.root) {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

type capabilityClientStub struct {
	result sdcpp.Capabilities
	err    error
	calls  *[]sdcpp.ImageProvider
}

func (client capabilityClientStub) Capabilities(_ context.Context, provider sdcpp.ImageProvider) (sdcpp.Capabilities, error) {
	if client.calls != nil {
		*client.calls = append(*client.calls, provider)
	}
	return client.result, client.err
}

type imageConfigWebFixture struct {
	handler    http.Handler
	repository *config.Repository
	root       string
}

func newImageConfigWebFixture(t *testing.T, client capabilityClientStub) imageConfigWebFixture {
	t.Helper()
	root := t.TempDir()
	repository, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if client.calls == nil {
		calls := []sdcpp.ImageProvider{}
		client.calls = &calls
	}
	handler := NewHandler(Options{
		Version: "test", Config: repository.Snapshot(), ConfigRepository: repository, ImageCapabilities: client,
	})
	return imageConfigWebFixture{handler: handler, repository: repository, root: root}
}

func (fixture imageConfigWebFixture) request(method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}

var _ ImageCapabilitiesClient = capabilityClientStub{}
