package routers

import (
	"strings"
	"testing"
)

// The posture indicator exists to be *honest*. A backup dashboard that reassures
// you wrongly is worse than none, because it stops you looking — so these tests
// pin the conservative readings rather than the flattering ones.

func TestPostureWithNoBackupsIsOneCopy(t *testing.T) {
	p := finalisePosture(BackupPosture{Copies: 1})

	if p.CopiesOK || p.MediaOK || p.OffsiteOK {
		t.Errorf("a database with no backups must fail every axis, got %+v", p)
	}
	if p.Score != 0 {
		t.Errorf("score = %d, want 0", p.Score)
	}
}

func TestUnknownLocalityIsNeverOffsite(t *testing.T) {
	// One destination, locality unconfirmed: copies may pass, off-site must not.
	p := finalisePosture(BackupPosture{Copies: 3, Media: 1, Offsite: 0})

	if p.OffsiteOK {
		t.Error("an unconfirmed destination was counted as off-site — Lattice cannot infer locality and must not guess")
	}
	if !p.CopiesOK {
		t.Error("three copies should satisfy the copies axis")
	}
	if p.Score != 1 {
		t.Errorf("score = %d, want 1 (copies only)", p.Score)
	}
}

func TestFullComplianceScoresThree(t *testing.T) {
	p := finalisePosture(BackupPosture{Copies: 3, Media: 2, Offsite: 1})
	if p.Score != 3 || !p.CopiesOK || !p.MediaOK || !p.OffsiteOK {
		t.Errorf("expected full compliance, got %+v", p)
	}
}

func TestTwoCopiesIsNotThree(t *testing.T) {
	// The live volume plus a single snapshot is two copies, not three. Rounding
	// that up would be the exact dishonesty this indicator exists to prevent.
	p := finalisePosture(BackupPosture{Copies: 2, Media: 1, Offsite: 0})
	if p.CopiesOK {
		t.Error("two copies must not satisfy the 3-copy axis")
	}
}

func TestFreshnessWindowIsBounded(t *testing.T) {
	// Staleness has to disqualify: three copies where two are six weeks old is
	// not three copies.
	if postureFreshnessWindow <= 0 {
		t.Fatal("a copy must have a freshness bound, or the indicator is decoration")
	}
	if postureFreshnessWindow > 30*24*60*60*1e9 {
		t.Error("freshness window is so wide that long-dead backups still count")
	}
}

// Empty slices, not nil, so the JSON carries [] rather than null and the UI has
// nothing to special-case.
func TestPostureAlwaysCarriesSlices(t *testing.T) {
	p := finalisePosture(BackupPosture{Copies: 1})
	if p.Detail == nil || p.Warnings == nil {
		t.Error("detail and warnings must serialise as [] rather than null")
	}
}

func TestSameHostWarningNamesTheRealRisk(t *testing.T) {
	// This is the OpenBucket-on-the-same-worker case. The warning has to say why
	// object lock does not save you, because that is the non-obvious half.
	p := BackupPosture{}
	p.Warnings = append(p.Warnings, "\"local\" shares a machine with this database: one hardware failure takes the data and its backups together. "+
		"Object-lock guarantees do not apply — immutability enforced by a process whose filesystem you can reach is not immutability")

	if !strings.Contains(p.Warnings[0], "Object-lock") {
		t.Error("the same-host warning should explain why immutability guarantees are cosmetic there")
	}
}
