package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

const maxErrorExcerptBytes = 4096

var (
	ErrHTTPStatus    = errors.New("provider returned a non-success HTTP status")
	ErrResponseLimit = errors.New("provider response exceeded its size limit")
	ErrResponsePath  = errors.New("provider response path is invalid")
	ErrResponseJSON  = errors.New("provider response is invalid JSON")
)

type StatusError struct {
	StatusCode int
	Excerpt    string
}

func (statusError *StatusError) Error() string {
	return fmt.Sprintf("%v: status %d: %s", ErrHTTPStatus, statusError.StatusCode, statusError.Excerpt)
}

func (statusError *StatusError) Unwrap() error {
	return ErrHTTPStatus
}

type ExecutionResult struct {
	Content         string
	StatusCode      int
	ResponseExcerpt string
}

type Executor struct{}

func (Executor) Execute(ctx context.Context, prepared PreparedRequest, onChunk func(string)) (ExecutionResult, error) {
	if prepared.MaxResponseBytes < 1 {
		return ExecutionResult{}, fmt.Errorf("max response bytes must be positive")
	}
	request, err := http.NewRequestWithContext(ctx, prepared.Method, prepared.URL, bytes.NewReader(prepared.Body))
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("create provider request: %w", err)
	}
	for name, value := range prepared.Headers {
		request.Header.Set(name, value)
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout: prepared.ConnectTimeout,
		}).DialContext,
		ForceAttemptHTTP2: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: prepared.TotalTimeout}
	response, err := client.Do(request)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("execute provider request: %w", err)
	}
	defer response.Body.Close()
	result := ExecutionResult{StatusCode: response.StatusCode}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		excerptLimit := int64(maxErrorExcerptBytes)
		if prepared.MaxResponseBytes < excerptLimit {
			excerptLimit = prepared.MaxResponseBytes
		}
		excerpt, readErr := io.ReadAll(io.LimitReader(response.Body, excerptLimit))
		if readErr != nil {
			return result, fmt.Errorf("read provider error response: %w", readErr)
		}
		result.ResponseExcerpt = string(excerpt)
		return result, &StatusError{StatusCode: response.StatusCode, Excerpt: result.ResponseExcerpt}
	}

	switch prepared.ResponseMode {
	case ResponseModeJSON:
		return executeJSONResponse(response.Body, result, prepared, onChunk)
	case ResponseModeSSEJSON:
		return executeSSEResponse(response.Body, result, prepared, onChunk)
	default:
		return result, fmt.Errorf("unsupported response mode %q", prepared.ResponseMode)
	}
}

func executeJSONResponse(
	body io.Reader,
	result ExecutionResult,
	prepared PreparedRequest,
	onChunk func(string),
) (ExecutionResult, error) {
	contents, err := readBounded(body, prepared.MaxResponseBytes)
	if err != nil {
		return result, err
	}
	result.ResponseExcerpt = boundedExcerpt(contents)
	value, err := decodeJSONDocument(contents)
	if err != nil {
		return result, err
	}
	extracted, err := extractPath(value, prepared.ResponseContentPath)
	if err != nil {
		return result, err
	}
	content, ok := extracted.(string)
	if !ok {
		return result, fmt.Errorf("%w: path %q is not a string", ErrResponsePath, prepared.ResponseContentPath)
	}
	result.Content = content
	if onChunk != nil && content != "" {
		onChunk(content)
	}
	return result, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read provider response: %w", err)
	}
	if int64(len(contents)) > limit {
		return nil, ErrResponseLimit
	}
	return contents, nil
}

func decodeJSONDocument(contents []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrResponseJSON, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("%w: multiple JSON values", ErrResponseJSON)
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %v", ErrResponseJSON, err)
	}
	return value, nil
}

func extractPath(value any, path string) (any, error) {
	if path == "" {
		return value, nil
	}
	current := value
	for _, segment := range strings.Split(path, ".") {
		switch typed := current.(type) {
		case map[string]any:
			next, exists := typed[segment]
			if !exists {
				return nil, fmt.Errorf("%w: key %q is missing in %q", ErrResponsePath, segment, path)
			}
			current = next
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, fmt.Errorf("%w: array index %q is invalid in %q", ErrResponsePath, segment, path)
			}
			current = typed[index]
		default:
			return nil, fmt.Errorf("%w: cannot traverse %q in %q", ErrResponsePath, segment, path)
		}
	}
	return current, nil
}

func boundedExcerpt(contents []byte) string {
	if len(contents) > maxErrorExcerptBytes {
		contents = contents[:maxErrorExcerptBytes]
	}
	return string(contents)
}
