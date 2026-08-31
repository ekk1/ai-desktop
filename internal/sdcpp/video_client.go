package sdcpp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"

	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

var ErrRequestTooLarge = errors.New("stable-diffusion.cpp request exceeds configured limit")

type VideoSubmission struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	PollURL string `json:"poll_url"`
}

type VideoJobResult struct {
	OutputFormat string `json:"output_format"`
	MIMEType     string `json:"mime_type"`
	FPS          int    `json:"fps"`
	FrameCount   int    `json:"frame_count"`
	B64JSON      string `json:"b64_json"`
}

type VideoJob struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	QueuePosition int             `json:"queue_position"`
	Result        *VideoJobResult `json:"result"`
	Error         *RemoteError    `json:"error"`
}

type VideoClient struct{}

func (VideoClient) Submit(ctx context.Context, provider videoconfig.HTTPProvider, body []byte) (VideoSubmission, error) {
	httpProvider, err := videoHTTPProvider(provider)
	if err != nil {
		return VideoSubmission{}, err
	}
	if int64(len(body)) > provider.MaxRequestBytes {
		return VideoSubmission{}, ErrRequestTooLarge
	}

	var result VideoSubmission
	if err := requestJSON(ctx, httpProvider, http.MethodPost, "/sdcpp/v1/vid_gen", body, exactly(http.StatusAccepted), &result); err != nil {
		return VideoSubmission{}, err
	}
	if result.ID == "" || result.Kind != "vid_gen" || !knownJobStatus(result.Status) {
		return VideoSubmission{}, fmt.Errorf("invalid stable-diffusion.cpp video submission")
	}
	path, err := escapedJobPath(result.ID)
	if err != nil {
		return VideoSubmission{}, err
	}
	result.PollURL = path
	return result, nil
}

func (VideoClient) Job(ctx context.Context, provider videoconfig.HTTPProvider, jobID string) (VideoJob, error) {
	path, err := escapedJobPath(jobID)
	if err != nil {
		return VideoJob{}, err
	}
	httpProvider, err := videoHTTPProvider(provider)
	if err != nil {
		return VideoJob{}, err
	}
	var result VideoJob
	if err := requestJSON(ctx, httpProvider, http.MethodGet, path, nil, exactly(http.StatusOK), &result); err != nil {
		return VideoJob{}, err
	}
	if err := validateVideoJob(result, jobID, provider.MaxVideoBytes); err != nil {
		return VideoJob{}, err
	}
	return result, nil
}

func (VideoClient) Cancel(ctx context.Context, provider videoconfig.HTTPProvider, jobID string) error {
	path, err := escapedJobPath(jobID)
	if err != nil {
		return err
	}
	httpProvider, err := videoHTTPProvider(provider)
	if err != nil {
		return err
	}
	return requestJSON(ctx, httpProvider, http.MethodPost, path+"/cancel", []byte{}, exactly(http.StatusOK), nil)
}

func videoHTTPProvider(provider videoconfig.HTTPProvider) (httpJSONProvider, error) {
	if err := (videoconfig.Config{HTTPProviders: []videoconfig.HTTPProvider{provider}}).Validate(); err != nil {
		return httpJSONProvider{}, fmt.Errorf("video provider: %w", err)
	}
	return httpJSONProvider{
		BaseURL: provider.BaseURL, Headers: provider.Headers, ConnectTimeoutSeconds: provider.ConnectTimeoutSeconds,
		MaxResponseBytes: videoJobResponseLimit(provider.MaxVideoBytes), MaxErrorBytes: provider.MaxErrorBytes,
		Validate: func() error { return nil },
	}, nil
}

func videoJobResponseLimit(maxVideoBytes int64) int64 {
	return 4*((maxVideoBytes+2)/3) + (1 << 20)
}

func validateVideoJob(job VideoJob, expectedID string, maxVideoBytes int64) error {
	if job.ID == "" || job.ID != expectedID {
		return fmt.Errorf("stable-diffusion.cpp job identity does not match request")
	}
	if job.Kind != "vid_gen" {
		return fmt.Errorf("stable-diffusion.cpp job kind %q is not vid_gen", job.Kind)
	}
	if !knownJobStatus(job.Status) {
		return fmt.Errorf("stable-diffusion.cpp job status %q is invalid", job.Status)
	}
	if job.QueuePosition < 0 {
		return fmt.Errorf("stable-diffusion.cpp queue position cannot be negative")
	}
	switch job.Status {
	case "completed":
		if err := validateVideoJobResult(job.Result, maxVideoBytes); err != nil {
			return fmt.Errorf("stable-diffusion.cpp completed video job result is invalid: %w", err)
		}
		if job.Error != nil {
			return fmt.Errorf("stable-diffusion.cpp completed video job result is invalid")
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

func validateVideoJobResult(result *VideoJobResult, maxVideoBytes int64) error {
	if result == nil || result.FPS <= 0 || result.FrameCount <= 0 {
		return errors.New("missing video result")
	}
	decodedBytes, valid := decodedVideoBytes(result.B64JSON)
	if !valid {
		return errors.New("missing video result")
	}
	if decodedBytes > maxVideoBytes {
		return ErrResponseTooLarge
	}
	expectedMIME, known := map[string]string{
		"webm": "video/webm",
		"webp": "image/webp",
		"avi":  "video/x-msvideo",
	}[result.OutputFormat]
	if !known || result.MIMEType != expectedMIME {
		return errors.New("invalid video output format")
	}
	return nil
}

func decodedVideoBytes(value string) (int64, bool) {
	if !validVideoBase64(value) {
		return 0, false
	}
	decodedBytes := int64(len(value)/4) * 3
	if value[len(value)-1] == '=' {
		decodedBytes--
	}
	if value[len(value)-2] == '=' {
		decodedBytes--
	}
	return decodedBytes, true
}

func validVideoBase64(value string) bool {
	if len(value) == 0 || len(value)%4 != 0 {
		return false
	}
	padding := false
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case character >= 'A' && character <= 'Z', character >= 'a' && character <= 'z', character >= '0' && character <= '9', character == '+', character == '/':
			if padding {
				return false
			}
		case character == '=':
			padding = true
			if len(value)-index > 2 {
				return false
			}
		default:
			return false
		}
	}
	_, err := base64.StdEncoding.Strict().DecodeString(value[len(value)-4:])
	return err == nil
}
