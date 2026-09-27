package routers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/mailer"
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

	var form settingsForm
	form.boolean("smtp.enabled", body.Enabled)
	form.str("smtp.host", body.Host)
	form.str("smtp.port", body.Port)
	form.str("smtp.username", body.Username)
	form.str("smtp.from_email", body.FromEmail)
	form.str("smtp.from_name", body.FromName)
	form.str("smtp.recipients", body.Recipients)

	// Only update password if non-empty and not the masked value. A password
	// that cannot be encrypted is never stored, and never silently skipped.
	if body.Password != nil && *body.Password != "" && !strings.HasPrefix(*body.Password, "••") {
		encrypted, err := crypto.Encrypt(*body.Password)
		if err != nil {
			responder.SendError(w, http.StatusInternalServerError, "failed to encrypt smtp password", err)
			return
		}
		form.raw("smtp.password", encrypted)
	}

	if err := saveSettings(form); err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to save smtp configuration", err)
		return
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
