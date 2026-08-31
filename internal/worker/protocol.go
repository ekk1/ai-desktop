package worker

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	MaxRequestBytes   = int64(1 << 20)
	MaxCommandBytes   = 256 << 10
	MaxWorkDirBytes   = 16 << 10
	MaxEnvEntries     = 256
	MaxEnvKeyBytes    = 256
	MaxEnvValueBytes  = 64 << 10
	MaxPatternBytes   = 16 << 10
	MinLogBufferBytes = 64 << 10
	MaxLogBufferBytes = 64 << 20
	MaxErrorBytes     = 4 << 10
)

const (
	ReadinessNone     = "none"
	ReadinessDelay    = "delay"
	ReadinessHTTP     = "http"
	ReadinessLogRegex = "log_regex"
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Readiness struct {
	Kind           string `json:"kind"`
	DelaySeconds   int    `json:"delay_seconds,omitempty"`
	URL            string `json:"url,omitempty"`
	Pattern        string `json:"pattern,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (readiness Readiness) Validate() error {
	if readiness.TimeoutSeconds < 1 || readiness.TimeoutSeconds > 3600 {
		return fmt.Errorf("readiness timeout_seconds must be between 1 and 3600")
	}
	switch readiness.Kind {
	case ReadinessNone:
		return nil
	case ReadinessDelay:
		if readiness.DelaySeconds < 1 || readiness.DelaySeconds > readiness.TimeoutSeconds {
			return fmt.Errorf("readiness delay_seconds must be positive and not exceed timeout_seconds")
		}
		return nil
	case ReadinessHTTP:
		return validateReadinessURL(readiness.URL)
	case ReadinessLogRegex:
		if strings.TrimSpace(readiness.Pattern) == "" {
			return fmt.Errorf("readiness pattern is required")
		}
		if len(readiness.Pattern) > MaxPatternBytes {
			return fmt.Errorf("readiness pattern exceeds %d bytes", MaxPatternBytes)
		}
		if _, err := regexp.Compile(readiness.Pattern); err != nil {
			return fmt.Errorf("compile readiness pattern: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported readiness kind %q", readiness.Kind)
	}
}

func validateReadinessURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || !parsed.IsAbs() || parsed.Scheme != "http" || parsed.Host == "" {
		return fmt.Errorf("readiness url must be an absolute loopback HTTP URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("readiness url must not contain user information or a fragment")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return nil
	}
	address := net.ParseIP(host)
	if address == nil || !address.IsLoopback() {
		return fmt.Errorf("readiness url host must be a loopback address")
	}
	return nil
}

type StartRequest struct {
	Command          string            `json:"command"`
	WorkDir          string            `json:"work_dir,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	StopGraceSeconds int               `json:"stop_grace_seconds"`
	LogBufferBytes   int               `json:"log_buffer_bytes"`
	Readiness        Readiness         `json:"readiness"`
}

func (request StartRequest) Validate() error {
	if strings.TrimSpace(request.Command) == "" {
		return fmt.Errorf("command is required")
	}
	if len(request.Command) > MaxCommandBytes {
		return fmt.Errorf("command exceeds %d bytes", MaxCommandBytes)
	}
	if len(request.WorkDir) > MaxWorkDirBytes {
		return fmt.Errorf("work_dir exceeds %d bytes", MaxWorkDirBytes)
	}
	if request.WorkDir != "" && !filepath.IsAbs(request.WorkDir) {
		return fmt.Errorf("work_dir must be an absolute path")
	}
	if len(request.Env) > MaxEnvEntries {
		return fmt.Errorf("environment exceeds %d entries", MaxEnvEntries)
	}
	for key, value := range request.Env {
		if len(key) > MaxEnvKeyBytes || !environmentNamePattern.MatchString(key) {
			return fmt.Errorf("invalid environment variable name %q", key)
		}
		if len(value) > MaxEnvValueBytes {
			return fmt.Errorf("environment value for %q exceeds %d bytes", key, MaxEnvValueBytes)
		}
	}
	if request.StopGraceSeconds < 1 || request.StopGraceSeconds > 300 {
		return fmt.Errorf("stop_grace_seconds must be between 1 and 300")
	}
	if request.LogBufferBytes < MinLogBufferBytes || request.LogBufferBytes > MaxLogBufferBytes {
		return fmt.Errorf("log_buffer_bytes must be between %d and %d", MinLogBufferBytes, MaxLogBufferBytes)
	}
	if err := request.Readiness.Validate(); err != nil {
		return err
	}
	return nil
}

type RunState string

const (
	StateStarting RunState = "starting"
	StateRunning  RunState = "running"
	StateStopping RunState = "stopping"
	StateStopped  RunState = "stopped"
	StateFailed   RunState = "failed"
)

type Run struct {
	RunID          string       `json:"run_id"`
	InstanceID     string       `json:"instance_id"`
	State          RunState     `json:"state"`
	PID            int          `json:"pid"`
	StartedAt      time.Time    `json:"started_at"`
	EndedAt        *time.Time   `json:"ended_at,omitempty"`
	ExitCode       *int         `json:"exit_code,omitempty"`
	Error          string       `json:"error,omitempty"`
	LogStartOffset int64        `json:"log_start_offset"`
	LogEndOffset   int64        `json:"log_end_offset"`
	Request        StartRequest `json:"request"`
}

type StatusResponse struct {
	Run *Run `json:"run"`
}

type HealthResponse struct {
	Status     string `json:"status"`
	Version    string `json:"version"`
	InstanceID string `json:"instance_id"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ErrorEnvelope struct {
	Error APIError `json:"error"`
}

func cloneStartRequest(request StartRequest) StartRequest {
	clone := request
	if request.Env != nil {
		clone.Env = make(map[string]string, len(request.Env))
		for key, value := range request.Env {
			clone.Env[key] = value
		}
	}
	return clone
}

func cloneRun(run Run) Run {
	clone := run
	clone.Request = cloneStartRequest(run.Request)
	if run.EndedAt != nil {
		endedAt := *run.EndedAt
		clone.EndedAt = &endedAt
	}
	if run.ExitCode != nil {
		exitCode := *run.ExitCode
		clone.ExitCode = &exitCode
	}
	return clone
}
