package sdcpp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var ErrManagedImageField = errors.New("image request field is managed by the workbench")

var managedImageFields = map[string]struct{}{
	"prompt": {}, "negative_prompt": {}, "init_image": {}, "ref_images": {},
	"mask_image": {}, "control_image": {}, "ip_adapter_image": {},
}

type ImageFields struct {
	InitImage      string
	RefImages      []string
	MaskImage      string
	ControlImage   string
	IPAdapterImage string
}

func MergeImageParams(base, override json.RawMessage) (map[string]any, error) {
	baseObject, err := decodeImageObject(base, "base image params")
	if err != nil {
		return nil, err
	}
	overrideObject, err := decodeImageObject(override, "image params override")
	if err != nil {
		return nil, err
	}
	if err := rejectManagedImageFields(baseObject); err != nil {
		return nil, fmt.Errorf("base image params: %w", err)
	}
	if err := rejectManagedImageFields(overrideObject); err != nil {
		return nil, fmt.Errorf("image params override: %w", err)
	}
	result := cloneImageObject(baseObject)
	mergeImageObjects(result, overrideObject)
	return result, nil
}

func RenderImageRequest(params map[string]any, prompt, negativePrompt string, images ImageFields) ([]byte, error) {
	if params == nil {
		params = map[string]any{}
	}
	if err := rejectManagedImageFields(params); err != nil {
		return nil, err
	}
	request := cloneImageObject(params)
	request["prompt"] = prompt
	request["negative_prompt"] = negativePrompt
	if images.InitImage != "" {
		request["init_image"] = images.InitImage
	}
	if len(images.RefImages) > 0 {
		request["ref_images"] = append([]string(nil), images.RefImages...)
	}
	if images.MaskImage != "" {
		request["mask_image"] = images.MaskImage
	}
	if images.ControlImage != "" {
		request["control_image"] = images.ControlImage
	}
	if images.IPAdapterImage != "" {
		request["ip_adapter_image"] = images.IPAdapterImage
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode image request: %w", err)
	}
	return body, nil
}

func decodeImageObject(raw json.RawMessage, label string) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%s must be one JSON Object: %w", label, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%s must be one JSON Object: %w", label, err)
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, fmt.Errorf("%s must be one non-null JSON Object", label)
	}
	return object, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return err
}

func rejectManagedImageFields(object map[string]any) error {
	for key := range object {
		if _, managed := managedImageFields[key]; managed {
			return fmt.Errorf("%w: %s", ErrManagedImageField, key)
		}
	}
	return nil
}

func mergeImageObjects(destination, source map[string]any) {
	for key, sourceValue := range source {
		sourceObject, sourceIsObject := sourceValue.(map[string]any)
		destinationObject, destinationIsObject := destination[key].(map[string]any)
		if sourceIsObject && destinationIsObject {
			mergeImageObjects(destinationObject, sourceObject)
			continue
		}
		destination[key] = cloneImageValue(sourceValue)
	}
}

func cloneImageObject(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = cloneImageValue(value)
	}
	return clone
}

func cloneImageValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneImageObject(typed)
	case []any:
		clone := make([]any, len(typed))
		for index, item := range typed {
			clone[index] = cloneImageValue(item)
		}
		return clone
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}
