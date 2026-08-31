package videogen

import (
	"fmt"
	"strings"
)

const maximumExpandedCLITemplateBytes = 256 << 10

var ordinaryCLITemplateVariables = map[string]struct{}{
	"ATTEMPT_ID": {}, "WORKSPACE_DIR": {}, "INPUT_DIR": {}, "OUTPUT_DIR": {}, "OUTPUT_PATH": {},
	"PROMPT": {}, "NEGATIVE_PROMPT": {}, "SEED": {}, "FPS": {}, "VIDEO_FRAMES": {}, "DURATION_SECONDS": {},
	"INIT_IMAGE": {}, "END_IMAGE": {}, "CONTROL_FRAMES_JSON": {}, "SELECTED_ASSETS_JSON": {}, "MANIFEST_PATH": {},
}

// TemplateVariables separates ordinary single-argument values from an
// explicit, trusted raw fragment. Raw names must end in _RAW.
type TemplateVariables struct {
	Values map[string]string
	Raw    map[string]string
}

// ExpandCLITemplate replaces only documented single-argument variables and
// explicitly supplied *_RAW fragments. It never performs recursive expansion.
func ExpandCLITemplate(template string, variables TemplateVariables) (string, error) {
	if len(template) > maximumExpandedCLITemplateBytes {
		return "", fmt.Errorf("CLI template result exceeds %d bytes", maximumExpandedCLITemplateBytes)
	}
	var expanded strings.Builder
	expanded.Grow(len(template))
	for offset := 0; offset < len(template); {
		nextOpen := strings.Index(template[offset:], "{{")
		nextClose := strings.Index(template[offset:], "}}")
		if nextOpen < 0 && nextClose < 0 {
			if err := appendExpanded(&expanded, template[offset:]); err != nil {
				return "", err
			}
			break
		}
		if nextClose >= 0 && (nextOpen < 0 || nextClose < nextOpen) {
			return "", fmt.Errorf("CLI template has an unmatched closing token")
		}
		open := offset + nextOpen
		if err := appendExpanded(&expanded, template[offset:open]); err != nil {
			return "", err
		}
		closeOffset := strings.Index(template[open+2:], "}}")
		if closeOffset < 0 {
			return "", fmt.Errorf("CLI template has an unmatched opening token")
		}
		close := open + 2 + closeOffset
		name := template[open+2 : close]
		if strings.Contains(name, "{{") || !validCLITemplateVariable(name) {
			return "", fmt.Errorf("CLI template token %q is invalid", name)
		}
		value, raw, err := templateValue(name, variables)
		if err != nil {
			return "", err
		}
		if !raw {
			value = shellQuote(value)
		}
		if err := appendExpanded(&expanded, value); err != nil {
			return "", err
		}
		offset = close + 2
	}
	return expanded.String(), nil
}

func templateValue(name string, variables TemplateVariables) (string, bool, error) {
	_, ordinary := ordinaryCLITemplateVariables[name]
	rawName := strings.HasSuffix(name, "_RAW")
	if !ordinary && !rawName {
		return "", false, fmt.Errorf("CLI template token %q is not allowed", name)
	}
	_, inValues := variables.Values[name]
	raw, inRaw := variables.Raw[name]
	if inValues && inRaw {
		return "", false, fmt.Errorf("CLI template token %q has conflicting values", name)
	}
	if rawName {
		if inValues || !inRaw {
			return "", false, fmt.Errorf("CLI template raw token %q is missing", name)
		}
		return raw, true, nil
	}
	if inRaw || !inValues {
		return "", false, fmt.Errorf("CLI template token %q is missing", name)
	}
	return variables.Values[name], false, nil
}

func validCLITemplateVariable(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		if !(character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_') {
			return false
		}
	}
	return true
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func appendExpanded(builder *strings.Builder, value string) error {
	if builder.Len()+len(value) > maximumExpandedCLITemplateBytes {
		return fmt.Errorf("CLI template result exceeds %d bytes", maximumExpandedCLITemplateBytes)
	}
	builder.WriteString(value)
	return nil
}
