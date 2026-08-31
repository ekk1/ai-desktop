package videogen

import (
	"strings"
	"testing"
)

// This fails if ordinary values are interpolated without shell quoting, or a
// caller cannot deliberately opt in to one trusted raw command fragment.
func TestExpandCLITemplateQuotesOrdinaryVariablesAndAllowsExplicitRaw(t *testing.T) {
	got, err := ExpandCLITemplate(`sd-cli -p {{PROMPT}} {{EXTRA_ARGS_RAW}} -o {{OUTPUT_PATH}}`, TemplateVariables{Values: map[string]string{"PROMPT": "a' b", "OUTPUT_PATH": "/tmp/out.webm"}, Raw: map[string]string{"EXTRA_ARGS_RAW": "--seed 7"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `sd-cli -p 'a'"'"' b' --seed 7 -o '/tmp/out.webm'`; got != want {
		t.Fatalf("expanded template = %q, want %q", got, want)
	}
}

// This fails if malformed, nested, or unrecognized template tokens are
// silently retained or expanded as command text.
func TestExpandCLITemplateRejectsMalformedNestedAndUnknownTokens(t *testing.T) {
	for _, template := range []string{"before {{PROMPT", "before PROMPT}}", "{{{{PROMPT}}}}", "{{NOT_ALLOWED}}", "{{prompt}}"} {
		if _, err := ExpandCLITemplate(template, TemplateVariables{Values: map[string]string{"PROMPT": "ok"}}); err == nil {
			t.Fatalf("ExpandCLITemplate accepted %q", template)
		}
	}
}

// This fails if omission or contradictory raw/non-raw sources can make a
// template variable resolve unexpectedly.
func TestExpandCLITemplateRejectsMissingAndConflictingTokens(t *testing.T) {
	for _, variables := range []TemplateVariables{
		{Values: map[string]string{}},
		{Values: map[string]string{"EXTRA_ARGS_RAW": "wrong"}, Raw: map[string]string{"EXTRA_ARGS_RAW": "--seed 7"}},
		{Values: map[string]string{"PROMPT": "p"}, Raw: map[string]string{"PROMPT": "wrong"}},
	} {
		if _, err := ExpandCLITemplate("{{EXTRA_ARGS_RAW}}", variables); err == nil {
			t.Fatalf("ExpandCLITemplate accepted conflicting or missing variables %#v", variables)
		}
	}
	if _, err := ExpandCLITemplate("{{PROMPT}}", TemplateVariables{Values: map[string]string{}, Raw: map[string]string{"PROMPT": "wrong"}}); err == nil {
		t.Fatal("ExpandCLITemplate accepted a missing ordinary variable")
	}
}

// This fails if output limits are applied before substitution or not applied
// at all, permitting a command beyond the 256 KiB execution boundary.
func TestExpandCLITemplateRejectsOversizeResult(t *testing.T) {
	_, err := ExpandCLITemplate("{{PROMPT}}", TemplateVariables{Values: map[string]string{"PROMPT": strings.Repeat("x", 1<<18)}})
	if err == nil {
		t.Fatal("ExpandCLITemplate accepted an oversize result")
	}
}
