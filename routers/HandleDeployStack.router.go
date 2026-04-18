package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/gorilla/mux"
)

type DeployHandler struct {
	WorkerHub *socket.WorkerHub
	AdminHub  *socket.AdminHub
}

func (h *DeployHandler) HandleDeployStack(w http.ResponseWriter, r *http.Request) {
	stackID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid stack id")
		return
	}

	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	stack, err := query.GetStackByID(db.DB, stackID)
	if err != nil {
		responder.NotFound(w)
		return
	}

	deployment, err := query.CreateDeployment(db.DB, query.CreateDeploymentRequest{
		StackID:     stack.ID,
		Strategy:    stack.DeploymentStrategy,
		TriggeredBy: &user.ID,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create deployment")
		return
	}

	deployingStatus := "deploying"
	_, err = query.UpdateStack(db.DB, stack.ID, query.UpdateStackRequest{
		Status: &deployingStatus,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to update stack status")
		return
	}

	responder.NewCreated(w, deployment, "deployment created")
}
