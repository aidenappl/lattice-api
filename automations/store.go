package automations

import (
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/structs"
)

// Store is everything the executor reads and writes.
//
// It exists so the executor's guarantees — stop on first failure, the
// concurrency guard, authorisation at run time, no side effects for a bad token
// — can be tested without a database. DBStore is the only production
// implementation, and every method on it is a direct call to one query function;
// there is no logic here to drift from the queries.
type Store interface {
	GetAutomation(id int) (*structs.Automation, error)
	GetAutomationByWebhookHash(hash string) (*structs.Automation, error)
	TouchWebhook(automationID int) error

	CreateRun(req query.CreateAutomationRunRequest) (*structs.AutomationRun, bool, error)
	UpdateRun(id int, req query.UpdateAutomationRunRequest) error
	ListStuckRuns(cutoff time.Time) ([]structs.AutomationRun, error)

	ClaimAutomation(automationID, runID int, staleBefore time.Time) (bool, error)
	ReleaseAutomation(automationID, runID int) error

	GetUser(id int) (*structs.User, error)
	GetStack(id int) (*structs.Stack, error)
	ListStackContainers(stackID int) ([]structs.Container, error)

	CreateAuditLog(req query.CreateAuditLogRequest) error
}

// DBStore is the Store backed by the shared MariaDB pool.
type DBStore struct{}

func (DBStore) GetAutomation(id int) (*structs.Automation, error) {
	return query.GetAutomationByID(db.DB, id)
}

func (DBStore) GetAutomationByWebhookHash(hash string) (*structs.Automation, error) {
	return query.GetAutomationByWebhookHash(db.DB, hash)
}

func (DBStore) TouchWebhook(automationID int) error {
	return query.TouchAutomationWebhook(db.DB, automationID)
}

func (DBStore) CreateRun(req query.CreateAutomationRunRequest) (*structs.AutomationRun, bool, error) {
	return query.CreateAutomationRun(db.DB, req)
}

func (DBStore) UpdateRun(id int, req query.UpdateAutomationRunRequest) error {
	return query.UpdateAutomationRun(db.DB, id, req)
}

func (DBStore) ListStuckRuns(cutoff time.Time) ([]structs.AutomationRun, error) {
	return query.ListStuckAutomationRuns(db.DB, cutoff)
}

func (DBStore) ClaimAutomation(automationID, runID int, staleBefore time.Time) (bool, error) {
	return query.ClaimAutomationRun(db.DB, automationID, runID, staleBefore)
}

func (DBStore) ReleaseAutomation(automationID, runID int) error {
	return query.ReleaseAutomationRun(db.DB, automationID, runID)
}

func (DBStore) GetUser(id int) (*structs.User, error) {
	return query.GetUserByID(db.DB, id)
}

func (DBStore) GetStack(id int) (*structs.Stack, error) {
	return query.GetStackByID(db.DB, id)
}

func (DBStore) ListStackContainers(stackID int) ([]structs.Container, error) {
	containers, err := query.ListContainersByStack(db.DB, stackID)
	if err != nil || containers == nil {
		return nil, err
	}
	return *containers, nil
}

func (DBStore) CreateAuditLog(req query.CreateAuditLogRequest) error {
	return query.CreateAuditLog(db.DB, req)
}
