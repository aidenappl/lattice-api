package logger

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// capture installs both sinks for one test and removes them after it.
func capture(t *testing.T) (records func() []Record, panics func() []PanicRecord) {
	t.Helper()
	var mu sync.Mutex
	var rs []Record
	var ps []PanicRecord
	SetSink(func(r Record) { mu.Lock(); rs = append(rs, r); mu.Unlock() })
	SetPanicSink(func(_ context.Context, p PanicRecord) { mu.Lock(); ps = append(ps, p); mu.Unlock() })
	t.Cleanup(func() { SetSink(nil); SetPanicSink(nil) })
	return func() []Record { mu.Lock(); defer mu.Unlock(); return append([]Record(nil), rs...) },
		func() []PanicRecord { mu.Lock(); defer mu.Unlock(); return append([]PanicRecord(nil), ps...) }
}

func TestSinkReceivesEachCallWithItsCaller(t *testing.T) {
	records, _ := capture(t)

	Warn("worker", "heartbeat update failed", F{"worker_id": 3})

	rs := records()
	if len(rs) != 1 {
		t.Fatalf("sink received %d records, want 1", len(rs))
	}
	r := rs[0]
	if r.Level != LevelWarn || r.Component != "worker" || r.Msg != "heartbeat update failed" || r.Fields["worker_id"] != 3 {
		t.Errorf("record = %+v", r)
	}
	if !strings.HasPrefix(r.Caller, "logger_test.go:") {
		t.Errorf("caller = %q, want the line that logged, in logger_test.go", r.Caller)
	}
}

func TestSinkIsIndependentOfLogLevel(t *testing.T) {
	records, _ := capture(t)
	Init("error", "text")
	t.Cleanup(func() { Init("info", "text") })

	Info("retention", "starting cleanup")

	if len(records()) != 1 {
		t.Error("LOG_LEVEL decides what reaches stdout, not what the sink receives")
	}
}

func TestRequestLinesAreNotPassedOn(t *testing.T) {
	records, _ := capture(t)

	Request("abc", "GET", "/admin/stacks", 500, time.Millisecond)

	if n := len(records()); n != 0 {
		t.Errorf("sink received %d records; the request middleware reports requests itself", n)
	}
}

func TestRecoverReportsThePanicWithTheStackWhereItHappened(t *testing.T) {
	_, panics := capture(t)

	func() {
		defer Recover("retention", F{"table": "container_logs"})
		panicsHere()
	}()

	ps := panics()
	if len(ps) != 1 {
		t.Fatalf("panic sink received %d reports, want 1", len(ps))
	}
	p := ps[0]
	if p.Goroutine != "retention" || p.Recovered != "boom" || p.Fields["table"] != "container_logs" {
		t.Errorf("report = %+v", p)
	}
	if !strings.Contains(p.Stack, "panicsHere") {
		t.Errorf("stack should include the frame that panicked:\n%s", p.Stack)
	}
}

func TestRecoverIsSilentWithoutAPanic(t *testing.T) {
	_, panics := capture(t)

	func() { defer Recover("idle") }()

	if n := len(panics()); n != 0 {
		t.Errorf("reported %d panics with nothing panicking", n)
	}
}

func panicsHere() { panic("boom") }
