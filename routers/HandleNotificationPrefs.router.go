package routers

import (
	"encoding/json"
	"net/http"

	"github.com/aidenappl/lattice-api/mailer"
	"github.com/aidenappl/lattice-api/responder"
)

// HandleGetNotificationPrefs returns the full notification preferences.
func HandleGetNotificationPrefs(w http.ResponseWriter, r *http.Request) {
	prefs := mailer.LoadPreferences()
	responder.New(w, prefs, "notification preferences retrieved")
}

// HandleUpdateNotificationPrefs updates notification preferences.
func HandleUpdateNotificationPrefs(w http.ResponseWriter, r *http.Request) {
	var body mailer.EventPreferences
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	// Load current prefs and merge incoming changes
	current := mailer.LoadPreferences()
	for eventType, cfg := range body {
		if _, exists := current[eventType]; !exists {
			continue // ignore unknown event types
		}
		current[eventType] = cfg
	}

	if err := mailer.SavePreferences(current); err != nil {
		responder.QueryError(w, err, "failed to save preferences")
		return
	}

	logAudit(r, "update", "notification_prefs", nil, nil)
	responder.New(w, current, "notification preferences updated")
}
