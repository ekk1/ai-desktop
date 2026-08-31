package sdcpp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrResponseTooLarge = errors.New("stable-diffusion.cpp response exceeds configured limit")
	ErrRedirect         = errors.New("stable-diffusion.cpp redirect rejected")
)

type ModelInfo struct {
	Name string `json:"name"`
	Stem string `json:"stem"`
	Path string `json:"path"`
}

type LoraInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type UpscalerInfo struct {
	Name string `json:"name"`
}

type CapabilityLimits struct {
	MinWidth      int `json:"min_width"`
	MaxWidth      int `json:"max_width"`
	MinHeight     int `json:"min_height"`
	MaxHeight     int `json:"max_height"`
	MaxBatchCount int `json:"max_batch_count"`
	MaxQueueSize  int `json:"max_queue_size"`
}

type Capabilities struct {
	Model               ModelInfo                  `json:"model"`
	CurrentMode         string                     `json:"current_mode"`
	SupportedModes      []string                   `json:"supported_modes"`
	Defaults            json.RawMessage            `json:"defaults"`
	OutputFormats       []string                   `json:"output_formats"`
	Features            map[string]bool            `json:"features"`
	DefaultsByMode      map[string]json.RawMessage `json:"defaults_by_mode"`
	OutputFormatsByMode map[string][]string        `json:"output_formats_by_mode"`
	FeaturesByMode      map[string]json.RawMessage `json:"features_by_mode"`
	Samplers            []string                   `json:"samplers"`
	Schedulers          []string                   `json:"schedulers"`
	Loras               []LoraInfo                 `json:"loras"`
	Upscalers           []UpscalerInfo             `json:"upscalers"`
	Limits              CapabilityLimits           `json:"limits"`
}

type Submission struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Created int64  `json:"created"`
	PollURL string `json:"poll_url"`
}

type Job struct {
	ID            string       `json:"id"`
	Kind          string       `json:"kind"`
	Status        string       `json:"status"`
	Created       int64        `json:"created"`
	Started       *int64       `json:"started"`
	Completed     *int64       `json:"completed"`
	QueuePosition int          `json:"queue_position"`
	Result        *JobResult   `json:"result"`
	Error         *RemoteError `json:"error"`
}

type JobResult struct {
	OutputFormat string     `json:"output_format"`
	Images       []JobImage `json:"images"`
}

type JobImage struct {
	Index   int    `json:"index"`
	B64JSON string `json:"b64_json"`
}

type RemoteError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (err *HTTPError) Error() string {
	if err.Body == "" {
		return fmt.Sprintf("stable-diffusion.cpp returned HTTP %d", err.StatusCode)
	}
	return fmt.Sprintf("stable-diffusion.cpp returned HTTP %d: %s", err.StatusCode, err.Body)
}

type Client struct{}

func (Client) Capabilities(ctx context.Context, provider ImageProvider) (Capabilities, error) {
	var result Capabilities
	if err := requestJSON(ctx, imageHTTPProvider(provider), http.MethodGet, "/sdcpp/v1/capabilities", nil, anyTwoHundred, &result); err != nil {
		return Capabilities{}, err
	}
	return result, nil
}

func (Client) Submit(ctx context.Context, provider ImageProvider, body []byte) (Submission, error) {
	var result Submission
	if err := requestJSON(ctx, imageHTTPProvider(provider), http.MethodPost, "/sdcpp/v1/img_gen", body, exactly(http.StatusAccepted), &result); err != nil {
		return Submission{}, err
	}
	if result.ID == "" || result.Kind != "img_gen" || !knownJobStatus(result.Status) {
		return Submission{}, fmt.Errorf("invalid stable-diffusion.cpp image submission")
	}
	return result, nil
}

func (Client) Job(ctx context.Context, provider ImageProvider, jobID string) (Job, error) {
	path, err := escapedJobPath(jobID)
	if err != nil {
		return Job{}, err
	}
	var result Job
	if err := requestJSON(ctx, imageHTTPProvider(provider), http.MethodGet, path, nil, anyTwoHundred, &result); err != nil {
		return Job{}, err
	}
	if err := validateJob(result, jobID); err != nil {
		return Job{}, err
	}
	return result, nil
}

func (Client) Cancel(ctx context.Context, provider ImageProvider, jobID string) error {
	path, err := escapedJobPath(jobID)
	if err != nil {
		return err
	}
	return requestJSON(ctx, imageHTTPProvider(provider), http.MethodPost, path+"/cancel", []byte{}, exactly(http.StatusOK), nil)
}

type statusAcceptance func(int) bool

type httpJSONProvider struct {
	BaseURL               string
	Headers               map[string]string
	ConnectTimeoutSeconds int
	MaxResponseBytes      int64
	MaxErrorBytes         int64
	Validate              func() error
}

func imageHTTPProvider(provider ImageProvider) httpJSONProvider {
	return httpJSONProvider{
		BaseURL: provider.BaseURL, Headers: provider.Headers, ConnectTimeoutSeconds: provider.ConnectTimeoutSeconds,
		MaxResponseBytes: provider.MaxResponseBytes, MaxErrorBytes: 4096,
		Validate: func() error {
			if err := (ImageConfig{Providers: []ImageProvider{provider}}).Validate(); err != nil {
				return fmt.Errorf("image provider: %w", err)
			}
			return nil
		},
	}
}

func requestJSON(
	ctx context.Context,
	provider httpJSONProvider,
	method, path string,
	body []byte,
	accept statusAcceptance,
	destination any,
) error {
	if err := provider.Validate(); err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, provider.BaseURL+path, reader)
	if err != nil {
		return fmt.Errorf("create stable-diffusion.cpp request: %w", err)
	}
	for name, value := range provider.Headers {
		request.Header.Set(name, value)
	}
	if body != nil && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", "application/json")
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout: time.Duration(provider.ConnectTimeoutSeconds) * time.Second,
		}).DialContext,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirect
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("call stable-diffusion.cpp: %w", err)
	}
	defer response.Body.Close()
	if !accept(response.StatusCode) {
		return readHTTPError(response, provider.MaxErrorBytes)
	}
	contents, err := readBounded(response.Body, provider.MaxResponseBytes)
	if err != nil {
		return err
	}
	if destination == nil {
		return nil
	}
	if err := decodeOneJSON(contents, destination); err != nil {
		return fmt.Errorf("decode stable-diffusion.cpp response: %w", err)
	}
	return nil
}

func readHTTPError(response *http.Response, maximum int64) error {
	contents, _ := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if int64(len(contents)) > maximum {
		contents = contents[:maximum]
	}
	return &HTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(contents))}
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read stable-diffusion.cpp response: %w", err)
	}
	if int64(len(contents)) > maximum {
		return nil, ErrResponseTooLarge
	}
	return contents, nil
}

func decodeOneJSON(contents []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func anyTwoHundred(status int) bool {
	return status >= 200 && status < 300
}

func exactly(expected int) statusAcceptance {
	return func(status int) bool { return status == expected }
}

func knownJobStatus(status string) bool {
	return status == "queued" || status == "generating" || status == "completed" || status == "failed" || status == "cancelled"
}

func validateJob(job Job, expectedID string) error {
	if job.ID == "" || job.ID != expectedID {
		return fmt.Errorf("stable-diffusion.cpp job identity does not match request")
	}
	if job.Kind != "img_gen" {
		return fmt.Errorf("stable-diffusion.cpp job kind %q is not img_gen", job.Kind)
	}
	if !knownJobStatus(job.Status) {
		return fmt.Errorf("stable-diffusion.cpp job status %q is invalid", job.Status)
	}
	if job.QueuePosition < 0 {
		return fmt.Errorf("stable-diffusion.cpp queue position cannot be negative")
	}
	switch job.Status {
	case "completed":
		if job.Result == nil || job.Result.OutputFormat == "" || len(job.Result.Images) == 0 || job.Error != nil {
			return fmt.Errorf("stable-diffusion.cpp completed job result is invalid")
		}
		indexes := make(map[int]struct{}, len(job.Result.Images))
		for _, image := range job.Result.Images {
			if image.Index < 0 || image.B64JSON == "" {
				return fmt.Errorf("stable-diffusion.cpp completed job image is invalid")
			}
			if _, duplicate := indexes[image.Index]; duplicate {
				return fmt.Errorf("stable-diffusion.cpp completed job image index is duplicated")
			}
			indexes[image.Index] = struct{}{}
		}
	case "failed", "cancelled":
		if job.Result != nil || job.Error == nil || job.Error.Code == "" || job.Error.Message == "" {
			return fmt.Errorf("stable-diffusion.cpp terminal job error is invalid")
		}
	default:
		if job.Result != nil || job.Error != nil {
			return fmt.Errorf("stable-diffusion.cpp active job payload is invalid")
		}
	}
	return nil
}

func escapedJobPath(jobID string) (string, error) {
	if jobID == "" {
		return "", fmt.Errorf("stable-diffusion.cpp job ID is required")
	}
	return "/sdcpp/v1/jobs/" + url.PathEscape(jobID), nil
}
