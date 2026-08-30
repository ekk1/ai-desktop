package sdcpp

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestMergeImageParamsRecursesObjectsAndReplacesArrays(t *testing.T) {
	got, err := MergeImageParams(
		json.RawMessage(`{"width":1024,"sample_params":{"sample_steps":20,"guidance":{"txt_cfg":5}},"lora":[1]}`),
		json.RawMessage(`{"sample_params":{"guidance":{"txt_cfg":7}},"lora":[2],"nullable":null}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got["width"].(json.Number).String() != "1024" {
		t.Fatalf("merged = %#v", got)
	}
	sample := got["sample_params"].(map[string]any)
	if sample["sample_steps"].(json.Number).String() != "20" || sample["guidance"].(map[string]any)["txt_cfg"].(json.Number).String() != "7" {
		t.Fatalf("merged = %#v", got)
	}
	if got["lora"].([]any)[0].(json.Number).String() != "2" || got["nullable"] != nil {
		t.Fatalf("merged = %#v", got)
	}

	got["sample_params"].(map[string]any)["sample_steps"] = json.Number("99")
	again, err := MergeImageParams(json.RawMessage(`{"sample_params":{"sample_steps":20}}`), json.RawMessage(`{}`))
	if err != nil || again["sample_params"].(map[string]any)["sample_steps"].(json.Number).String() != "20" {
		t.Fatalf("merge retained aliases: %#v, %v", again, err)
	}
}

func TestMergeImageParamsRejectsManagedKeys(t *testing.T) {
	for _, key := range []string{"prompt", "negative_prompt", "init_image", "ref_images", "mask_image", "control_image", "ip_adapter_image"} {
		t.Run(key, func(t *testing.T) {
			_, err := MergeImageParams(json.RawMessage(`{"`+key+`":"bypass"}`), json.RawMessage(`{}`))
			if !errors.Is(err, ErrManagedImageField) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestMergeImageParamsRequiresExactlyOneObject(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"empty": nil, "null": json.RawMessage(`null`), "array": json.RawMessage(`[]`),
		"scalar": json.RawMessage(`1`), "trailing": json.RawMessage(`{} {}`), "invalid": json.RawMessage(`{`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := MergeImageParams(raw, json.RawMessage(`{}`)); err == nil {
				t.Fatalf("accepted %q", raw)
			}
		})
	}
}

func TestRenderImageRequestInjectsManagedFieldsWithoutMutatingParams(t *testing.T) {
	params, err := MergeImageParams(json.RawMessage(`{"width":1024}`), json.RawMessage(`{"seed":42}`))
	if err != nil {
		t.Fatal(err)
	}
	before := cloneTestMap(params)
	body, err := RenderImageRequest(params, "cat", "blur", ImageFields{
		InitImage: "data:image/png;base64,a", RefImages: []string{"data:image/png;base64,b"},
		MaskImage: "data:image/png;base64,c", ControlImage: "data:image/png;base64,d", IPAdapterImage: "data:image/png;base64,e",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["prompt"] != "cat" || got["negative_prompt"] != "blur" || got["init_image"] != "data:image/png;base64,a" {
		t.Fatalf("body = %s", body)
	}
	if got["ref_images"].([]any)[0] != "data:image/png;base64,b" || got["ip_adapter_image"] != "data:image/png;base64,e" {
		t.Fatalf("body = %s", body)
	}
	if !reflect.DeepEqual(before, params) {
		t.Fatalf("params mutated: before=%#v after=%#v", before, params)
	}
}

func TestRenderImageRequestOmitsUnusedImageFields(t *testing.T) {
	body, err := RenderImageRequest(map[string]any{}, "", "", ImageFields{})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if _, exists := got["init_image"]; exists {
		t.Fatalf("body = %s", body)
	}
	if _, exists := got["ref_images"]; exists {
		t.Fatalf("body = %s", body)
	}
	if got["prompt"] != "" || got["negative_prompt"] != "" {
		t.Fatalf("body = %s", body)
	}
}

func cloneTestMap(source map[string]any) map[string]any {
	encoded, _ := json.Marshal(source)
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var result map[string]any
	_ = decoder.Decode(&result)
	return result
}
