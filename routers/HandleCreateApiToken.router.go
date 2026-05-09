package routers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/tools"
)

func HandleCreateApiToken(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body struct {
		Name      string  `json:"name"`
		Scopes    *string `json:"scopes"`
		ExpiresIn string  `json:"expires_in"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Name == "" {
		responder.MissingBodyFields(w, "name")
		return
	}

	var expiresAt *time.Time
	switch strings.ToLower(body.ExpiresIn) {
	case "never", "":
		if body.ExpiresIn == "" {
			t := time.Now().Add(90 * 24 * time.Hour)
			expiresAt = &t
		}
	case "30d":
		t := time.Now().Add(30 * 24 * time.Hour)
		expiresAt = &t
	case "90d":
		t := time.Now().Add(90 * 24 * time.Hour)
		expiresAt = &t
	case "365d":
		t := time.Now().Add(365 * 24 * time.Hour)
		expiresAt = &t
	default:
		responder.SendError(w, http.StatusBadRequest, "invalid expires_in value, use: 30d, 90d, 365d, or never")
		return
	}

	plaintext, hash, err := tools.GenerateToken()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to generate token", err)
		return
	}

	token, err := query.CreateApiToken(db.DB, query.CreateApiTokenRequest{
		UserID:    user.ID,
		Name:      body.Name,
		TokenHash: hash,
		Scopes:    body.Scopes,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create api token")
		return
	}

	logAudit(r, "create", "api_token", intPtr(token.ID), strPtr(body.Name))
	responder.NewCreated(w, map[string]any{
		"token":        plaintext,
		"id":           token.ID,
		"user_id":      token.UserID,
		"name":         token.Name,
		"scopes":       token.Scopes,
		"expires_at":   token.ExpiresAt,
		"last_used_at": token.LastUsedAt,
		"active":       token.Active,
		"inserted_at":  token.InsertedAt,
		"updated_at":   token.UpdatedAt,
	}, "api token created")
}
