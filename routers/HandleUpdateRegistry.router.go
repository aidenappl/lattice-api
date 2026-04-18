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

func HandleUpdateRegistry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid registry id")
		return
	}

	var body struct {
		Name     *string `json:"name"`
		URL      *string `json:"url"`
		Type     *string `json:"type"`
		Username *string `json:"username"`
		Password *string `json:"password"`
		Active   *bool   `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	reg, err := query.UpdateRegistry(db.DB, id, query.UpdateRegistryRequest{
		Name:     body.Name,
		URL:      body.URL,
		Type:     body.Type,
		Username: body.Username,
		Password: body.Password,
		Active:   body.Active,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to update registry")
		return
	}

	responder.New(w, reg, "registry updated")
}
