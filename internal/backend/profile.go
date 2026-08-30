package backend

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	ReadinessNone     = "none"
	ReadinessDelay    = "delay"
	ReadinessHTTP     = "http"
	ReadinessLogRegex = "log_regex"
)

type Readiness struct {
	Kind           string `json:"kind"`
	DelaySeconds   int    `json:"delay_seconds,omitempty"`
	URL            string `json:"url,omitempty"`
	Pattern        string `json:"pattern,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type Profile struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Description      string            `json:"description,omitempty"`
	Tags             []string          `json:"tags,omitempty"`
	Command          string            `json:"command"`
	WorkDir          string            `json:"work_dir,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Variables        map[string]string `json:"variables,omitempty"`
	Readiness        Readiness         `json:"readiness"`
	StopGraceSeconds int               `json:"stop_grace_seconds"`
	LogBufferBytes   int               `json:"log_buffer_bytes"`
}

func DefaultProfile() Profile {
	return Profile{
		Env:       make(map[string]string),
		Variables: make(map[string]string),
		Readiness: Readiness{
			Kind:           ReadinessNone,
			TimeoutSeconds: 60,
		},
		StopGraceSeconds: 10,
		LogBufferBytes:   1 << 20,
	}
}

func (profile Profile) Validate() error {
	if strings.TrimSpace(profile.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(profile.Command) == "" {
		return fmt.Errorf("command is required")
	}
	if profile.StopGraceSeconds < 1 || profile.StopGraceSeconds > 300 {
		return fmt.Errorf("stop_grace_seconds must be between 1 and 300")
	}
	if profile.LogBufferBytes < 64<<10 || profile.LogBufferBytes > 64<<20 {
		return fmt.Errorf("log_buffer_bytes must be between 65536 and 67108864")
	}
	for key := range profile.Env {
		if strings.TrimSpace(key) == "" || strings.ContainsRune(key, '=') {
			return fmt.Errorf("invalid environment variable name %q", key)
		}
	}
	for key := range profile.Variables {
		if !variableNamePattern.MatchString(key) {
			return fmt.Errorf("invalid template variable name %q", key)
		}
	}
	return profile.Readiness.validate()
}

func (readiness Readiness) validate() error {
	if readiness.TimeoutSeconds < 1 || readiness.TimeoutSeconds > 3600 {
		return fmt.Errorf("readiness timeout_seconds must be between 1 and 3600")
	}
	switch readiness.Kind {
	case ReadinessNone:
		return nil
	case ReadinessDelay:
		if readiness.DelaySeconds < 1 || readiness.DelaySeconds > readiness.TimeoutSeconds {
			return fmt.Errorf("readiness delay_seconds must be positive and not exceed timeout")
		}
		return nil
	case ReadinessHTTP:
		parsed, err := url.ParseRequestURI(readiness.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("readiness url must be an absolute HTTP URL")
		}
		return nil
	case ReadinessLogRegex:
		if strings.TrimSpace(readiness.Pattern) == "" {
			return fmt.Errorf("readiness pattern is required")
		}
		if _, err := regexp.Compile(readiness.Pattern); err != nil {
			return fmt.Errorf("compile readiness pattern: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported readiness kind %q", readiness.Kind)
	}
}

func cloneProfile(profile Profile) Profile {
	clone := profile
	clone.Tags = append([]string(nil), profile.Tags...)
	clone.Env = cloneStringMap(profile.Env)
	clone.Variables = cloneStringMap(profile.Variables)
	return clone
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
