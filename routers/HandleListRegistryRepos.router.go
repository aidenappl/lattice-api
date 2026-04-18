package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/registry"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/gorilla/mux"
)

// HandleListRegistryRepos lists all repositories in a registry.
// GET /admin/registries/{id}/repositories
func HandleListRegistryRepos(w http.ResponseWriter, r *http.Request) {
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
	repos, err := client.ListRepositories()
	if err != nil {
		responder.SendError(w, http.StatusBadGateway, "failed to list repositories: "+err.Error())
		return
	}

	responder.New(w, repos, "repositories retrieved")
}

// HandleListRegistryTags lists all tags for a repository in a registry.
// GET /admin/registries/{id}/repositories/{repo}/tags
// The repo name can include slashes (e.g., "library/nginx"), so we use
// the remainder of the path after /repositories/.
func HandleListRegistryTags(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid registry id")
		return
	}

	repo := r.URL.Query().Get("repo")
	if repo == "" {
		responder.MissingBodyFields(w, "repo (query param)")
		return
	}

	reg, err := query.GetRegistryByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	client := registryClient(reg)
	tags, err := client.ListTags(repo)
	if err != nil {
		responder.SendError(w, http.StatusBadGateway, "failed to list tags: "+err.Error())
		return
	}

	responder.New(w, tags, "tags retrieved")
}

// registryClient builds a registry.Client from a stored registry record.
func registryClient(reg *structs.Registry) *registry.Client {
	username := ""
	password := ""
	if reg.Username != nil {
		username = *reg.Username
	}
	if reg.Password != nil {
		password = *reg.Password
	}
	return registry.NewClient(reg.URL, username, password)
}
