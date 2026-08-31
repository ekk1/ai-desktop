package backend

import (
	"strings"
	"testing"
)

func TestProfileValidateAcceptsRunnableDefaults(t *testing.T) {
	profile := DefaultProfile()
	profile.Name = "llama"
	profile.Command = "llama-server --port 8080"
	if err := profile.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if profile.Execution.Kind != ExecutionLocal {
		t.Fatalf("default execution kind = %q", profile.Execution.Kind)
	}
}

func TestProfileValidateRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Profile)
	}{
		{name: "empty name", change: func(p *Profile) { p.Name = " " }},
		{name: "empty command", change: func(p *Profile) { p.Command = "" }},
		{name: "short grace", change: func(p *Profile) { p.StopGraceSeconds = 0 }},
		{name: "long grace", change: func(p *Profile) { p.StopGraceSeconds = 301 }},
		{name: "small log", change: func(p *Profile) { p.LogBufferBytes = (64 << 10) - 1 }},
		{name: "large log", change: func(p *Profile) { p.LogBufferBytes = (64 << 20) + 1 }},
		{name: "readiness kind", change: func(p *Profile) { p.Readiness.Kind = "socket" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := DefaultProfile()
			profile.Name = "server"
			profile.Command = "run"
			test.change(&profile)
			if err := profile.Validate(); err == nil {
				t.Fatal("Validate accepted invalid profile")
			}
		})
	}
}

func TestProfileValidateExecutionLocation(t *testing.T) {
	valid := DefaultProfile()
	valid.Name = "remote server"
	valid.Command = "server --port 8080"
	valid.Execution = Execution{Kind: ExecutionWorker, WorkerBaseURL: "http://127.0.0.1:8288"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid worker profile: %v", err)
	}

	tests := []struct {
		name      string
		execution Execution
	}{
		{name: "unknown kind", execution: Execution{Kind: "cluster"}},
		{name: "local with URL", execution: Execution{Kind: ExecutionLocal, WorkerBaseURL: "http://127.0.0.1:8288"}},
		{name: "worker without URL", execution: Execution{Kind: ExecutionWorker}},
		{name: "relative worker URL", execution: Execution{Kind: ExecutionWorker, WorkerBaseURL: "127.0.0.1:8288"}},
		{name: "worker URL with credentials", execution: Execution{Kind: ExecutionWorker, WorkerBaseURL: "http://user:pass@127.0.0.1:8288"}},
		{name: "worker URL with path", execution: Execution{Kind: ExecutionWorker, WorkerBaseURL: "http://127.0.0.1:8288/api"}},
		{name: "worker URL with query", execution: Execution{Kind: ExecutionWorker, WorkerBaseURL: "http://127.0.0.1:8288?x=1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := valid
			profile.Execution = test.execution
			if err := profile.Validate(); err == nil || !strings.Contains(err.Error(), "execution") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestProfileValidateRejectsWorkerReadinessRejectedByWorker(t *testing.T) {
	profile := DefaultProfile()
	profile.Name = "remote server"
	profile.Command = "server --port 8080"
	profile.Execution = Execution{Kind: ExecutionWorker, WorkerBaseURL: "http://127.0.0.1:8288"}
	profile.Readiness = Readiness{Kind: ReadinessHTTP, URL: "https://127.0.0.1:8080/health", TimeoutSeconds: 60}
	if err := profile.Validate(); err == nil || !strings.Contains(err.Error(), "readiness") {
		t.Fatalf("Validate() error = %v, want worker readiness validation", err)
	}
}
