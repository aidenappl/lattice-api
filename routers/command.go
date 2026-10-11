package routers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mattn/go-shellwords"
)

// parseCommand turns a command or entrypoint into argv. It accepts either form
// compose does: a JSON array (["sh", "-c", "echo hi"]) used as-is, or a string
// split like a shell (sh -c "echo hi" -> [sh -c echo hi]), via go-shellwords —
// the splitter compose-go uses. Blank input is no command (nil, nil).
//
// Before this, API bodies only worked as JSON arrays (a plain `npm start` was
// stored, then dropped at deploy as invalid JSON) and compose strings were split
// on whitespace, so quoted arguments came apart.
func parseCommand(raw string) ([]string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	if strings.HasPrefix(s, "[") {
		var argv []string
		if err := json.Unmarshal([]byte(s), &argv); err != nil {
			return nil, fmt.Errorf("looks like a JSON array but is not a list of strings: %w", err)
		}
		return argv, nil
	}
	p := shellwords.NewParser()
	argv, err := p.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("cannot split %q into arguments: %w", s, err)
	}
	// go-shellwords stops at an unquoted ; & | < > and returns the words before
	// it with no error, so `echo a && echo b` would silently become [echo a].
	// Docker runs argv without a shell, so the operator could never work anyway.
	if p.Position != -1 {
		return nil, fmt.Errorf("%q contains an unquoted shell operator (&&, |, ;, <, >), which only a shell understands; "+
			"wrap it: sh -c \"...\"", s)
	}
	return argv, nil
}

// commandFromBody validates a command/entrypoint from an API body (create,
// update, stack import) and returns it stored as a JSON array. nil leaves the
// field untouched; "" clears it.
func commandFromBody(field string, raw *string) (*string, error) {
	if raw == nil || *raw == "" {
		return raw, nil
	}
	argv, err := parseCommand(*raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	if len(argv) == 0 {
		empty := ""
		return &empty, nil
	}
	b, err := json.Marshal(argv)
	if err != nil {
		return nil, err
	}
	s := string(b)
	return &s, nil
}

// composeArgv reads a compose `command:` / `entrypoint:` value, which YAML
// decodes as either a string or a list.
func composeArgv(v any) ([]string, error) {
	switch val := v.(type) {
	case nil:
		return nil, nil
	case string:
		return parseCommand(val)
	case []any:
		argv := make([]string, 0, len(val))
		for _, item := range val {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("list items must be strings, got %v", item)
			}
			argv = append(argv, s)
		}
		return argv, nil
	}
	return nil, fmt.Errorf("must be a string or a list")
}

// validateComposeCommands checks every service's command and entrypoint up
// front, so a bad value rejects the whole file before anything is written.
func validateComposeCommands(compose composeFile) error {
	for name, svc := range compose.Services {
		if _, err := composeArgv(svc.Command); err != nil {
			return fmt.Errorf("service %s: command %w", name, err)
		}
		if _, err := composeArgv(svc.Entrypoint); err != nil {
			return fmt.Errorf("service %s: entrypoint %w", name, err)
		}
	}
	return nil
}
