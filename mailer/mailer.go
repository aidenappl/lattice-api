package mailer

import (
	"fmt"
	"net/smtp"
	"strings"

	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
)

// AllEventTypes lists all supported notification event types.
var AllEventTypes = []string{
	"worker.disconnected",
	"worker.crash",
	"container.unhealthy",
	"deployment.failed",
	"deployment.success",
}

// EventLabels provides human-readable names for event types.
var EventLabels = map[string]string{
	"worker.disconnected": "Worker Disconnected",
	"worker.crash":        "Worker Crashed",
	"container.unhealthy": "Container Unhealthy",
	"deployment.failed":   "Deployment Failed",
	"deployment.success":  "Deployment Successful",
}

// SMTPConfig holds the SMTP settings loaded from the database.
type SMTPConfig struct {
	Enabled    bool
	Host       string
	Port       string
	Username   string
	Password   string
	FromEmail  string
	FromName   string
	Recipients string // comma-separated email addresses
}

// LoadConfig reads SMTP configuration from the settings table.
func LoadConfig() *SMTPConfig {
	settings, err := query.GetSettingsByPrefix(db.DB, "smtp.")
	if err != nil || len(settings) == 0 {
		return &SMTPConfig{Enabled: false}
	}

	cfg := &SMTPConfig{
		Enabled:    settings["smtp.enabled"] == "true",
		Host:       strings.TrimSpace(settings["smtp.host"]),
		Port:       strings.TrimSpace(settings["smtp.port"]),
		Username:   strings.TrimSpace(settings["smtp.username"]),
		FromEmail:  strings.TrimSpace(settings["smtp.from_email"]),
		FromName:   strings.TrimSpace(settings["smtp.from_name"]),
		Recipients: strings.TrimSpace(settings["smtp.recipients"]),
	}

	if password, ok := settings["smtp.password"]; ok && password != "" {
		decrypted, err := crypto.Decrypt(password)
		if err == nil {
			cfg.Password = decrypted
		} else {
			cfg.Password = password
		}
	}

	return cfg
}

// doSend sends an email with the given subject and HTML body.
func doSend(subject, htmlBody string) error {
	cfg := LoadConfig()
	if !cfg.Enabled || cfg.Host == "" || cfg.Recipients == "" {
		return fmt.Errorf("SMTP not configured or disabled")
	}

	recipients := strings.Split(cfg.Recipients, ",")
	for i := range recipients {
		recipients[i] = strings.TrimSpace(recipients[i])
	}

	from := cfg.FromEmail
	if cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", cfg.FromName, cfg.FromEmail)
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		from,
		strings.Join(recipients, ", "),
		subject,
		htmlBody,
	)

	addr := cfg.Host + ":" + cfg.Port
	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	return smtp.SendMail(addr, auth, cfg.FromEmail, recipients, []byte(msg))
}

// SendSync sends an email synchronously and returns any error.
func SendSync(subject, body string) error {
	return doSend(subject, renderEmail(subject, body))
}

// Send sends an email asynchronously (fire-and-forget).
func Send(subject, body string) {
	go func() {
		if err := doSend(subject, renderEmail(subject, body)); err != nil {
			logger.Error("mailer", "failed to send email", logger.F{"error": err, "subject": subject})
		} else {
			logger.Info("mailer", "email sent", logger.F{"subject": subject})
		}
	}()
}

// Notify sends an email if SMTP is configured and the event type is enabled.
// For events with cooldowns/thresholds, use ShouldAlert/TrackUnhealthy first.
func Notify(eventType, subject, body string) {
	cfg := GetEventConfig(eventType)
	if !cfg.Enabled {
		return
	}
	Send(fmt.Sprintf("[Lattice] %s", subject), body)
}

// renderEmail wraps a plain-text body in an HTML email template.
func renderEmail(subject, body string) string {
	// Determine accent color based on subject keywords
	accentColor := "#3b82f6" // blue default
	iconEmoji := "ℹ️"
	if strings.Contains(subject, "Failed") || strings.Contains(subject, "Crashed") || strings.Contains(subject, "Unhealthy") {
		accentColor = "#ef4444" // red
		iconEmoji = "🔴"
	} else if strings.Contains(subject, "Successful") || strings.Contains(subject, "Success") {
		accentColor = "#22c55e" // green
		iconEmoji = "✅"
	} else if strings.Contains(subject, "Disconnected") {
		accentColor = "#f59e0b" // amber
		iconEmoji = "⚠️"
	}

	// Convert newlines in body to HTML line breaks
	htmlBody := strings.ReplaceAll(body, "\n", "<br>")

	// Strip [Lattice] prefix from display title
	displayTitle := strings.TrimPrefix(subject, "[Lattice] ")

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
</head>
<body style="margin:0;padding:0;background-color:#111113;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;">
<table width="100%%" cellpadding="0" cellspacing="0" style="background-color:#111113;padding:32px 16px;">
<tr><td align="center">
<table width="560" cellpadding="0" cellspacing="0" style="max-width:560px;width:100%%;">

<!-- Accent bar -->
<tr><td style="background:%s;height:4px;border-radius:8px 8px 0 0;"></td></tr>

<!-- Card -->
<tr><td style="background-color:#1a1a1e;padding:28px 32px;border-radius:0 0 8px 8px;">

<!-- Title -->
<table width="100%%" cellpadding="0" cellspacing="0">
<tr>
<td style="font-size:18px;font-weight:600;color:#e4e4e7;padding-bottom:16px;">
%s&nbsp;&nbsp;%s
</td>
</tr>
</table>

<!-- Body -->
<table width="100%%" cellpadding="0" cellspacing="0">
<tr>
<td style="font-size:14px;line-height:22px;color:#a1a1aa;padding-bottom:24px;">
%s
</td>
</tr>
</table>

<!-- Divider -->
<table width="100%%" cellpadding="0" cellspacing="0">
<tr><td style="border-top:1px solid #27272a;padding-top:16px;">
<span style="font-size:11px;color:#52525b;">Sent by Lattice &bull; Container Orchestration Platform</span>
</td></tr>
</table>

</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`, accentColor, iconEmoji, displayTitle, htmlBody)
}
