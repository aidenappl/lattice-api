// Package telemetry connects lattice-api to Monitor.
//
// Monitor runs as a stack on the Lattice it would be reporting on, so it can
// never be a boot requirement here: nothing in this package touches the network
// at startup or fails, and with MONITOR_SPOOL_DIR set every event waits on disk
// until Monitor answers — through a Monitor outage, and through restarts of this
// process, which is how events from the boot that deploys Monitor still arrive.
package telemetry

import (
	"context"
	"fmt"
	"log"
	"log/slog"

	monitor "github.com/aidenappl/go-monitor"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/logger"
)

// Service is the name every lattice-api event is filed under.
const Service = "lattice-api"

// Init configures Monitor from the environment, routes the logger into it and
// announces the start. It never returns an error: a misconfiguration is logged
// and the service runs on without telemetry.
func Init(version string) {
	err := monitor.Init(monitor.Config{
		Service:       Service,
		Env:           env.MonitorEnv,
		Zone:          env.MonitorZone,
		IngestURL:     env.MonitorIngestURL,
		APIKey:        env.MonitorAPIKey,
		SpoolDir:      env.MonitorSpoolDir,
		Debug:         env.MonitorDebug,
		DisableStdout: !env.MonitorStdout,
		GzipEnabled:   true,
		// User records carry email addresses: personal data that no failure
		// needs in order to be diagnosed.
		RedactKeys: []string{"email"},
	})
	if err != nil {
		logger.Warn("telemetry", "Monitor disabled", logger.F{"error": err.Error()})
		return
	}
	InstallSinks(version)
	if env.MonitorIngestURL == "" {
		logger.Warn("telemetry", "MONITOR_INGEST_URL is not set; events are not being shipped")
	}
	monitor.Info(context.Background(), "service.startup", map[string]any{
		"version":     version,
		"port":        env.Port,
		"environment": env.Environment,
		"spool":       env.MonitorSpoolDir != "",
	})
}

// InstallSinks routes logging into Monitor: every log record becomes a
// "<component>.log.<level>" event attributed to the line that logged and
// carrying version, and every recovered panic a panic.recovered event with its
// stack.
//
// componentHandler writes each record unchanged to stdout (what LOG_LEVEL
// allows, in the format it always had) and hands Monitor a copy with the event
// name and the warn limiter applied. Monitor keeps info and above (debug too
// with MONITOR_DEBUG).
func InstallSinks(version string) {
	level := slog.LevelInfo
	if env.MonitorDebug {
		level = slog.LevelDebug
	}
	// version is a WithAttrs attribute, so a record's own "version" field (a
	// runner's, say) wins over it in the event's data.
	mon := monitor.NewSlogHandler(nil, &monitor.SlogOptions{Level: level}).
		WithAttrs([]slog.Attr{slog.String("version", version)})
	logger.SetHandler(newComponentHandler(logger.StdoutHandler(), mon, DEFAULT_WARN_LIMIT, DEFAULT_WARN_WINDOW))
	logger.SetPanicSink(emitPanic)
}

func emitPanic(ctx context.Context, p logger.PanicRecord) {
	data := make(map[string]any, len(p.Fields)+4)
	for k, v := range p.Fields {
		if e, ok := v.(error); ok && e != nil {
			v = e.Error()
		}
		data[k] = v
	}
	data["goroutine"] = p.Goroutine
	data["error"] = fmt.Sprint(p.Recovered)
	data["panic_type"] = fmt.Sprintf("%T", p.Recovered)
	data["stacktrace"] = p.Stack
	monitor.Emit(ctx, "panic.recovered", data, monitor.WithLevel(monitor.LevelError))
}

// Shutdown announces the stop and delivers (or, with a spool, persists) what is
// still buffered. Bounded: it never holds the process for more than a few
// seconds.
func Shutdown(reason string) {
	monitor.Info(context.Background(), "service.shutdown", map[string]any{"reason": reason})
	monitor.Shutdown()
}

// ReportFatal records why the process is about to exit and flushes, for exit
// paths that print their own diagnostic. It does not exit.
func ReportFatal(name string, data map[string]any) {
	monitor.Fatal(context.Background(), name, data)
	monitor.Shutdown()
}

// Fatal reports a boot failure, flushes, and exits. log.Fatal alone skips
// deferred functions, so the one event that explains why the control plane is
// down would never leave the process.
func Fatal(name, msg string, err error) {
	data := map[string]any{"message": msg}
	if err != nil {
		data["error"] = err.Error()
	}
	ReportFatal(name, data)
	log.Fatal(msg, err)
}

// CrashGuard reports a panic that is about to kill the process, then lets it.
// Defer it first thing in main: the boot's own panics (a weak JWT_SIGNING_KEY,
// a missing ENCRYPTION_KEY in production) are exactly what a crashloop looks
// like from outside, and without this they reach only a container log.
func CrashGuard() {
	rec := recover()
	if rec == nil {
		return
	}
	logger.Panic("main", rec)
	monitor.Fatal(context.Background(), "service.crashed", map[string]any{
		"error":      fmt.Sprint(rec),
		"panic_type": fmt.Sprintf("%T", rec),
	})
	monitor.Shutdown()
	panic(rec)
}
