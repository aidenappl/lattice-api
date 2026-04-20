package routers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	var body struct {
		Name   *string `json:"name"`
		Role   *string `json:"role"`
		Active *bool   `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	user, err := query.UpdateUser(db.DB, id, query.UpdateUserRequest{
		Name:   body.Name,
		Role:   body.Role,
		Active: body.Active,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to update user")
		return
	}

	logAudit(r, "update", "user", intPtr(id), nil)
	responder.New(w, user, "user updated")
}
