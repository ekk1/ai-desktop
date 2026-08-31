package videoconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path/filepath"
	"strings"
)

const (
	maximumHTTPTimeoutSeconds       = 86400
	maximumPollMilliseconds         = 10000
	maximumConcurrentJobs           = 16
	maximumRequestBytes       int64 = 2 << 30
	maximumErrorBytes         int64 = 64 << 10
	maximumVideoBytes         int64 = 4 << 30
	maximumInputBytes         int64 = 1 << 30
	maximumTemplateBytes            = 64 << 10
	maximumLogBufferBytes           = 16 << 20
	maximumStopGraceSeconds         = 3600
)

var defaultHTTPParams = json.RawMessage(`{"width":832,"height":480,"seed":-1,"output_format":"webm","sample_params":{"sample_steps":28}}`)

type Config struct {
	HTTPProviders    []HTTPProvider    `json:"http_providers"`
	CLIPresets       []CLIPreset       `json:"cli_presets"`
	TailFramePresets []TailFramePreset `json:"tail_frame_presets"`
}

type ExecutionKind string

const (
	ExecutionHTTP     ExecutionKind = "http"
	ExecutionLocalCLI ExecutionKind = "local_cli"
)

type HTTPProvider struct {
	ID                       string            `json:"id"`
	Name                     string            `json:"name"`
	BaseURL                  string            `json:"base_url"`
	Headers                  map[string]string `json:"headers"`
	ConnectTimeoutSeconds    int               `json:"connect_timeout_seconds"`
	JobTimeoutSeconds        int               `json:"job_timeout_seconds"`
	PollIntervalMilliseconds int               `json:"poll_interval_milliseconds"`
	MaxRequestBytes          int64             `json:"max_request_bytes"`
	MaxErrorBytes            int64             `json:"max_error_bytes"`
	MaxVideoBytes            int64             `json:"max_video_bytes"`
	MaxInputImageBytes       int64             `json:"max_input_image_bytes"`
	MaxConcurrentJobs        int               `json:"max_concurrent_jobs"`
	Enabled                  bool              `json:"enabled"`
	DefaultParams            json.RawMessage   `json:"default_params"`
}

type CLIPreset struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	Enabled                bool              `json:"enabled"`
	ExecutionKind          ExecutionKind     `json:"execution_kind"`
	PrepareCommandTemplate string            `json:"prepare_command_template"`
	CommandTemplate        string            `json:"command_template"`
	WorkDir                string            `json:"work_dir"`
	Env                    map[string]string `json:"env"`
	TimeoutSeconds         int               `json:"timeout_seconds"`
	StopGraceSeconds       int               `json:"stop_grace_seconds"`
	LogBufferBytes         int               `json:"log_buffer_bytes"`
	OutputRelativePath     string            `json:"output_relative_path"`
	OutputMediaType        string            `json:"output_media_type"`
	OutputExtension        string            `json:"output_extension"`
	MaxOutputBytes         int64             `json:"max_output_bytes"`
	DefaultParams          json.RawMessage   `json:"default_params"`
}

type TailFramePreset struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Enabled          bool   `json:"enabled"`
	CommandTemplate  string `json:"command_template"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
	StopGraceSeconds int    `json:"stop_grace_seconds"`
	MaxImageBytes    int64  `json:"max_image_bytes"`
	OutputExtension  string `json:"output_extension"`
}

func Default() Config {
	return Config{HTTPProviders: []HTTPProvider{{
		ID: "sdcpp-video-local", Name: "stable-diffusion.cpp Video Local", BaseURL: "http://127.0.0.1:1234",
		Headers: map[string]string{}, ConnectTimeoutSeconds: 10, JobTimeoutSeconds: 86400,
		PollIntervalMilliseconds: 750, MaxRequestBytes: 384 << 20, MaxErrorBytes: 64 << 10,
		MaxVideoBytes: 1 << 30, MaxInputImageBytes: 256 << 20, MaxConcurrentJobs: 1, Enabled: true,
		DefaultParams: DefaultHTTPParams(),
	}}, CLIPresets: []CLIPreset{}, TailFramePresets: []TailFramePreset{}}
}

func DefaultHTTPParams() json.RawMessage {
	return append(json.RawMessage(nil), defaultHTTPParams...)
}

func (configuration Config) Validate() error {
	if err := validateUniqueHTTPProviders(configuration.HTTPProviders); err != nil {
		return err
	}
	if err := validateUniqueCLIPresets(configuration.CLIPresets); err != nil {
		return err
	}
	if err := validateUniqueTailFramePresets(configuration.TailFramePresets); err != nil {
		return err
	}
	return nil
}

func (configuration Config) Clone() Config {
	clone := configuration
	clone.HTTPProviders = make([]HTTPProvider, len(configuration.HTTPProviders))
	for index, provider := range configuration.HTTPProviders {
		clone.HTTPProviders[index] = provider
		clone.HTTPProviders[index].Headers = cloneStringMap(provider.Headers)
		clone.HTTPProviders[index].DefaultParams = cloneRawMessage(provider.DefaultParams)
	}
	clone.CLIPresets = make([]CLIPreset, len(configuration.CLIPresets))
	for index, preset := range configuration.CLIPresets {
		clone.CLIPresets[index] = preset
		clone.CLIPresets[index].Env = cloneStringMap(preset.Env)
		clone.CLIPresets[index].DefaultParams = cloneRawMessage(preset.DefaultParams)
	}
	clone.TailFramePresets = append([]TailFramePreset(nil), configuration.TailFramePresets...)
	return clone
}

func validateUniqueHTTPProviders(providers []HTTPProvider) error {
	seen := make(map[string]struct{}, len(providers))
	for index, provider := range providers {
		if err := validateID(provider.ID); err != nil {
			return fmt.Errorf("HTTP provider %d ID: %w", index, err)
		}
		if _, duplicate := seen[provider.ID]; duplicate {
			return fmt.Errorf("duplicate HTTP provider ID %q", provider.ID)
		}
		seen[provider.ID] = struct{}{}
		if err := provider.validate(); err != nil {
			return fmt.Errorf("HTTP provider %q: %w", provider.ID, err)
		}
	}
	return nil
}

func validateUniqueCLIPresets(presets []CLIPreset) error {
	seen := make(map[string]struct{}, len(presets))
	for index, preset := range presets {
		if err := validateID(preset.ID); err != nil {
			return fmt.Errorf("CLI preset %d ID: %w", index, err)
		}
		if _, duplicate := seen[preset.ID]; duplicate {
			return fmt.Errorf("duplicate CLI preset ID %q", preset.ID)
		}
		seen[preset.ID] = struct{}{}
		if err := preset.validate(); err != nil {
			return fmt.Errorf("CLI preset %q: %w", preset.ID, err)
		}
	}
	return nil
}

func validateUniqueTailFramePresets(presets []TailFramePreset) error {
	seen := make(map[string]struct{}, len(presets))
	for index, preset := range presets {
		if err := validateID(preset.ID); err != nil {
			return fmt.Errorf("tail-frame preset %d ID: %w", index, err)
		}
		if _, duplicate := seen[preset.ID]; duplicate {
			return fmt.Errorf("duplicate tail-frame preset ID %q", preset.ID)
		}
		seen[preset.ID] = struct{}{}
		if err := preset.validate(); err != nil {
			return fmt.Errorf("tail-frame preset %q: %w", preset.ID, err)
		}
	}
	return nil
}

func (provider HTTPProvider) validate() error {
	if strings.TrimSpace(provider.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if err := validateBaseURL(provider.BaseURL); err != nil {
		return err
	}
	if err := validateHeaders(provider.Headers); err != nil {
		return err
	}
	if err := validateTimeout(provider.ConnectTimeoutSeconds, "connect timeout"); err != nil {
		return err
	}
	if err := validateTimeout(provider.JobTimeoutSeconds, "job timeout"); err != nil {
		return err
	}
	if provider.PollIntervalMilliseconds < 100 || provider.PollIntervalMilliseconds > maximumPollMilliseconds {
		return fmt.Errorf("poll interval must be between 100 and %d milliseconds", maximumPollMilliseconds)
	}
	if err := validateBytes(provider.MaxRequestBytes, maximumRequestBytes, "max request bytes"); err != nil {
		return err
	}
	if err := validateBytes(provider.MaxErrorBytes, maximumErrorBytes, "max error bytes"); err != nil {
		return err
	}
	if err := validateBytes(provider.MaxVideoBytes, maximumVideoBytes, "max video bytes"); err != nil {
		return err
	}
	if err := validateBytes(provider.MaxInputImageBytes, maximumInputBytes, "max input image bytes"); err != nil {
		return err
	}
	if provider.MaxConcurrentJobs < 1 || provider.MaxConcurrentJobs > maximumConcurrentJobs {
		return fmt.Errorf("max concurrent jobs must be between 1 and %d", maximumConcurrentJobs)
	}
	if err := validateJSONObject(provider.DefaultParams); err != nil {
		return fmt.Errorf("default params must be one JSON object: %w", err)
	}
	return nil
}

func (preset CLIPreset) validate() error {
	if strings.TrimSpace(preset.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if preset.ExecutionKind != ExecutionLocalCLI {
		return fmt.Errorf("execution kind must be %q", ExecutionLocalCLI)
	}
	if err := validateTemplate(preset.PrepareCommandTemplate, "prepare command template", false); err != nil {
		return err
	}
	if err := validateTemplate(preset.CommandTemplate, "command template", true); err != nil {
		return err
	}
	if preset.WorkDir != "" && !filepath.IsAbs(preset.WorkDir) {
		return fmt.Errorf("work directory must be absolute")
	}
	for key := range preset.Env {
		if !validEnvKey(key) {
			return fmt.Errorf("invalid environment key %q", key)
		}
	}
	if err := validateTimeout(preset.TimeoutSeconds, "timeout"); err != nil {
		return err
	}
	if err := validateStopGrace(preset.StopGraceSeconds); err != nil {
		return err
	}
	if preset.LogBufferBytes < 1 || preset.LogBufferBytes > maximumLogBufferBytes {
		return fmt.Errorf("log buffer bytes must be between 1 and %d", maximumLogBufferBytes)
	}
	if !validOutputRelativePath(preset.OutputRelativePath) {
		return fmt.Errorf("output relative path must remain under outputs/")
	}
	if _, _, err := mime.ParseMediaType(preset.OutputMediaType); err != nil || strings.TrimSpace(preset.OutputMediaType) == "" {
		return fmt.Errorf("output media type must be valid")
	}
	if !validExtension(preset.OutputExtension) {
		return fmt.Errorf("output extension must start with a dot")
	}
	if err := validateBytes(preset.MaxOutputBytes, maximumVideoBytes, "max output bytes"); err != nil {
		return err
	}
	if err := validateJSONObject(preset.DefaultParams); err != nil {
		return fmt.Errorf("default params must be one JSON object: %w", err)
	}
	return nil
}

func (preset TailFramePreset) validate() error {
	if strings.TrimSpace(preset.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if err := validateTemplate(preset.CommandTemplate, "command template", true); err != nil {
		return err
	}
	if err := validateTimeout(preset.TimeoutSeconds, "timeout"); err != nil {
		return err
	}
	if err := validateStopGrace(preset.StopGraceSeconds); err != nil {
		return err
	}
	if err := validateBytes(preset.MaxImageBytes, maximumInputBytes, "max image bytes"); err != nil {
		return err
	}
	switch preset.OutputExtension {
	case ".png", ".jpg", ".jpeg", ".webp":
		return nil
	default:
		return fmt.Errorf("output extension must be .png, .jpg, .jpeg, or .webp")
	}
}

func validateID(id string) error {
	if len(id) < 1 || len(id) > 120 {
		return fmt.Errorf("must contain 1 to 120 safe characters")
	}
	for _, character := range id {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("._-", character) {
			continue
		}
		return fmt.Errorf("contains unsafe character %q", character)
	}
	return nil
}

func validateBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("base URL must be absolute HTTP(S)")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("base URL cannot contain query or fragment")
	}
	if strings.HasSuffix(value, "/") {
		return fmt.Errorf("base URL cannot have a trailing slash")
	}
	return nil
}

func validateHeaders(headers map[string]string) error {
	for name, value := range headers {
		if !validHeaderName(name) {
			return fmt.Errorf("invalid header name %q", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("header %q contains a newline", name)
		}
	}
	return nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func validateTimeout(value int, name string) error {
	if value < 1 || value > maximumHTTPTimeoutSeconds {
		return fmt.Errorf("%s must be between 1 and %d seconds", name, maximumHTTPTimeoutSeconds)
	}
	return nil
}

func validateStopGrace(value int) error {
	if value < 0 || value > maximumStopGraceSeconds {
		return fmt.Errorf("stop grace seconds must be between 0 and %d", maximumStopGraceSeconds)
	}
	return nil
}

func validateBytes(value, maximum int64, name string) error {
	if value < 1 || value > maximum {
		return fmt.Errorf("%s must be between 1 and %d", name, maximum)
	}
	return nil
}

func validateTemplate(value, name string, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maximumTemplateBytes {
		return fmt.Errorf("%s must not exceed %d bytes", name, maximumTemplateBytes)
	}
	return nil
}

func validEnvKey(key string) bool {
	if key == "" || !(key[0] == '_' || key[0] >= 'a' && key[0] <= 'z' || key[0] >= 'A' && key[0] <= 'Z') {
		return false
	}
	for _, character := range key[1:] {
		if character == '_' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func validOutputRelativePath(value string) bool {
	cleaned := filepath.Clean(value)
	prefix := "outputs" + string(filepath.Separator)
	return value != "" && !filepath.IsAbs(value) && strings.HasPrefix(cleaned, prefix)
}

func validExtension(value string) bool {
	return len(value) > 1 && strings.HasPrefix(value, ".") && filepath.Ext(value) == value && !strings.ContainsAny(value, "/\\")
}

func validateJSONObject(contents json.RawMessage) error {
	if len(bytes.TrimSpace(contents)) == 0 {
		return fmt.Errorf("empty JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("top-level value is not a JSON object")
	}
	return nil
}

func cloneStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneRawMessage(source json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), source...)
}
