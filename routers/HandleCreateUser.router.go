package routers

import (
	"encoding/json"
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/tools"
)

func HandleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string  `json:"email"`
		Name     *string `json:"name"`
		Password string  `json:"password"`
		Role     string  `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Email == "" {
		responder.MissingBodyFields(w, "email")
		return
	}
	if body.Password == "" {
		responder.MissingBodyFields(w, "password")
		return
	}
	if body.Role == "" {
		body.Role = "viewer"
	}

	hash, err := tools.HashPassword(body.Password)
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to hash password", err)
		return
	}

	user, err := query.CreateUser(db.DB, query.CreateUserRequest{
		Email:        body.Email,
		Name:         body.Name,
		AuthType:     "local",
		PasswordHash: &hash,
		Role:         body.Role,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create user")
		return
	}

	responder.NewCreated(w, user, "user created")
}
