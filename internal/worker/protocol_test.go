package worker

import (
	"strings"
	"testing"
)

func validStartRequest() StartRequest {
	return StartRequest{
		Command:          "./llama-server --port 8080",
		WorkDir:          "/srv/models",
		Env:              map[string]string{"CUDA_VISIBLE_DEVICES": "0"},
		StopGraceSeconds: 10,
		LogBufferBytes:   1 << 20,
		Readiness: Readiness{
			Kind:           ReadinessHTTP,
			URL:            "http://127.0.0.1:8080/health",
			TimeoutSeconds: 60,
		},
	}
}

func TestStartRequestValidateAcceptsValidRequest(t *testing.T) {
	t.Parallel()
	request := validStartRequest()
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestStartRequestValidateRejectsInvalidFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*StartRequest)
		want   string
	}{
		{name: "empty command", mutate: func(request *StartRequest) { request.Command = " \n" }, want: "command"},
		{name: "command too long", mutate: func(request *StartRequest) { request.Command = strings.Repeat("x", MaxCommandBytes+1) }, want: "command"},
		{name: "relative work directory", mutate: func(request *StartRequest) { request.WorkDir = "models" }, want: "work_dir"},
		{name: "work directory too long", mutate: func(request *StartRequest) { request.WorkDir = "/" + strings.Repeat("x", MaxWorkDirBytes) }, want: "work_dir"},
		{name: "invalid environment key", mutate: func(request *StartRequest) { request.Env = map[string]string{"BAD=KEY": "x"} }, want: "environment"},
		{name: "environment value too long", mutate: func(request *StartRequest) {
			request.Env = map[string]string{"KEY": strings.Repeat("x", MaxEnvValueBytes+1)}
		}, want: "environment"},
		{name: "short stop grace", mutate: func(request *StartRequest) { request.StopGraceSeconds = 0 }, want: "stop_grace_seconds"},
		{name: "long stop grace", mutate: func(request *StartRequest) { request.StopGraceSeconds = 301 }, want: "stop_grace_seconds"},
		{name: "small log buffer", mutate: func(request *StartRequest) { request.LogBufferBytes = MinLogBufferBytes - 1 }, want: "log_buffer_bytes"},
		{name: "large log buffer", mutate: func(request *StartRequest) { request.LogBufferBytes = MaxLogBufferBytes + 1 }, want: "log_buffer_bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validStartRequest()
			test.mutate(&request)
			err := request.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestReadinessValidateRestrictsHTTPToLoopback(t *testing.T) {
	t.Parallel()
	accepted := []string{
		"http://localhost:8080/health",
		"http://127.0.0.1:8080/health",
		"http://127.12.34.56:8080/health",
		"http://[::1]:8080/health",
	}
	for _, address := range accepted {
		readiness := Readiness{Kind: ReadinessHTTP, URL: address, TimeoutSeconds: 60}
		if err := readiness.Validate(); err != nil {
			t.Errorf("Validate(%q) error = %v", address, err)
		}
	}

	rejected := []string{
		"https://127.0.0.1:8080/health",
		"http://10.0.0.8:8080/health",
		"http://example.com/health",
		"http://user:pass@127.0.0.1:8080/health",
		"http://127.0.0.1:8080/health#fragment",
	}
	for _, address := range rejected {
		readiness := Readiness{Kind: ReadinessHTTP, URL: address, TimeoutSeconds: 60}
		if err := readiness.Validate(); err == nil {
			t.Errorf("Validate(%q) error = nil", address)
		}
	}
}

func TestReadinessValidateKinds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		readiness Readiness
		wantError bool
	}{
		{name: "none", readiness: Readiness{Kind: ReadinessNone, TimeoutSeconds: 60}},
		{name: "delay", readiness: Readiness{Kind: ReadinessDelay, DelaySeconds: 2, TimeoutSeconds: 60}},
		{name: "delay exceeds timeout", readiness: Readiness{Kind: ReadinessDelay, DelaySeconds: 61, TimeoutSeconds: 60}, wantError: true},
		{name: "log regex", readiness: Readiness{Kind: ReadinessLogRegex, Pattern: `ready\s+now`, TimeoutSeconds: 60}},
		{name: "invalid log regex", readiness: Readiness{Kind: ReadinessLogRegex, Pattern: `[`, TimeoutSeconds: 60}, wantError: true},
		{name: "unknown kind", readiness: Readiness{Kind: "socket", TimeoutSeconds: 60}, wantError: true},
		{name: "short timeout", readiness: Readiness{Kind: ReadinessNone, TimeoutSeconds: 0}, wantError: true},
		{name: "long timeout", readiness: Readiness{Kind: ReadinessNone, TimeoutSeconds: 3601}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.readiness.Validate()
			if test.wantError && err == nil {
				t.Fatal("Validate() error = nil")
			}
			if !test.wantError && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestStartRequestValidateRejectsTooManyEnvironmentVariables(t *testing.T) {
	t.Parallel()
	request := validStartRequest()
	request.Env = make(map[string]string, MaxEnvEntries+1)
	for index := 0; index <= MaxEnvEntries; index++ {
		request.Env["KEY_"+strings.Repeat("X", index)] = "value"
	}
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "environment") {
		t.Fatalf("Validate() error = %v", err)
	}
}
