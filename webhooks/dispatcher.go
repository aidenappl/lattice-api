package webhooks

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
)

type WebhookPayload struct {
	Event     string `json:"event"`
	Timestamp string `json:"timestamp"`
	Data      any    `json:"data"`
}

// Fire sends a webhook notification to all configured endpoints that subscribe to the given event.
func Fire(event string, data any) {
	go func() {
		configs, err := query.ListWebhookConfigs(db.DB)
		if err != nil || configs == nil {
			return
		}

		payload := WebhookPayload{
			Event:     event,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Data:      data,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return
		}

		for _, cfg := range *configs {
			if !cfg.Active {
				continue
			}
			// Check if this webhook subscribes to this event
			var events []string
			if json.Unmarshal([]byte(cfg.Events), &events) != nil {
				continue
			}
			subscribed := false
			for _, e := range events {
				if e == event || e == "*" {
					subscribed = true
					break
				}
			}
			if !subscribed {
				continue
			}

			go sendWebhook(cfg.URL, cfg.Secret, body)
		}
	}()
}

func sendWebhook(url string, secret *string, body []byte) {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		logger.Error("webhook", "failed to create request", logger.F{"url": url, "error": err})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Lattice-Webhook/1.0")

	// Sign with HMAC-SHA256 if secret is configured
	if secret != nil && *secret != "" {
		mac := hmac.New(sha256.New, []byte(*secret))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Lattice-Signature", "sha256="+sig)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logger.Error("webhook", "delivery failed", logger.F{"url": url, "error": err})
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		logger.Warn("webhook", "delivery returned error status", logger.F{"url": url, "status": resp.StatusCode})
	}
}
