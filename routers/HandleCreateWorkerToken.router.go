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

func HandleCreateWorkerToken(w http.ResponseWriter, r *http.Request) {
	workerID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid worker id")
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

	token, err := query.CreateWorkerToken(db.DB, query.CreateWorkerTokenRequest{
		WorkerID:  workerID,
		Name:      body.Name,
		TokenHash: hash,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create worker token")
		return
	}

	responder.NewCreated(w, map[string]any{
		"token":     plaintext,
		"token_id":  token.ID,
		"worker_id": token.WorkerID,
		"name":      token.Name,
	}, "worker token created")
}
