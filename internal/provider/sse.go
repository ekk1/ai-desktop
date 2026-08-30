package provider

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

const maxSSELineBytes = 1 << 20

var errStopSSE = errors.New("stop SSE stream")

func readSSE(reader io.Reader, maxBytes int64) ([]string, error) {
	events := make([]string, 0)
	err := scanSSE(reader, maxBytes, func(event string) error {
		events = append(events, event)
		return nil
	})
	return events, err
}

func scanSSE(reader io.Reader, maxBytes int64, onEvent func(string) error) error {
	if maxBytes < 1 {
		return ErrResponseLimit
	}
	lineLimit := int64(maxSSELineBytes)
	if maxBytes < lineLimit {
		lineLimit = maxBytes
	}
	initialBufferSize := int64(64 << 10)
	if lineLimit+1 < initialBufferSize {
		initialBufferSize = lineLimit + 1
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, int(initialBufferSize)), int(lineLimit+1))
	dataLines := make([]string, 0)
	var consumed int64
	dispatch := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		event := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		return onEvent(event)
	}
	for scanner.Scan() {
		line := scanner.Text()
		consumed += int64(len(line)) + 1
		if consumed > maxBytes {
			return ErrResponseLimit
		}
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, hasColon := strings.Cut(line, ":")
		if !hasColon {
			value = ""
		}
		if strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		if field == "data" {
			dataLines = append(dataLines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%w: read SSE event line: %v", ErrResponseLimit, err)
	}
	return dispatch()
}

func executeSSEResponse(
	body io.Reader,
	result ExecutionResult,
	prepared PreparedRequest,
	onChunk func(string),
) (ExecutionResult, error) {
	var content strings.Builder
	var excerpt strings.Builder
	err := scanSSE(body, prepared.MaxResponseBytes, func(event string) error {
		if excerpt.Len() < maxErrorExcerptBytes {
			remaining := maxErrorExcerptBytes - excerpt.Len()
			excerptEvent := event
			if len(excerptEvent) > remaining {
				excerptEvent = excerptEvent[:remaining]
			}
			excerpt.WriteString(excerptEvent)
		}
		if strings.TrimSpace(event) == "[DONE]" {
			return errStopSSE
		}
		value, err := decodeJSONDocument([]byte(event))
		if err != nil {
			return err
		}
		extracted, err := extractPath(value, prepared.StreamContentPath)
		if err != nil {
			return err
		}
		chunk, ok := extracted.(string)
		if !ok {
			return fmt.Errorf("%w: stream path %q is not a string", ErrResponsePath, prepared.StreamContentPath)
		}
		content.WriteString(chunk)
		if onChunk != nil && chunk != "" {
			onChunk(chunk)
		}
		if prepared.StreamDonePath != "" {
			doneValue, err := extractPath(value, prepared.StreamDonePath)
			if err != nil {
				return err
			}
			done, ok := doneValue.(bool)
			if !ok {
				return fmt.Errorf("%w: stream done path %q is not a boolean", ErrResponsePath, prepared.StreamDonePath)
			}
			if done {
				return errStopSSE
			}
		}
		return nil
	})
	result.Content = content.String()
	result.ResponseExcerpt = excerpt.String()
	if errors.Is(err, errStopSSE) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	return result, nil
}
