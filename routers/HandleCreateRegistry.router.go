package routers

import (
	"encoding/json"
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/registry"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleCreateRegistry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string  `json:"name"`
		URL      string  `json:"url"`
		Type     string  `json:"type"`
		Username *string `json:"username"`
		Password *string `json:"password"`
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
	if body.Type == "" {
		responder.MissingBodyFields(w, "type")
		return
	}

	// Validate connectivity if credentials are provided
	if body.Username != nil && body.Password != nil {
		client := registry.NewClient(body.URL, *body.Username, *body.Password)
		if err := client.Ping(); err != nil {
			responder.SendError(w, http.StatusBadRequest, "registry connection failed: "+err.Error())
			return
		}
	}

	reg, err := query.CreateRegistry(db.DB, query.CreateRegistryRequest{
		Name:     body.Name,
		URL:      body.URL,
		Type:     body.Type,
		Username: body.Username,
		Password: body.Password,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create registry")
		return
	}

	logAudit(r, "create", "registry", intPtr(reg.ID), strPtr(reg.Name))
	responder.NewCreated(w, reg, "registry created")
}
