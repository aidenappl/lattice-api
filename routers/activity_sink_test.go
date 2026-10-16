package routers

import (
	"testing"

	"github.com/aidenappl/lattice-api/structs"
)

// Local (password) users, pre-heal email subjects and SSO logins never reach
// the sink, so none of them touches the database for SSO config.
func TestReportActivitySkips(t *testing.T) {
	sub, email := "abc", "a@b.test"
	reportActivity(nil, "deploy", "stack", nil)
	reportActivity(&structs.User{ID: 1}, "deploy", "stack", nil)
	reportActivity(&structs.User{ID: 1, SSOSubject: &email}, "deploy", "stack", nil)
	reportActivity(&structs.User{ID: 1, SSOSubject: &sub}, "sso_login", "user", nil)
}

// Without ACTIVITY_REPORT_URL there is no sink, whatever SSO says.
func TestActivitySinkOffByDefault(t *testing.T) {
	if activitySink() != nil {
		t.Fatal("sink built without ACTIVITY_REPORT_URL")
	}
}
