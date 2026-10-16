package routers

import (
	"context"
	"strconv"
	"sync"

	"github.com/aidenappl/go-forta/activity"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/sso"
	"github.com/aidenappl/lattice-api/structs"
)

// Activity sink: what an SSO-signed-in person does in Lattice (deploys,
// rollbacks, credential reveals, deletes…) is mirrored to an external sink —
// the identity provider's per-user activity trail — so "everything this person
// did" spans platforms. Lattice's own audit log stays the record.
//
// Opt-in: off unless ACTIVITY_REPORT_URL is set. The sink authenticates the SSO
// client id/secret; that config lives in the database and can change at
// runtime, so the reporter is rebuilt when the client changes. Fire-and-forget
// and never fails or slows a request. Only identifiers travel — resource type,
// action, id — never the audit row's details text (it can carry emails/names).

var (
	sinkMu       sync.Mutex
	sinkReporter *activity.Reporter
	sinkClient   string // ClientID + "\x00" + ClientSecret the reporter was built with
)

// activitySink returns a reporter for the current SSO client, or nil when no
// sink is configured or SSO cannot authenticate to it.
func activitySink() *activity.Reporter {
	if env.ActivityReportURL == "" {
		return nil
	}
	cfg := sso.LoadConfig()
	if cfg == nil || !cfg.Enabled || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil
	}
	client := cfg.ClientID + "\x00" + cfg.ClientSecret
	sinkMu.Lock()
	defer sinkMu.Unlock()
	if sinkReporter != nil && client == sinkClient {
		return sinkReporter
	}
	if old := sinkReporter; old != nil {
		go old.Close(context.Background())
	}
	sinkReporter = activity.New(activity.Config{
		Endpoint:     env.ActivityReportURL,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		OnError: func(action string, err error) {
			logger.Warn("activity_sink", "activity not recorded by the sink", logger.F{"action": action, "error": err})
		},
	})
	sinkClient = client
	return sinkReporter
}

// skipSinkActions are already recorded by the identity provider itself.
var skipSinkActions = map[string]bool{"sso_login": true}

// reportActivity forwards one audited action by an SSO user. Runs on the audit
// goroutine; never blocks the request.
func reportActivity(user *structs.User, action, resourceType string, resourceID *int) {
	// A pre-heal row can still hold an email in sso_subject; the sink would refuse it.
	if user == nil || user.SSOSubject == nil || *user.SSOSubject == "" || sso.LooksLikeEmail(*user.SSOSubject) || skipSinkActions[action] {
		return
	}
	r := activitySink()
	if r == nil {
		return
	}
	var details map[string]string
	if resourceID != nil {
		details = map[string]string{"id": strconv.Itoa(*resourceID)}
	}
	r.Report(*user.SSOSubject, resourceType+"."+action, activity.Success, details)
}
