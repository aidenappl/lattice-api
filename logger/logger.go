package logger

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// Level represents log severity
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var (
	minLevel  = LevelInfo
	useJSON   = false
	outLogger = log.New(os.Stdout, "", 0)
)

// Init configures the logger. Call at startup.
// level: "debug", "info", "warn", "error"
// format: "text" or "json"
func Init(level, format string) {
	switch strings.ToLower(level) {
	case "debug":
		minLevel = LevelDebug
	case "warn", "warning":
		minLevel = LevelWarn
	case "error":
		minLevel = LevelError
	default:
		minLevel = LevelInfo
	}
	useJSON = strings.ToLower(format) == "json"
}

func levelStr(l Level) string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "INFO"
	}
}

func levelColor(l Level) string {
	if useJSON {
		return ""
	}
	switch l {
	case LevelDebug:
		return "\033[36m" // cyan
	case LevelInfo:
		return "\033[32m" // green
	case LevelWarn:
		return "\033[33m" // yellow
	case LevelError:
		return "\033[31m" // red
	default:
		return ""
	}
}

const resetColor = "\033[0m"

func emit(level Level, component, msg string, fields map[string]any) {
	if level < minLevel {
		return
	}

	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	if useJSON {
		// Structured JSON output
		parts := []string{
			fmt.Sprintf(`"ts":"%s"`, ts),
			fmt.Sprintf(`"level":"%s"`, levelStr(level)),
		}
		if component != "" {
			parts = append(parts, fmt.Sprintf(`"component":"%s"`, component))
		}
		parts = append(parts, fmt.Sprintf(`"msg":"%s"`, escapeJSON(msg)))
		for k, v := range fields {
			parts = append(parts, fmt.Sprintf(`"%s":%s`, k, formatValue(v)))
		}
		outLogger.Printf("{%s}", strings.Join(parts, ","))
	} else {
		// Human-readable colored output
		color := levelColor(level)
		prefix := fmt.Sprintf("%s %s%-5s%s", ts, color, levelStr(level), resetColor)
		if component != "" {
			prefix += fmt.Sprintf(" [%s]", component)
		}
		if len(fields) > 0 {
			fieldParts := make([]string, 0, len(fields))
			for k, v := range fields {
				fieldParts = append(fieldParts, fmt.Sprintf("%s=%v", k, v))
			}
			outLogger.Printf("%s %s %s", prefix, msg, strings.Join(fieldParts, " "))
		} else {
			outLogger.Printf("%s %s", prefix, msg)
		}
	}
}

func escapeJSON(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}

func formatValue(v any) string {
	switch val := v.(type) {
	case string:
		return fmt.Sprintf(`"%s"`, escapeJSON(val))
	case int, int64, float64, bool:
		return fmt.Sprintf("%v", val)
	default:
		return fmt.Sprintf(`"%v"`, escapeJSON(fmt.Sprint(val)))
	}
}

// ─── Public API ──────────────────────────────────────────────────────────────

func Debug(component, msg string, fields ...map[string]any) {
	f := mergeFields(fields)
	emit(LevelDebug, component, msg, f)
}

func Info(component, msg string, fields ...map[string]any) {
	f := mergeFields(fields)
	emit(LevelInfo, component, msg, f)
}

func Warn(component, msg string, fields ...map[string]any) {
	f := mergeFields(fields)
	emit(LevelWarn, component, msg, f)
}

func Error(component, msg string, fields ...map[string]any) {
	f := mergeFields(fields)
	emit(LevelError, component, msg, f)
}

// F is a convenience alias for map[string]any
type F = map[string]any

func mergeFields(fields []map[string]any) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	merged := make(map[string]any)
	for _, f := range fields {
		for k, v := range f {
			merged[k] = v
		}
	}
	return merged
}

// ─── HTTP request logging ────────────────────────────────────────────────────

func Request(requestID, method, path string, status int, duration time.Duration) {
	level := LevelInfo
	if status >= 500 {
		level = LevelError
	} else if status >= 400 {
		level = LevelWarn
	}
	emit(level, "http", fmt.Sprintf("%s %s", method, path), F{
		"status":     status,
		"duration_ms": duration.Milliseconds(),
		"request_id": requestID,
	})
}
