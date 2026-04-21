package routers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/tools"
	"github.com/gorilla/mux"
)

func HandleListDeployTokens(w http.ResponseWriter, r *http.Request) {
	stackID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid stack id")
		return
	}

	tokens, err := query.ListDeployTokensByStack(db.DB, stackID)
	if err != nil {
		responder.QueryError(w, err, "failed to list deploy tokens")
		return
	}

	responder.New(w, tokens, "deploy tokens retrieved")
}

func HandleCreateDeployToken(w http.ResponseWriter, r *http.Request) {
	stackID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid stack id")
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Name == "" {
		responder.MissingBodyFields(w, "name")
		return
	}

	plaintext, hash, err := tools.GenerateToken()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to generate token", err)
		return
	}

	token, err := query.CreateDeployToken(db.DB, query.CreateDeployTokenRequest{
		StackID:   stackID,
		Name:      body.Name,
		TokenHash: hash,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create deploy token")
		return
	}

	logAudit(r, "create", "deploy_token", intPtr(token.ID), strPtr(body.Name))
	responder.NewCreated(w, map[string]any{
		"token":        plaintext,
		"id":           token.ID,
		"stack_id":     token.StackID,
		"name":         token.Name,
		"last_used_at": token.LastUsedAt,
		"active":       token.Active,
		"inserted_at":  token.InsertedAt,
		"updated_at":   token.UpdatedAt,
	}, "deploy token created")
}

func HandleDeleteDeployToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid token id")
		return
	}

	if err := query.DeleteDeployToken(db.DB, id); err != nil {
		responder.QueryError(w, err, "failed to delete deploy token")
		return
	}

	logAudit(r, "delete", "deploy_token", intPtr(id), nil)
	responder.New(w, nil, "deploy token deleted")
}
