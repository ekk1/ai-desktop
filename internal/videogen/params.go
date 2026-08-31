package videogen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

var managedHTTPParams = map[string]struct{}{
	"prompt": {}, "negative_prompt": {}, "init_image": {}, "end_image": {},
	"control_frames": {}, "fps": {}, "video_frames": {}, "batch_count": {},
}

// MergeParams combines provider defaults, batch parameters, and item overrides.
// Object values are merged recursively; null removes an inherited key.
func MergeParams(defaults, common, override json.RawMessage) (map[string]any, error) {
	layers := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "default params", raw: defaults},
		{name: "common params", raw: common},
		{name: "params override", raw: override},
	}
	result := make(map[string]any)
	for _, layer := range layers {
		object, err := decodeParamsObject(layer.raw, layer.name)
		if err != nil {
			return nil, err
		}
		if err := rejectManagedHTTPParams(object); err != nil {
			return nil, fmt.Errorf("%s: %w", layer.name, err)
		}
		mergeParamsObject(result, object)
	}
	return result, nil
}

// ResolveTiming selects the item timing when supplied, otherwise the batch
// timing, and records the conversion performed for the provider request.
func ResolveTiming(batch TimingInput, item *TimingInput) (ResolvedTiming, error) {
	input := batch
	if item != nil {
		input = *item
	}
	input.Mode = strings.TrimSpace(input.Mode)
	if input.FPS < 1 || input.FPS > 240 {
		return ResolvedTiming{}, fmt.Errorf("FPS must be between 1 and 240")
	}
	switch input.Mode {
	case "duration":
		if math.IsNaN(input.DurationSeconds) || math.IsInf(input.DurationSeconds, 0) || input.DurationSeconds < 0.001 || input.DurationSeconds > 86400 || input.VideoFrames != 0 {
			return ResolvedTiming{}, fmt.Errorf("duration timing requires duration between 0.001 and 86400 seconds only")
		}
		return ResolvedTiming{
			InputMode: "duration", DurationSeconds: input.DurationSeconds, FPS: input.FPS,
			RequestedFrames: int(math.Ceil(input.DurationSeconds * float64(input.FPS))), AlgorithmVersion: "duration-ceil-v1",
		}, nil
	case "frames":
		if input.VideoFrames < 1 || input.VideoFrames > 100000 || input.DurationSeconds != 0 {
			return ResolvedTiming{}, fmt.Errorf("frame timing requires 1 to 100000 frames only")
		}
		return ResolvedTiming{InputMode: "frames", FPS: input.FPS, RequestedFrames: input.VideoFrames, AlgorithmVersion: "frames-v1"}, nil
	default:
		return ResolvedTiming{}, fmt.Errorf("timing mode is invalid")
	}
}

func decodeParamsObject(raw json.RawMessage, label string) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%s must be one JSON Object: %w", label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("%s must be one JSON Object: multiple JSON values", label)
		}
		return nil, fmt.Errorf("%s must be one JSON Object: %w", label, err)
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, fmt.Errorf("%s must be one non-null JSON Object", label)
	}
	return object, nil
}

func rejectManagedHTTPParams(object map[string]any) error {
	for key := range object {
		if _, managed := managedHTTPParams[key]; managed {
			return fmt.Errorf("%q is managed by the workbench", key)
		}
	}
	return nil
}

func mergeParamsObject(destination, source map[string]any) {
	for key, sourceValue := range source {
		if sourceValue == nil {
			delete(destination, key)
			continue
		}
		sourceObject, sourceIsObject := sourceValue.(map[string]any)
		destinationObject, destinationIsObject := destination[key].(map[string]any)
		if sourceIsObject && destinationIsObject {
			mergeParamsObject(destinationObject, sourceObject)
			continue
		}
		destination[key] = cloneParamsValue(sourceValue)
	}
}

func cloneParamsValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(typed))
		for key, nested := range typed {
			clone[key] = cloneParamsValue(nested)
		}
		return clone
	case []any:
		clone := make([]any, len(typed))
		for index, nested := range typed {
			clone[index] = cloneParamsValue(nested)
		}
		return clone
	default:
		return typed
	}
}
