package routers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/registry"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

// HandleTestRegistry tests connectivity and auth for an existing registry.
// POST /admin/registries/{id}/test
func HandleTestRegistry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid registry id")
		return
	}

	reg, err := query.GetRegistryByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	client := registryClient(reg)
	if err := client.Ping(); err != nil {
		responder.SendError(w, http.StatusBadGateway, "registry connection failed: "+err.Error())
		return
	}

	responder.New(w, map[string]any{"status": "ok"}, "registry connection successful")
}

// HandleTestRegistryInline tests connectivity for credentials provided inline (before saving).
// POST /admin/registries/test
func HandleTestRegistryInline(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.URL == "" {
		responder.MissingBodyFields(w, "url")
		return
	}

	client := registry.NewClient(body.URL, body.Username, body.Password)
	if err := client.Ping(); err != nil {
		responder.SendError(w, http.StatusBadGateway, "registry connection failed: "+err.Error())
		return
	}

	responder.New(w, map[string]any{"status": "ok"}, "registry connection successful")
}
