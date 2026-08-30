package backend

import "testing"

func TestProfileValidateAcceptsRunnableDefaults(t *testing.T) {
	profile := DefaultProfile()
	profile.Name = "llama"
	profile.Command = "llama-server --port 8080"
	if err := profile.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
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
