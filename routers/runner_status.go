package routers

import (
	"sync"
	"time"
)

// runnerStatusFreshness is how long a runner's deployment_status reply is
// trusted. The monitor pings every deployPingInterval, so a reply older than
// this means the runner has stopped answering.
const runnerStatusFreshness = 2 * time.Minute

type runnerStatus struct {
	inProgress bool
	at         time.Time
}

// runnerStatuses is the latest deployment_status reply per deployment, keyed
// by deployment id.
var runnerStatuses = struct {
	sync.Mutex
	m map[int]runnerStatus
}{m: make(map[int]runnerStatus)}

// RecordRunnerDeploymentStatus keeps a runner's answer to a deployment_ping:
// whether it still has the deployment in progress. Stale entries are pruned
// on every write, so the map holds at most the deploys pinged recently.
func RecordRunnerDeploymentStatus(deploymentID int, inProgress bool) {
	recordRunnerDeploymentStatusAt(deploymentID, inProgress, time.Now())
}

func recordRunnerDeploymentStatusAt(deploymentID int, inProgress bool, now time.Time) {
	runnerStatuses.Lock()
	defer runnerStatuses.Unlock()
	for id, s := range runnerStatuses.m {
		if now.Sub(s.at) > runnerStatusFreshness {
			delete(runnerStatuses.m, id)
		}
	}
	runnerStatuses.m[deploymentID] = runnerStatus{inProgress: inProgress, at: now}
}

// runnerHasDeploymentInProgress reports whether the runner's latest reply,
// within runnerStatusFreshness, said it is still executing the deployment. A
// disconnected worker, a stale reply, or an idle answer is false.
func runnerHasDeploymentInProgress(deploymentID int, now time.Time) bool {
	runnerStatuses.Lock()
	defer runnerStatuses.Unlock()
	s, ok := runnerStatuses.m[deploymentID]
	return ok && s.inProgress && now.Sub(s.at) <= runnerStatusFreshness
}

// forgetRunnerDeploymentStatus drops a deployment's entry once it is settled.
func forgetRunnerDeploymentStatus(deploymentID int) {
	runnerStatuses.Lock()
	defer runnerStatuses.Unlock()
	delete(runnerStatuses.m, deploymentID)
}
