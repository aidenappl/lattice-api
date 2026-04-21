package routers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

// HandleListGlobalEnvVars returns all global environment variables.
// Secret values are decrypted for admin users and masked for non-admin users.
// GET /admin/env-vars
func HandleListGlobalEnvVars(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	vars, err := query.ListGlobalEnvVars(db.DB)
	if err != nil {
		responder.QueryError(w, err, "failed to list global env vars")
		return
	}

	isAdmin := user.Role == "admin"

	result := make([]map[string]any, 0, len(*vars))
	for _, gv := range *vars {
		decrypted, _ := crypto.Decrypt(gv.EncryptedValue)

		value := decrypted
		if gv.IsSecret && !isAdmin {
			value = "***"
		}

		result = append(result, map[string]any{
			"id":          gv.ID,
			"key":         gv.Key,
			"value":       value,
			"is_secret":   gv.IsSecret,
			"updated_at":  gv.UpdatedAt,
			"inserted_at": gv.InsertedAt,
		})
	}

	responder.New(w, result, "global env vars retrieved")
}

// HandleCreateGlobalEnvVar creates a new global environment variable.
// POST /admin/env-vars
func HandleCreateGlobalEnvVar(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key      string `json:"key"`
		Value    string `json:"value"`
		IsSecret bool   `json:"is_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Key == "" {
		responder.MissingBodyFields(w, "key")
		return
	}
	if body.Value == "" {
		responder.MissingBodyFields(w, "value")
		return
	}

	encrypted, err := crypto.Encrypt(body.Value)
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to encrypt value")
		return
	}

	gv, err := query.CreateGlobalEnvVar(db.DB, body.Key, encrypted, body.IsSecret)
	if err != nil {
		responder.QueryError(w, err, "failed to create global env var")
		return
	}

	// Return decrypted value in the response
	decrypted, _ := crypto.Decrypt(gv.EncryptedValue)
	result := map[string]any{
		"id":          gv.ID,
		"key":         gv.Key,
		"value":       decrypted,
		"is_secret":   gv.IsSecret,
		"updated_at":  gv.UpdatedAt,
		"inserted_at": gv.InsertedAt,
	}

	logAudit(r, "create", "global_env_var", intPtr(gv.ID), strPtr(gv.Key))
	responder.NewCreated(w, result, "global env var created")
}

// HandleUpdateGlobalEnvVar updates an existing global environment variable.
// PUT /admin/env-vars/{id}
func HandleUpdateGlobalEnvVar(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid env var id")
		return
	}

	var body struct {
		Value    *string `json:"value"`
		IsSecret *bool   `json:"is_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	// Fetch existing to get current values
	existing, err := query.GetGlobalEnvVar(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	encryptedValue := existing.EncryptedValue
	isSecret := existing.IsSecret

	if body.Value != nil {
		encryptedValue, err = crypto.Encrypt(*body.Value)
		if err != nil {
			responder.SendError(w, http.StatusInternalServerError, "failed to encrypt value")
			return
		}
	}
	if body.IsSecret != nil {
		isSecret = *body.IsSecret
	}

	if err := query.UpdateGlobalEnvVar(db.DB, id, encryptedValue, isSecret); err != nil {
		responder.QueryError(w, err, "failed to update global env var")
		return
	}

	logAudit(r, "update", "global_env_var", intPtr(id), nil)
	responder.New(w, nil, "global env var updated")
}

// HandleDeleteGlobalEnvVar deletes a global environment variable.
// DELETE /admin/env-vars/{id}
func HandleDeleteGlobalEnvVar(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid env var id")
		return
	}

	if err := query.DeleteGlobalEnvVar(db.DB, id); err != nil {
		responder.QueryError(w, err, "failed to delete global env var")
		return
	}

	logAudit(r, "delete", "global_env_var", intPtr(id), nil)
	responder.New(w, nil, "global env var deleted")
}
