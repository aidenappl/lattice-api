package routers

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/webhooks"
	"github.com/gorilla/mux"
)

// HandleListWebhooks returns all active webhook configs.
// GET /admin/webhooks
func HandleListWebhooks(w http.ResponseWriter, r *http.Request) {
	configs, err := query.ListWebhookConfigs(db.DB)
	if err != nil {
		responder.QueryError(w, err, "failed to list webhook configs")
		return
	}

	responder.New(w, configs, "webhook configs retrieved")
}

// HandleCreateWebhook creates a new webhook config.
// POST /admin/webhooks
func HandleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string  `json:"name"`
		URL    string  `json:"url"`
		Events string  `json:"events"` // JSON array string e.g. '["container.status","*"]'
		Secret *string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Name == "" {
		responder.MissingBodyFields(w, "name")
		return
	}
	if body.URL == "" {
		responder.MissingBodyFields(w, "url")
		return
	}
	if body.Events == "" {
		responder.MissingBodyFields(w, "events")
		return
	}

	// Validate URL
	if _, err := url.ParseRequestURI(body.URL); err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid webhook URL")
		return
	}

	// Validate events is a JSON array
	var events []string
	if err := json.Unmarshal([]byte(body.Events), &events); err != nil {
		responder.SendError(w, http.StatusBadRequest, "events must be a JSON array of strings")
		return
	}

	cfg, err := query.CreateWebhookConfig(db.DB, query.CreateWebhookConfigRequest{
		Name:   body.Name,
		URL:    body.URL,
		Events: body.Events,
		Secret: body.Secret,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create webhook config")
		return
	}

	logAudit(r, "create", "webhook", intPtr(cfg.ID), strPtr(cfg.Name))
	responder.NewCreated(w, cfg, "webhook config created")
}

// HandleUpdateWebhook updates an existing webhook config.
// PUT /admin/webhooks/{id}
func HandleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid webhook id")
		return
	}

	var body struct {
		Name   *string `json:"name"`
		URL    *string `json:"url"`
		Events *string `json:"events"`
		Secret *string `json:"secret"`
		Active *bool   `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	// Validate URL if provided
	if body.URL != nil {
		if _, err := url.ParseRequestURI(*body.URL); err != nil {
			responder.SendError(w, http.StatusBadRequest, "invalid webhook URL")
			return
		}
	}

	// Validate events if provided
	if body.Events != nil {
		var events []string
		if err := json.Unmarshal([]byte(*body.Events), &events); err != nil {
			responder.SendError(w, http.StatusBadRequest, "events must be a JSON array of strings")
			return
		}
	}

	cfg, err := query.UpdateWebhookConfig(db.DB, id, query.UpdateWebhookConfigRequest{
		Name:   body.Name,
		URL:    body.URL,
		Events: body.Events,
		Secret: body.Secret,
		Active: body.Active,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to update webhook config")
		return
	}

	logAudit(r, "update", "webhook", intPtr(id), nil)
	responder.New(w, cfg, "webhook config updated")
}

// HandleDeleteWebhook soft-deletes a webhook config.
// DELETE /admin/webhooks/{id}
func HandleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid webhook id")
		return
	}

	if err := query.DeleteWebhookConfig(db.DB, id); err != nil {
		responder.QueryError(w, err, "failed to delete webhook config")
		return
	}

	logAudit(r, "delete", "webhook", intPtr(id), nil)
	responder.New(w, nil, "webhook config deleted")
}

// HandleTestWebhook sends a test event to a specific webhook.
// POST /admin/webhooks/{id}/test
func HandleTestWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid webhook id")
		return
	}

	cfg, err := query.GetWebhookConfig(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	// Fire a test event directly to this webhook
	webhooks.Fire("test", map[string]any{
		"webhook_id":   cfg.ID,
		"webhook_name": cfg.Name,
		"message":      "This is a test webhook delivery from Lattice.",
	})

	responder.New(w, map[string]any{"status": "sent"}, "test webhook dispatched")
}
