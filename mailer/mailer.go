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

// Send sends an email notification asynchronously.
func Send(subject, body string) {
	go func() {
		cfg := LoadConfig()
		if !cfg.Enabled || cfg.Host == "" || cfg.Recipients == "" {
			return
		}

		recipients := strings.Split(cfg.Recipients, ",")
		for i := range recipients {
			recipients[i] = strings.TrimSpace(recipients[i])
		}

		from := cfg.FromEmail
		if cfg.FromName != "" {
			from = fmt.Sprintf("%s <%s>", cfg.FromName, cfg.FromEmail)
		}

		msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
			from,
			strings.Join(recipients, ", "),
			subject,
			body,
		)

		addr := cfg.Host + ":" + cfg.Port
		var auth smtp.Auth
		if cfg.Username != "" {
			auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		}

		if err := smtp.SendMail(addr, auth, cfg.FromEmail, recipients, []byte(msg)); err != nil {
			logger.Error("mailer", "failed to send email", logger.F{"error": err, "subject": subject})
		} else {
			logger.Info("mailer", "email sent", logger.F{"subject": subject, "recipients": len(recipients)})
		}
	}()
}

// Notify sends an email if SMTP is configured for the given event type.
// eventType matches webhook event types (e.g., "deployment.failed", "container.unhealthy").
func Notify(eventType, subject, body string) {
	Send(fmt.Sprintf("[Lattice] %s", subject), body)
}
