package routers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/mailer"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

// HandleGetSMTPConfig returns the full SMTP configuration (with password masked).
// GET /admin/smtp-config
func HandleGetSMTPConfig(w http.ResponseWriter, r *http.Request) {
	cfg := mailer.LoadConfig()

	// Mask the password for display
	maskedPassword := ""
	if cfg.Password != "" {
		if len(cfg.Password) > 4 {
			maskedPassword = "••••••••" + cfg.Password[len(cfg.Password)-4:]
		} else {
			maskedPassword = "••••••••"
		}
	}

	responder.New(w, map[string]any{
		"enabled":    cfg.Enabled,
		"host":       cfg.Host,
		"port":       cfg.Port,
		"username":   cfg.Username,
		"password":   maskedPassword,
		"from_email": cfg.FromEmail,
		"from_name":  cfg.FromName,
		"recipients": cfg.Recipients,
	}, "SMTP configuration")
}

// HandleUpdateSMTPConfig updates SMTP configuration in the database.
// PUT /admin/smtp-config
func HandleUpdateSMTPConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled    *bool   `json:"enabled"`
		Host       *string `json:"host"`
		Port       *string `json:"port"`
		Username   *string `json:"username"`
		Password   *string `json:"password"`
		FromEmail  *string `json:"from_email"`
		FromName   *string `json:"from_name"`
		Recipients *string `json:"recipients"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	// Helper to set a string setting if provided (trims whitespace)
	setIf := func(key string, val *string) {
		if val != nil {
			_ = query.SetSetting(db.DB, key, strings.TrimSpace(*val))
		}
	}
	// Helper to set a bool setting if provided
	setBoolIf := func(key string, val *bool) {
		if val != nil {
			v := "false"
			if *val {
				v = "true"
			}
			_ = query.SetSetting(db.DB, key, v)
		}
	}

	setBoolIf("smtp.enabled", body.Enabled)
	setIf("smtp.host", body.Host)
	setIf("smtp.port", body.Port)
	setIf("smtp.username", body.Username)
	setIf("smtp.from_email", body.FromEmail)
	setIf("smtp.from_name", body.FromName)
	setIf("smtp.recipients", body.Recipients)

	// Only update password if non-empty and not the masked value
	if body.Password != nil && *body.Password != "" && !strings.HasPrefix(*body.Password, "••") {
		encrypted, err := crypto.Encrypt(*body.Password)
		if err == nil {
			_ = query.SetSetting(db.DB, "smtp.password", encrypted)
		}
	}

	logAudit(r, "update", "smtp_config", nil, nil)

	responder.New(w, nil, "SMTP configuration updated")
}

// HandleTestSMTP sends a test email using the current SMTP configuration.
// POST /admin/smtp-config/test
func HandleTestSMTP(w http.ResponseWriter, r *http.Request) {
	err := mailer.SendSync("[Lattice] Test Email", "This is a test email from Lattice. If you received this, your SMTP configuration is working correctly.")
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "SMTP test failed: "+err.Error())
		return
	}
	responder.New(w, nil, "test email sent successfully")
}
