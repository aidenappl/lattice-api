package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/tools"
)

const (
	// DEFAULT_TIMEOUT bounds a delivery that does not name its own timeout.
	DEFAULT_TIMEOUT = 10 * time.Second
	// MAX_RESPONSE_SNIPPET caps how much of a response body is kept: enough to
	// explain a failure in a run history, never a whole page.
	MAX_RESPONSE_SNIPPET = 512
	// USER_AGENT is sent unless the request overrides it.
	USER_AGENT = "Lattice-Webhook/1.0"
)

type WebhookPayload struct {
	Event     string `json:"event"`
	Timestamp string `json:"timestamp"`
	Data      any    `json:"data"`
}

// Request is one outbound HTTP call to a user- or admin-configured URL.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
	// Secret, when set, signs Body with HMAC-SHA256 into X-Lattice-Signature.
	Secret  *string
	Timeout time.Duration
}

// Response is what came back. Body holds at most MAX_RESPONSE_SNIPPET bytes.
type Response struct {
	StatusCode int
	Body       string
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

// Deliver makes one outbound request and reports the response status.
//
// It is the one outbound HTTP path for user- and admin-configured URLs: event
// webhooks go through it, and so do automation http_request actions. Keeping a
// single path is what keeps the SSRF guard in one place — the dialer is pinned
// to public IPs at connect time (tools.NewSafeHTTPClient), which also covers
// every redirect hop, so validating a URL when it is saved is not the only
// defence against DNS rebinding.
//
// The request is bounded twice: by ctx, and by the client timeout. A non-2xx
// status is NOT an error here; callers decide what a status means.
func Deliver(ctx context.Context, r Request) (*Response, error) {
	method := r.Method
	if method == "" {
		method = http.MethodPost
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DEFAULT_TIMEOUT
	}

	var body io.Reader
	if len(r.Body) > 0 {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.URL, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", USER_AGENT)
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}

	// Sign with HMAC-SHA256 if secret is configured
	if r.Secret != nil && *r.Secret != "" {
		mac := hmac.New(sha256.New, []byte(*r.Secret))
		mac.Write(r.Body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Lattice-Signature", "sha256="+sig)
	}

	resp, err := tools.NewSafeHTTPClient(timeout).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, MAX_RESPONSE_SNIPPET))
	return &Response{StatusCode: resp.StatusCode, Body: string(snippet)}, nil
}

func sendWebhook(url string, secret *string, body []byte) {
	resp, err := Deliver(context.Background(), Request{
		Method:  http.MethodPost,
		URL:     url,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
		Secret:  secret,
	})
	if err != nil {
		logger.Error("webhook", "delivery failed", logger.F{"url": url, "error": err})
		return
	}

	if resp.StatusCode >= 400 {
		logger.Warn("webhook", "delivery returned error status", logger.F{"url": url, "status": resp.StatusCode})
	}
}
