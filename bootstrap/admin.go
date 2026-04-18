package bootstrap

import (
	"fmt"
	"log"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/tools"
)

// EnsureAdminUser checks if any user exists in the database.
// If no users exist and LATTICE_ADMIN_EMAIL + LATTICE_ADMIN_PASSWORD are set,
// it creates an initial local admin user. This enables bootstrapping Lattice
// before Forta is available.
func EnsureAdminUser(engine db.Queryable) error {
	count, err := query.CountUsers(engine)
	if err != nil {
		return fmt.Errorf("failed to count users: %w", err)
	}

	if count > 0 {
		return nil
	}

	if env.LatticeAdminEmail == "" || env.LatticeAdminPassword == "" {
		log.Println("⚠️  No users exist and LATTICE_ADMIN_EMAIL/LATTICE_ADMIN_PASSWORD not set. No admin user created.")
		return nil
	}

	hash, err := tools.HashPassword(env.LatticeAdminPassword)
	if err != nil {
		return fmt.Errorf("failed to hash admin password: %w", err)
	}

	_, err = query.CreateUser(engine, query.CreateUserRequest{
		Email:        env.LatticeAdminEmail,
		Name:         strPtr("Admin"),
		AuthType:     "local",
		PasswordHash: &hash,
		Role:         "admin",
	})
	if err != nil {
		return fmt.Errorf("failed to create admin user: %w", err)
	}

	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║  Bootstrap admin user created successfully.                  ║")
	fmt.Printf("║  Email: %-52s║\n", env.LatticeAdminEmail)
	fmt.Println("║  Role:  admin                                                ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()

	return nil
}

func strPtr(s string) *string {
	return &s
}
