package backend

import (
	"fmt"
	"regexp"
	"strings"
)

var variableNamePattern = regexp.MustCompile(`^[A-Z0-9_]+$`)

func ExpandCommand(command string, values map[string]string) (string, error) {
	var expanded strings.Builder
	for index := 0; index < len(command); {
		if command[index] != '$' {
			expanded.WriteByte(command[index])
			index++
			continue
		}
		if index+1 < len(command) && command[index+1] == '$' {
			expanded.WriteByte('$')
			index += 2
			continue
		}
		if index+1 >= len(command) || command[index+1] != '{' {
			expanded.WriteByte('$')
			index++
			continue
		}
		closing := strings.IndexByte(command[index+2:], '}')
		if closing < 0 {
			return "", fmt.Errorf("unterminated template variable at byte %d", index)
		}
		closing += index + 2
		name := command[index+2 : closing]
		if !variableNamePattern.MatchString(name) {
			return "", fmt.Errorf("invalid template variable %q", name)
		}
		lookupName := name
		quoted := strings.HasSuffix(name, "_SH")
		if quoted {
			lookupName = strings.TrimSuffix(name, "_SH")
		}
		value, ok := values[lookupName]
		if !ok {
			return "", fmt.Errorf("unknown template variable %q", lookupName)
		}
		if quoted {
			expanded.WriteString(shellQuote(value))
		} else {
			expanded.WriteString(value)
		}
		index = closing + 1
	}
	return expanded.String(), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
