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
	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

func TestVideoConfigAPIReadsAllPresetKindsAndRejectsEscapingCLIOutput(t *testing.T) {
	root := t.TempDir()
	repository, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Options{Config: repository.Snapshot(), ConfigRepository: repository})
	videos := videoconfig.Config{
		HTTPProviders: repository.Snapshot().Videos.HTTPProviders,
		CLIPresets: []videoconfig.CLIPreset{{
			ID: "cli-contract", Name: "CLI Contract", Enabled: true, ExecutionKind: videoconfig.ExecutionLocalCLI,
			CommandTemplate: "printf video > {{OUTPUT_PATH}}", WorkDir: root, Env: map[string]string{"MODE": "test"},
			TimeoutSeconds: 2, StopGraceSeconds: 0, LogBufferBytes: 1024, OutputRelativePath: "outputs/contract.webm",
			OutputMediaType: "video/webm", OutputExtension: ".webm", MaxOutputBytes: 2048, DefaultParams: json.RawMessage(`{"seed":7}`),
		}},
		TailFramePresets: []videoconfig.TailFramePreset{{
			ID: "tail-contract", Name: "Tail Contract", Enabled: true, CommandTemplate: "printf image > {{OUTPUT_IMAGE}}",
			TimeoutSeconds: 2, StopGraceSeconds: 0, MaxImageBytes: 1024, OutputExtension: ".png",
		}},
	}
	body, err := json.Marshal(videos)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/videos/config", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/videos/config", nil))
	var decoded videoconfig.Config
	if err := json.NewDecoder(get.Body).Decode(&decoded); get.Code != http.StatusOK || err != nil {
		t.Fatalf("GET status=%d body=%s decode=%v", get.Code, get.Body.String(), err)
	}
	if len(decoded.HTTPProviders) != 1 || decoded.HTTPProviders[0].ID != "sdcpp-video-local" || decoded.HTTPProviders[0].BaseURL != "http://127.0.0.1:1234" {
		t.Fatalf("HTTP providers = %#v", decoded.HTTPProviders)
	}
	if len(decoded.CLIPresets) != 1 || decoded.CLIPresets[0].ID != "cli-contract" || decoded.CLIPresets[0].OutputRelativePath != "outputs/contract.webm" || decoded.CLIPresets[0].Env["MODE"] != "test" {
		t.Fatalf("CLI presets = %#v", decoded.CLIPresets)
	}
	if len(decoded.TailFramePresets) != 1 || decoded.TailFramePresets[0].ID != "tail-contract" || decoded.TailFramePresets[0].OutputExtension != ".png" {
		t.Fatalf("tail presets = %#v", decoded.TailFramePresets)
	}

	invalid := decoded.Clone()
	invalid.CLIPresets[0].OutputRelativePath = "outputs/../../outside.webm"
	invalidBody, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, httptest.NewRequest(http.MethodPut, "/api/v1/videos/config", bytes.NewReader(invalidBody)))
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("escaping output status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}
	if got := repository.Snapshot().Videos.CLIPresets[0].OutputRelativePath; got != "outputs/contract.webm" {
		t.Fatalf("invalid PUT mutated output_relative_path to %q", got)
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

func TestVideoCapabilitiesAPIErrorsAndUnsupportedMode(t *testing.T) {
	for _, tc := range []struct {
		name, id  string
		disable   bool
		err       error
		want      int
		supported bool
	}{
		{"missing", "missing", false, nil, 404, false}, {"disabled", "sdcpp-video-local", true, nil, 400, false},
		{"timeout", "sdcpp-video-local", false, context.DeadlineExceeded, 504, false}, {"upstream", "sdcpp-video-local", false, &sdcpp.HTTPError{StatusCode: 500, Body: "bad"}, 502, false},
		{"unsupported", "sdcpp-video-local", false, nil, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			repo, err := config.OpenRepository(filepath.Join(root, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.disable {
				videos := repo.Snapshot().Videos
				videos.HTTPProviders[0].Enabled = false
				if _, err := repo.UpdateVideos(videos); err != nil {
					t.Fatal(err)
				}
			}
			h := NewHandler(Options{Config: repo.Snapshot(), ConfigRepository: repo, VideoCapabilities: videoCapabilitiesStub{result: sdcpp.Capabilities{SupportedModes: []string{"img_gen"}}, err: tc.err}})
			r := httptest.NewRecorder()
			h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/videos/providers/"+tc.id+"/capabilities", nil))
			if r.Code != tc.want {
				t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
			}
			if tc.want == 200 && !bytes.Contains(r.Body.Bytes(), []byte(`"video_generation_supported":false`)) {
				t.Fatal(r.Body.String())
			}
		})
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
