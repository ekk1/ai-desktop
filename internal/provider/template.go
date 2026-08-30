package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var (
	ErrPlaceholderPosition = errors.New("JSON placeholder must be a complete value")
	ErrInvalidHeader       = errors.New("invalid provider header")
)

type TemplateVariables struct {
	Content       string
	Panels        any
	Knowledge     any
	AssetDataURLs []string
}

type PreparedRequest struct {
	URL                 string
	Method              string
	Headers             map[string]string
	SnapshotHeaders     map[string]string
	Body                []byte
	ResponseMode        string
	ResponseContentPath string
	StreamContentPath   string
	StreamDonePath      string
	ConnectTimeout      time.Duration
	TotalTimeout        time.Duration
	MaxResponseBytes    int64
}

func Render(provider Provider, quickPath QuickPath, variables TemplateVariables) (PreparedRequest, error) {
	headers, snapshotHeaders, err := renderHeaders(provider)
	if err != nil {
		return PreparedRequest{}, err
	}
	if err := provider.validate(); err != nil {
		return PreparedRequest{}, err
	}
	parameters, err := decodeObject(quickPath.Params)
	if err != nil {
		return PreparedRequest{}, fmt.Errorf("quick path params: %w", err)
	}
	values := map[string]any{
		"${CONTENT_JSON}":         variables.Content,
		"${PANELS_JSON}":          variables.Panels,
		"${KNOWLEDGE_JSON}":       variables.Knowledge,
		"${ASSET_DATA_URLS_JSON}": variables.AssetDataURLs,
		"${MODEL_JSON}":           quickPath.Model,
		"${PARAMS_JSON}":          parameters,
	}
	replaced, err := replaceJSONPlaceholders(provider.BodyTemplate, values)
	if err != nil {
		return PreparedRequest{}, err
	}
	bodyObject, err := decodeObject(replaced)
	if err != nil {
		return PreparedRequest{}, fmt.Errorf("render provider body: %w", err)
	}
	for key, value := range parameters {
		bodyObject[key] = value
	}
	body, err := json.Marshal(bodyObject)
	if err != nil {
		return PreparedRequest{}, fmt.Errorf("encode provider body: %w", err)
	}
	return PreparedRequest{
		URL: provider.URL, Method: provider.Method, Headers: headers, SnapshotHeaders: snapshotHeaders, Body: body,
		ResponseMode: provider.ResponseMode, ResponseContentPath: provider.ResponseContentPath,
		StreamContentPath: provider.StreamContentPath, StreamDonePath: provider.StreamDonePath,
		ConnectTimeout:   time.Duration(provider.ConnectTimeoutSeconds) * time.Second,
		TotalTimeout:     time.Duration(provider.TotalTimeoutSeconds) * time.Second,
		MaxResponseBytes: provider.MaxResponseBytes,
	}, nil
}

func renderHeaders(provider Provider) (map[string]string, map[string]string, error) {
	headers := make(map[string]string, len(provider.Headers))
	snapshotHeaders := make(map[string]string, len(provider.Headers))
	for name, template := range provider.Headers {
		if !validHeaderName(name) {
			return nil, nil, fmt.Errorf("%w: invalid name %q", ErrInvalidHeader, name)
		}
		remaining := strings.ReplaceAll(template, "${API_KEY}", "")
		if strings.Contains(remaining, "${") {
			return nil, nil, fmt.Errorf("%w: header %q contains an unknown placeholder", ErrInvalidHeader, name)
		}
		value := strings.ReplaceAll(template, "${API_KEY}", provider.APIKey)
		if strings.ContainsAny(value, "\r\n") {
			return nil, nil, fmt.Errorf("%w: header %q contains a newline", ErrInvalidHeader, name)
		}
		headers[name] = value
		if strings.Contains(template, "${API_KEY}") || sensitiveHeader(name) {
			snapshotHeaders[name] = "<redacted>"
		} else {
			snapshotHeaders[name] = value
		}
	}
	return headers, snapshotHeaders, nil
}

func replaceJSONPlaceholders(template string, values map[string]any) ([]byte, error) {
	var result bytes.Buffer
	inString := false
	escaped := false
	for index := 0; index < len(template); {
		character := template[index]
		if inString {
			if character == '$' && strings.HasPrefix(template[index:], "${") {
				return nil, ErrPlaceholderPosition
			}
			result.WriteByte(character)
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			index++
			continue
		}
		if character == '"' {
			inString = true
			result.WriteByte(character)
			index++
			continue
		}
		if character == '$' && strings.HasPrefix(template[index:], "${") {
			matched := false
			for placeholder, value := range values {
				if !strings.HasPrefix(template[index:], placeholder) {
					continue
				}
				encoded, err := json.Marshal(value)
				if err != nil {
					return nil, fmt.Errorf("encode %s: %w", placeholder, err)
				}
				result.Write(encoded)
				index += len(placeholder)
				matched = true
				break
			}
			if !matched {
				return nil, fmt.Errorf("unknown JSON placeholder")
			}
			continue
		}
		result.WriteByte(character)
		index++
	}
	return result.Bytes(), nil
}

func decodeObject(contents []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(contents)) == 0 {
		contents = []byte(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, fmt.Errorf("top-level value is not a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return object, nil
}

func sensitiveHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "x-api-key", "api-key":
		return true
	default:
		return false
	}
}
