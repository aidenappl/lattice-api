package routers

import (
	"fmt"
	"strings"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
)

// settingWrite is one key of a settings form.
type settingWrite struct {
	key   string
	value string
}

// settingsForm collects the keys a settings request changes, so they can be
// written together — all or none — once the whole request has been validated.
type settingsForm []settingWrite

// str stages a trimmed string setting if it was provided.
func (f *settingsForm) str(key string, val *string) {
	if val != nil {
		*f = append(*f, settingWrite{key, strings.TrimSpace(*val)})
	}
}

// boolean stages a bool setting if it was provided.
func (f *settingsForm) boolean(key string, val *bool) {
	if val != nil {
		v := "false"
		if *val {
			v = "true"
		}
		*f = append(*f, settingWrite{key, v})
	}
}

// raw stages a setting as given.
func (f *settingsForm) raw(key, value string) {
	*f = append(*f, settingWrite{key, value})
}

// saveSettings writes every staged setting in one transaction. A settings save
// that half-applies would leave, say, a new client id beside an old secret —
// and one that reported success without writing is worse. Swappable in tests.
var saveSettings = func(form settingsForm) error {
	if len(form) == 0 {
		return nil
	}
	tx, err := db.BeginTx()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	for _, s := range form {
		if err := query.SetSetting(tx, s.key, s.value); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("set %s: %w", s.key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
