package structs

import "time"

type BackupDestination struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	// Locality is where this destination physically lives, asserted by an
	// operator rather than inferred. Lattice cannot tell an OpenBucket bucket on
	// the worker being backed up from a bucket in another country — both are
	// "s3" with a URL — so the default is `unknown` and posture reports it as
	// unknown rather than assuming anything reassuring.
	Locality   string    `json:"locality"`
	Config     *string   `json:"-"`
	Active     bool      `json:"active"`
	UpdatedAt  time.Time `json:"updated_at"`
	InsertedAt time.Time `json:"inserted_at"`
}

// Backup destination localities, worst to best.
const (
	// LocalityUnknown is the default and is never counted as off-site.
	LocalityUnknown = "unknown"
	// LocalitySameHost means it shares a machine with the database it backs up —
	// a disk failure takes both. Object-lock guarantees are cosmetic here: WORM
	// enforced by a process whose filesystem you can reach is not immutability.
	LocalitySameHost = "same_host"
	// LocalitySameFleet means a different machine, same failure domain.
	LocalitySameFleet = "same_fleet"
	// LocalityOffsite is the only value that satisfies the "1" in 3-2-1.
	LocalityOffsite = "offsite"
)

// IsValidLocality reports whether s is a known locality.
func IsValidLocality(s string) bool {
	switch s {
	case LocalityUnknown, LocalitySameHost, LocalitySameFleet, LocalityOffsite:
		return true
	}
	return false
}
