package routers

import (
	"encoding/json"
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleCreateRegistry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name             string  `json:"name"`
		URL              string  `json:"url"`
		Type             string  `json:"type"`
		KeyringSecretKey *string `json:"keyring_secret_key"`
		AuthConfig       *string `json:"auth_config"`
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

	registry, err := query.CreateRegistry(db.DB, query.CreateRegistryRequest{
		Name:             body.Name,
		URL:              body.URL,
		Type:             body.Type,
		KeyringSecretKey: body.KeyringSecretKey,
		AuthConfig:       body.AuthConfig,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create registry")
		return
	}

	responder.NewCreated(w, registry, "registry created")
}
