package routers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/structs"
)

// BackupPosture is a database's standing against the 3-2-1 rule: three copies,
// on two kinds of media, one of them off-site.
//
// Every axis reports what is *true*, not what was configured. A destination that
// was set up but has never produced a fresh snapshot is not a copy, and a
// destination whose locality nobody has confirmed is not off-site. The failure
// this guards against is a dashboard that reassures you — false confidence about
// backups is worse than no dashboard, because it stops you looking.
type BackupPosture struct {
	Copies    int  `json:"copies"`
	CopiesOK  bool `json:"copies_ok"`
	Media     int  `json:"media"`
	MediaOK   bool `json:"media_ok"`
	Offsite   int  `json:"offsite"`
	OffsiteOK bool `json:"offsite_ok"`
	Score     int  `json:"score"`
	// Detail explains each axis in the operator's terms.
	Detail []string `json:"detail"`
	// Warnings call out things that look like protection and are not.
	Warnings []string `json:"warnings"`
}

// postureFreshnessWindow is how recent a snapshot must be to count as a copy.
//
// Staleness disqualifies deliberately: three copies where two are six weeks old
// is not three copies, and counting them would turn the whole indicator into
// decoration.
const postureFreshnessWindow = 8 * 24 * time.Hour

// HandleGetDatabaseBackupPosture reports a database's 3-2-1 standing.
func HandleGetDatabaseBackupPosture(w http.ResponseWriter, r *http.Request) {
	instance, ok := loadInstanceForObservability(w, r)
	if !ok {
		return
	}

	posture := computeBackupPosture(instance)
	responder.New(w, posture, "backup posture retrieved")
}

func computeBackupPosture(instance *structs.DatabaseInstance) BackupPosture {
	p := BackupPosture{
		// The live data volume is the first copy — the one you are protecting.
		Copies: 1,
		Detail: []string{"the live data volume on the worker"},
	}

	cutoff := time.Now().UTC().Add(-postureFreshnessWindow)

	// Count *replicas*, not snapshots: the question 3-2-1 asks is "does a copy
	// exist on that destination", which a snapshot row alone cannot answer once
	// a snapshot can live in more than one place.
	freshByDestination, err := query.FreshReplicasByDestination(db.DB, instance.ID, cutoff)
	if err != nil {
		p.Warnings = append(p.Warnings, "could not read this database's snapshot copies")
		return finalisePosture(p)
	}

	if len(freshByDestination) == 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf(
			"no successful snapshot in the last %d days — this database has exactly one copy",
			int(postureFreshnessWindow.Hours()/24)))
		return finalisePosture(p)
	}

	seenTypes := map[string]bool{}
	for destID, count := range freshByDestination {
		dest, err := query.GetBackupDestinationByID(db.DB, destID)
		if err != nil {
			continue
		}

		p.Copies += count
		seenTypes[dest.Type] = true

		switch dest.Locality {
		case structs.LocalityOffsite:
			p.Offsite++
			p.Detail = append(p.Detail, fmt.Sprintf("%d snapshot(s) on %q (off-site)", count, dest.Name))
		case structs.LocalitySameHost:
			p.Detail = append(p.Detail, fmt.Sprintf("%d snapshot(s) on %q (same host as the database)", count, dest.Name))
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"%q shares a machine with this database: one hardware failure takes the data and its backups together. "+
					"Object-lock guarantees do not apply — immutability enforced by a process whose filesystem you can reach is not immutability",
				dest.Name))
		case structs.LocalitySameFleet:
			p.Detail = append(p.Detail, fmt.Sprintf("%d snapshot(s) on %q (same fleet)", count, dest.Name))
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"%q is in the same failure domain as this database — a site-level loss takes both", dest.Name))
		default:
			p.Detail = append(p.Detail, fmt.Sprintf("%d snapshot(s) on %q (locality unconfirmed)", count, dest.Name))
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"nobody has confirmed where %q physically lives, so it is not counted as off-site. "+
					"Lattice cannot infer this: an S3 endpoint may be a bucket on this very worker or one in another country",
				dest.Name))
		}
	}

	p.Media = len(seenTypes)
	return finalisePosture(p)
}

func finalisePosture(p BackupPosture) BackupPosture {
	p.CopiesOK = p.Copies >= 3
	p.MediaOK = p.Media >= 2
	p.OffsiteOK = p.Offsite >= 1

	for _, ok := range []bool{p.CopiesOK, p.MediaOK, p.OffsiteOK} {
		if ok {
			p.Score++
		}
	}
	if p.Detail == nil {
		p.Detail = []string{}
	}
	if p.Warnings == nil {
		p.Warnings = []string{}
	}
	return p
}
