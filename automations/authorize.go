package automations

import (
	"errors"
	"fmt"

	"github.com/aidenappl/lattice-api/structs"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE AUTHORISATION MODEL
//
// An automation carries an identity — automations.run_as_user_id — and every
// run is authorised against that identity AT RUN TIME, before its first side
// effect.
//
// Why at run time rather than at creation time: a webhook token that can
// redeploy anything is worse than the per-stack deploy token it replaces, and a
// check made once at creation is a check that never happens again. Checked on
// every run, an automation can never do more than its identity can do TODAY:
//
//   - deactivate or delete the user → every automation acting as them fails on
//     its next firing, with the reason named in the run history;
//   - demote them → the steps their new role no longer allows fail the same way.
//
// Revoking a person revokes their automations, with no second list to remember.
//
// Why the identity is RE-BOUND on edit and enable rather than frozen at
// creation: if an edit kept the creator's identity, an editor could add an
// admin-only step to an admin's automation and have it run with the admin's
// authority. So the identity is always whoever last decided what the automation
// does — create, an edit of its trigger or actions, or enable — and that person
// must be able to run every step at the moment they save. Disable and delete
// only ever reduce what happens, so they do not re-bind. A manual "run now" is
// checked against BOTH the person pressing it and the run-as identity.
//
// Required roles mirror the routes a person would otherwise use, so an
// automation grants nothing a click could not:
//
//   - redeploy_container → editor   POST /containers/{id}/recreate is RequireEditor
//   - http_request       → admin    outbound webhooks are RequireAdmin, and this is
//                                   outbound HTTP made from inside the control plane
//   - webhook trigger    → admin    it holds a bearer credential; creating a deploy
//                                   token is RequireAdmin
//   - schedule trigger   → editor
//
// Known gap, recorded rather than hidden: an SSO user whose grant is revoked at
// the identity provider has their Lattice tokens revoked (tokens_revoked_at) but
// stays `active`. tokens_revoked_at cannot be consulted here, because an
// ordinary logout stamps it too (HandleLogout) and logging out must not disable
// anyone's automations. Such a user's automations keep running until an admin
// deactivates them in Lattice. See AGENTS.md → Automations.
// ─────────────────────────────────────────────────────────────────────────────

const (
	roleAdmin  = "admin"
	roleEditor = "editor"
	roleViewer = "viewer"
)

// roleRank orders the roles that can act at all. `pending` and anything unknown
// rank 0 and are refused outright.
var roleRank = map[string]int{roleViewer: 1, roleEditor: 2, roleAdmin: 3}

func roleAllows(have, need string) bool {
	rank := roleRank[have]
	return rank > 0 && rank >= roleRank[need]
}

// triggerRole is the role needed to be the identity of an automation with this
// trigger type.
func triggerRole(t structs.AutomationTriggerType) (string, error) {
	switch t {
	case structs.AutomationTriggerWebhook:
		return roleAdmin, nil
	case structs.AutomationTriggerSchedule:
		return roleEditor, nil
	default:
		return "", fmt.Errorf("unknown trigger type %q", t)
	}
}

// Authorise reports why user may NOT be the identity of an automation with this
// trigger and these actions, or nil if they may.
//
// The same function answers both questions the model asks — "may this person
// save this?" and "may this run proceed?" — so the two can never disagree.
func Authorise(user *structs.User, trigger structs.AutomationTrigger, actions []structs.AutomationAction) error {
	if user == nil {
		return errors.New("there is no user to act as")
	}
	who := describeUser(user)
	if !user.Active {
		return fmt.Errorf("%s is deactivated", who)
	}
	if roleRank[user.Role] == 0 {
		return fmt.Errorf("%s has role %q, which cannot run automations", who, user.Role)
	}

	need, err := triggerRole(trigger.Type)
	if err != nil {
		return err
	}
	if !roleAllows(user.Role, need) {
		return fmt.Errorf("%s triggers require the %s role; %s is %s", trigger.Type, need, who, user.Role)
	}

	for i, action := range actions {
		kind, err := kindFor(action.Type)
		if err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		if need := kind.requiredRole(); !roleAllows(user.Role, need) {
			return fmt.Errorf("step %d (%s) requires the %s role; %s is %s", i+1, action.Type, need, who, user.Role)
		}
	}
	return nil
}

func describeUser(u *structs.User) string {
	if u == nil {
		return "no user"
	}
	return fmt.Sprintf("user #%d (%s)", u.ID, u.Email)
}
