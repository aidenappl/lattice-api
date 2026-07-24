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

	// SSRF guard: if the URL is being changed, it must remain an external HTTPS URL.
	if body.URL != nil {
		if err := tools.ValidateExternalURL(*body.URL); err != nil {
			responder.SendError(w, http.StatusBadRequest, "invalid registry url: "+err.Error())
			return
		}
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

	logAudit(r, "update", "registry", intPtr(id), nil)
	responder.New(w, reg, "registry updated")
}
