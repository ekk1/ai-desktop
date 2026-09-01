package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
)

func TestVideoConfigAPIReplacesAllPresetKinds(t *testing.T) {
	root := t.TempDir()
	repository, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Options{Config: repository.Snapshot(), ConfigRepository: repository})
	videos := repository.Snapshot().Videos
	videos.CLIPresets = videos.CLIPresets[:0]
	body, err := json.Marshal(videos)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/videos/config", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(repository.Snapshot().Videos.CLIPresets) != 0 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodPut, "/api/v1/videos/config", bytes.NewReader([]byte(`{"unknown":true}`))))
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown status=%d body=%s", unknown.Code, unknown.Body.String())
	}
}

func TestVideoCapabilitiesAPIMapsSavedProviderAndMode(t *testing.T) {
	root := t.TempDir()
	repository, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	calls := []sdcpp.ImageProvider{}
	client := videoCapabilitiesStub{result: sdcpp.Capabilities{CurrentMode: "img_gen", SupportedModes: []string{"img_gen", "vid_gen"}}, calls: &calls}
	handler := NewHandler(Options{Config: repository.Snapshot(), ConfigRepository: repository, VideoCapabilities: client})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/videos/providers/sdcpp-video-local/capabilities", nil))
	if response.Code != http.StatusOK || len(calls) != 1 || calls[0].BaseURL != repository.Snapshot().Videos.HTTPProviders[0].BaseURL || !bytes.Contains(response.Body.Bytes(), []byte(`"video_generation_supported":true`)) {
		t.Fatalf("status=%d body=%s calls=%#v", response.Code, response.Body.String(), calls)
	}
}

func TestSafeDataLocationRedactsAbsolutePathsAndRejectsOutside(t *testing.T) {
	root := t.TempDir()
	location, err := safeDataLocation(root, filepath.Join(root, "videos", "video-logs", "attempt.log"))
	if err != nil || location != "videos/video-logs/attempt.log" {
		t.Fatalf("location=%q err=%v", location, err)
	}
	if _, err := safeDataLocation(root, filepath.Join(filepath.Dir(root), "escape.log")); err == nil {
		t.Fatal("outside location was exposed")
	}
}

type videoCapabilitiesStub struct {
	result sdcpp.Capabilities
	err    error
	calls  *[]sdcpp.ImageProvider
}

func (c videoCapabilitiesStub) Capabilities(_ context.Context, p sdcpp.ImageProvider) (sdcpp.Capabilities, error) {
	if c.calls != nil {
		*c.calls = append(*c.calls, p)
	}
	return c.result, c.err
}
