package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var ErrResponseTooLarge = errors.New("worker response is too large")

const defaultMaxResponseBytes = int64(MaxLogBufferBytes + (1 << 20))

type Client struct {
	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

type ClientError struct {
	StatusCode int
	Code       string
	Message    string
}

func (err *ClientError) Error() string {
	return fmt.Sprintf("worker API %s (HTTP %d): %s", err.Code, err.StatusCode, err.Message)
}

type LogEvent struct {
	Kind   string
	Offset int64
	Data   []byte
}

func (client Client) Health(ctx context.Context) (HealthResponse, error) {
	var response HealthResponse
	if err := client.doJSON(ctx, http.MethodGet, "/api/v1/health", nil, &response); err != nil {
		return HealthResponse{}, err
	}
	if response.Status != "ok" || response.InstanceID == "" {
		return HealthResponse{}, fmt.Errorf("worker returned an invalid health response")
	}
	return response, nil
}

func (client Client) Status(ctx context.Context) (StatusResponse, error) {
	var response StatusResponse
	if err := client.doJSON(ctx, http.MethodGet, "/api/v1/process", nil, &response); err != nil {
		return StatusResponse{}, err
	}
	return response, nil
}

func (client Client) Start(ctx context.Context, request StartRequest) (Run, error) {
	if err := request.Validate(); err != nil {
		return Run{}, err
	}
	var response Run
	if err := client.doJSON(ctx, http.MethodPost, "/api/v1/process/start", request, &response); err != nil {
		return Run{}, err
	}
	return response, nil
}

func (client Client) Stop(ctx context.Context, runID string) (Run, error) {
	var response Run
	path := "/api/v1/process/" + url.PathEscape(runID) + "/stop"
	if err := client.doJSON(ctx, http.MethodPost, path, nil, &response); err != nil {
		return Run{}, err
	}
	return response, nil
}

func (client Client) Logs(ctx context.Context, runID string) (LogSnapshot, error) {
	response, err := client.do(ctx, http.MethodGet, "/api/v1/process/"+url.PathEscape(runID)+"/logs", nil)
	if err != nil {
		return LogSnapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return LogSnapshot{}, client.decodeAPIError(response)
	}
	contents, err := readLimited(response.Body, client.responseLimit())
	if err != nil {
		return LogSnapshot{}, err
	}
	start, err := parseOffsetHeader(response, "X-Log-Start-Offset")
	if err != nil {
		return LogSnapshot{}, err
	}
	end, err := parseOffsetHeader(response, "X-Log-End-Offset")
	if err != nil {
		return LogSnapshot{}, err
	}
	if start < 0 || end < start || end-start != int64(len(contents)) {
		return LogSnapshot{}, fmt.Errorf("worker returned inconsistent log offsets")
	}
	return LogSnapshot{StartOffset: start, EndOffset: end, Data: contents}, nil
}

func (client Client) SubscribeLogs(ctx context.Context, runID string) (<-chan LogEvent, <-chan error, error) {
	response, err := client.do(ctx, http.MethodGet, "/api/v1/process/"+url.PathEscape(runID)+"/logs/events", nil)
	if err != nil {
		return nil, nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, nil, client.decodeAPIError(response)
	}
	events := make(chan LogEvent, 16)
	failures := make(chan error, 1)
	go client.readLogEvents(ctx, response.Body, events, failures)
	return events, failures, nil
}

func (client Client) doJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode worker request: %w", err)
		}
		if int64(len(encoded)) > MaxRequestBytes {
			return fmt.Errorf("worker request exceeds %d bytes", MaxRequestBytes)
		}
		body = bytes.NewReader(encoded)
	}
	response, err := client.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return client.decodeAPIError(response)
	}
	contents, err := readLimited(response.Body, client.responseLimit())
	if err != nil {
		return err
	}
	if err := json.Unmarshal(contents, output); err != nil {
		return fmt.Errorf("decode worker response: %w", err)
	}
	return nil
}

func (client Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	base, err := client.parsedBaseURL()
	if err != nil {
		return nil, err
	}
	endpoint := *base
	endpoint.Path = path
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create worker request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	httpClient := http.DefaultClient
	if client.HTTPClient != nil {
		httpClient = client.HTTPClient
	}
	bounded := *httpClient
	bounded.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := bounded.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call worker: %w", err)
	}
	return response, nil
}

func (client Client) parsedBaseURL() (*url.URL, error) {
	parsed, err := url.Parse(client.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("worker base URL must be an absolute HTTP URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("worker base URL must not contain credentials, a path, query, or fragment")
	}
	parsed.Path = ""
	return parsed, nil
}

func (client Client) decodeAPIError(response *http.Response) error {
	contents, err := readLimited(response.Body, client.responseLimit())
	if err != nil {
		return err
	}
	envelope := ErrorEnvelope{}
	if err := json.Unmarshal(contents, &envelope); err == nil && envelope.Error.Code != "" {
		return &ClientError{StatusCode: response.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	message := strings.TrimSpace(string(contents))
	if message == "" {
		message = response.Status
	}
	return &ClientError{StatusCode: response.StatusCode, Code: fmt.Sprintf("http_%d", response.StatusCode), Message: truncateError(message)}
}

func (client Client) readLogEvents(ctx context.Context, body io.ReadCloser, events chan<- LogEvent, failures chan<- error) {
	defer body.Close()
	defer close(events)
	defer close(failures)
	scanner := bufio.NewScanner(body)
	maxEvent := client.responseLimit()
	if maxEvent > int64(^uint(0)>>1) {
		maxEvent = int64(^uint(0) >> 1)
	}
	scanner.Buffer(make([]byte, 64<<10), int(maxEvent))
	var kind, data string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			kind = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if data != "" {
				failures <- fmt.Errorf("decode log event: multiple data lines are unsupported")
				return
			}
			data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if kind == "" && data == "" {
				continue
			}
			event, err := decodeLogEvent(kind, data)
			if err != nil {
				failures <- err
				return
			}
			select {
			case events <- event:
			case <-ctx.Done():
				failures <- ctx.Err()
				return
			}
			kind, data = "", ""
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			failures <- ctx.Err()
		} else {
			failures <- fmt.Errorf("read worker log stream: %w", err)
		}
		return
	}
	if ctx.Err() != nil {
		failures <- ctx.Err()
	}
}

func decodeLogEvent(kind, data string) (LogEvent, error) {
	if kind != "snapshot" && kind != "chunk" {
		return LogEvent{}, fmt.Errorf("decode log event: unsupported event %q", kind)
	}
	var payload struct {
		Offset int64  `json:"offset"`
		Data   string `json:"data"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return LogEvent{}, fmt.Errorf("decode log event: %w", err)
	}
	if payload.Offset < 0 {
		return LogEvent{}, fmt.Errorf("decode log event: offset must not be negative")
	}
	return LogEvent{Kind: kind, Offset: payload.Offset, Data: []byte(payload.Data)}, nil
}

func (client Client) responseLimit() int64 {
	if client.MaxResponseBytes > 0 {
		return client.MaxResponseBytes
	}
	return defaultMaxResponseBytes
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > limit {
		return nil, ErrResponseTooLarge
	}
	return contents, nil
}

func parseOffsetHeader(response *http.Response, name string) (int64, error) {
	value := response.Header.Get(name)
	offset, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("worker returned invalid %s header", name)
	}
	return offset, nil
}
