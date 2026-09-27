package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	monitor "github.com/aidenappl/go-monitor"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/socket"
)

// ctxRecord is one log record with the correlation ids of the ctx it was
// logged under.
type ctxRecord struct {
	msg              string
	requestID, trace string
}

type ctxRecordingHandler struct{ records *[]ctxRecord }

func (h ctxRecordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h ctxRecordingHandler) Handle(ctx context.Context, r slog.Record) error {
	*h.records = append(*h.records, ctxRecord{msg: r.Message, requestID: monitor.RequestID(ctx), trace: monitor.TraceID(ctx)})
	return nil
}
func (h ctxRecordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h ctxRecordingHandler) WithGroup(string) slog.Handler      { return h }

// A runner reply that echoes its command's ids must put them on every event
// its handler logs, so the reply joins the request that issued the command.
func TestReplyHandlersLogUnderTheCommandsIDs(t *testing.T) {
	var got []ctxRecord
	prev := slog.Default().Handler()
	logger.SetHandler(ctxRecordingHandler{&got})
	t.Cleanup(func() { logger.SetHandler(prev) })

	const (
		cmd   = "fedcba9876543210"
		req   = "0123456789abcdef"
		trace = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	)
	tests := []struct {
		name      string
		raw       string
		wantReq   string
		wantTrace string
	}{
		{name: "reply with request and trace ids", raw: `{"type":"db_status","command_id":"` + cmd + `","request_id":"` + req + `","trace_id":"` + trace + `","payload":{"action":"start"}}`, wantReq: req, wantTrace: trace},
		{name: "reply from a runner echoing only command_id", raw: `{"type":"db_status","command_id":"` + cmd + `","payload":{"action":"start"}}`, wantReq: cmd},
		{name: "old runner reply with no ids", raw: `{"type":"db_status","payload":{"action":"start"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got = nil
			var msg socket.IncomingMessage
			if err := json.Unmarshal([]byte(tt.raw), &msg); err != nil {
				t.Fatal(err)
			}
			ctx := msg.Context(context.Background())

			// Each of these logs without touching the database.
			handleDbStatus(ctx, 3, msg.Payload) // no database_instance_id: warns and drops
			logDeploymentProgress(ctx, "deploying", logger.F{"deployment_id": 7})
			logDeploymentFailed(ctx, "deploying", logger.F{"deployment_id": 7})
			logContainerTransition(ctx, "status", "web", "running", "stopped")

			if len(got) != 4 {
				t.Fatalf("recorded %d events, want 4: %+v", len(got), got)
			}
			for _, r := range got {
				if r.requestID != tt.wantReq || r.trace != tt.wantTrace {
					t.Errorf("%q logged with (%q, %q), want (%q, %q)", r.msg, r.requestID, r.trace, tt.wantReq, tt.wantTrace)
				}
			}
		})
	}
}
