package routers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

// HandleListTemplates returns all active templates.
// GET /admin/templates
func HandleListTemplates(w http.ResponseWriter, r *http.Request) {
	templates, err := query.ListTemplates(db.DB)
	if err != nil {
		responder.QueryError(w, err, "failed to list templates")
		return
	}

	responder.New(w, templates, "templates retrieved")
}

// HandleCreateTemplate creates a new template.
// POST /admin/templates
func HandleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
		Config      string  `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Name == "" {
		responder.MissingBodyFields(w, "name")
		return
	}
	if body.Config == "" {
		responder.MissingBodyFields(w, "config")
		return
	}

	var createdBy *int
	if user, ok := middleware.GetUserFromContext(r.Context()); ok && user != nil {
		createdBy = &user.ID
	}

	tmpl, err := query.CreateTemplate(db.DB, query.CreateTemplateRequest{
		Name:        body.Name,
		Description: body.Description,
		Config:      body.Config,
		CreatedBy:   createdBy,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create template")
		return
	}

	logAudit(r, "create", "template", intPtr(tmpl.ID), strPtr(tmpl.Name))
	responder.NewCreated(w, tmpl, "template created")
}

// HandleDeleteTemplate soft-deletes a template.
// DELETE /admin/templates/{id}
func HandleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid template id")
		return
	}

	if err := query.DeleteTemplate(db.DB, id); err != nil {
		responder.QueryError(w, err, "failed to delete template")
		return
	}

	logAudit(r, "delete", "template", intPtr(id), nil)
	responder.New(w, nil, "template deleted")
}

// HandleCreateTemplateFromStack creates a template from an existing stack.
// POST /admin/stacks/{id}/save-template
func HandleCreateTemplateFromStack(w http.ResponseWriter, r *http.Request) {
	stackID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid stack id")
		return
	}

	var body struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Name == "" {
		responder.MissingBodyFields(w, "name")
		return
	}

	stack, err := query.GetStackByID(db.DB, stackID)
	if err != nil {
		responder.NotFound(w)
		return
	}

	containers, err := query.ListContainersByStack(db.DB, stackID)
	if err != nil {
		responder.QueryError(w, err, "failed to list containers")
		return
	}

	export := map[string]any{
		"version": "1",
		"stack": map[string]any{
			"name":                  stack.Name,
			"description":           stack.Description,
			"deployment_strategy":   stack.DeploymentStrategy,
			"auto_deploy":           stack.AutoDeploy,
			"env_vars":              stack.EnvVars,
			"compose_yaml":          stack.ComposeYAML,
			"placement_constraints": stack.PlacementConstraints,
		},
		"containers": formatContainersForExport(*containers),
	}

	configJSON, err := json.Marshal(export)
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to marshal config")
		return
	}

	var createdBy *int
	if user, ok := middleware.GetUserFromContext(r.Context()); ok && user != nil {
		createdBy = &user.ID
	}

	tmpl, err := query.CreateTemplate(db.DB, query.CreateTemplateRequest{
		Name:        body.Name,
		Description: body.Description,
		Config:      string(configJSON),
		CreatedBy:   createdBy,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create template")
		return
	}

	logAudit(r, "create", "template", intPtr(tmpl.ID), strPtr(tmpl.Name))
	responder.NewCreated(w, tmpl, "template created from stack")
}
