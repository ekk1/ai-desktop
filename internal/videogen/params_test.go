package videogen

import (
	"encoding/json"
	"math"
	"testing"
)

func TestMergeParamsRecursesAndNullDeletesInheritedKeys(t *testing.T) {
	got, err := MergeParams(
		json.RawMessage(`{"sample_params":{"sample_steps":20,"eta":1},"lora":[1]}`),
		json.RawMessage(`{"sample_params":{"eta":0.5}}`),
		json.RawMessage(`{"sample_params":{"eta":null},"lora":[2]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	sample := got["sample_params"].(map[string]any)
	if _, exists := sample["eta"]; exists || sample["sample_steps"].(json.Number).String() != "20" {
		t.Fatalf("merged params = %#v", got)
	}
	if got["lora"].([]any)[0].(json.Number).String() != "2" {
		t.Fatalf("array replacement = %#v", got["lora"])
	}
}

func TestMergeParamsRejectsManagedKeysAtEveryLayer(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"prompt":"managed"}`),
		json.RawMessage(`{"fps":24}`),
		json.RawMessage(`{"control_frames":[]}`),
	} {
		if _, err := MergeParams(json.RawMessage(`{}`), raw, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("MergeParams accepted managed params %s", raw)
		}
	}
}

func TestMergeParamsRequiresExactlyOneJSONObject(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`[]`), json.RawMessage(`null`), json.RawMessage(`{"ok":true} {}`),
	} {
		if _, err := MergeParams(raw, json.RawMessage(`{}`), json.RawMessage(`{}`)); err == nil {
			t.Fatalf("MergeParams accepted %s", raw)
		}
	}
}

func TestResolveTimingUsesCeilingAndRecordsAlgorithm(t *testing.T) {
	got, err := ResolveTiming(TimingInput{Mode: "duration", DurationSeconds: 2.01, FPS: 16}, nil)
	if err != nil || got.RequestedFrames != 33 || got.AlgorithmVersion != "duration-ceil-v1" {
		t.Fatalf("ResolveTiming = %#v, %v", got, err)
	}
}

func TestResolveTimingUsesItemOverrideAndFrameInputVerbatim(t *testing.T) {
	got, err := ResolveTiming(
		TimingInput{Mode: "duration", DurationSeconds: 2, FPS: 16},
		&TimingInput{Mode: "frames", VideoFrames: 37, FPS: 23},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.InputMode != "frames" || got.FPS != 23 || got.RequestedFrames != 37 || got.AlgorithmVersion != "frames-v1" {
		t.Fatalf("ResolveTiming = %#v", got)
	}
}

func TestResolveTimingRejectsInvalidBoundariesAndAmbiguousInput(t *testing.T) {
	cases := []TimingInput{
		{Mode: "duration", DurationSeconds: 0.0009, FPS: 16},
		{Mode: "duration", DurationSeconds: 86400.1, FPS: 16},
		{Mode: "duration", DurationSeconds: 1, FPS: 241},
		{Mode: "frames", VideoFrames: 0, FPS: 16},
		{Mode: "frames", VideoFrames: 100001, FPS: 16},
		{Mode: "frames", VideoFrames: 10, DurationSeconds: 1, FPS: 16},
	}
	for _, input := range cases {
		if _, err := ResolveTiming(input, nil); err == nil {
			t.Fatalf("ResolveTiming accepted %#v", input)
		}
	}
}

func TestResolveTimingRejectsNonFiniteDuration(t *testing.T) {
	for _, duration := range []float64{math.NaN(), math.Inf(1)} {
		if _, err := ResolveTiming(TimingInput{Mode: "duration", DurationSeconds: duration, FPS: 16}, nil); err == nil {
			t.Fatalf("ResolveTiming accepted %v", duration)
		}
	}
}
