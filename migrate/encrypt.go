// Package migrate holds one-off data migrations run via the `lattice-api
// migrate-encrypt` subcommand rather than the normal server boot.
package migrate

import (
	"database/sql"
	"fmt"

	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
)

// EncryptTarget is one (table, id-column, value-column) location holding a
// secret that lives encrypted at rest via crypto.Encrypt.
type EncryptTarget struct {
	Table string
	IDCol string
	Value string
}

// ColumnTargets are the dedicated secret columns across the schema. Keep this in
// sync with every crypto.Encrypt/Decrypt call site that reads/writes a column.
var ColumnTargets = []EncryptTarget{
	{Table: "registries", IDCol: "id", Value: "password"},
	{Table: "global_env_vars", IDCol: "id", Value: "encrypted_value"},
	{Table: "database_instances", IDCol: "id", Value: "root_password"},
	{Table: "database_instances", IDCol: "id", Value: "password"},
	{Table: "backup_destinations", IDCol: "id", Value: "config"},
}

// SettingKeys are secrets stored as rows in the settings(`key`, value) table.
var SettingKeys = []string{"sso.client_secret", "smtp.password"}

// needsEncryption reports whether a stored value is plaintext and must be
// encrypted. A value that Decrypts cleanly is already ciphertext — AES-GCM is
// authenticated, so a plaintext value surviving Open() as valid is
// cryptographically negligible — and is left untouched. This is what makes the
// migration idempotent and safe to re-run.
func needsEncryption(raw string) bool {
	if raw == "" {
		return false
	}
	if _, err := crypto.Decrypt(raw); err == nil {
		return false // already valid ciphertext
	}
	return true // decrypt failed -> treat as plaintext
}

// Report counts what happened (or would happen, in dry-run) for a target.
type Report struct {
	Encrypted        int
	AlreadyEncrypted int
	Empty            int
}

func (r *Report) add(o Report) {
	r.Encrypted += o.Encrypted
	r.AlreadyEncrypted += o.AlreadyEncrypted
	r.Empty += o.Empty
}

// RunEncrypt encrypts every plaintext secret value in a single transaction.
// It is idempotent: already-encrypted values are skipped. In dry-run mode it
// reports counts and rolls back without writing.
func RunEncrypt(dryRun bool) error {
	// Guard: if the key is not active, Decrypt is passthrough and every value
	// would look "already encrypted" — a silent no-op that would then let the
	// server flip encryption on with plaintext rows still in place. Refuse.
	if !crypto.IsConfigured() {
		return fmt.Errorf("ENCRYPTION_KEY is not set/active — refusing to run (would treat every value as already-encrypted)")
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless we Commit

	var total Report
	for _, t := range ColumnTargets {
		rep, err := migrateColumn(tx, t, dryRun)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", t.Table, t.Value, err)
		}
		fmt.Printf("  %-20s %-16s  encrypt=%d already=%d empty=%d\n", t.Table, t.Value, rep.Encrypted, rep.AlreadyEncrypted, rep.Empty)
		total.add(rep)
	}

	rep, err := migrateSettings(tx, dryRun)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	fmt.Printf("  %-20s %-16s  encrypt=%d already=%d empty=%d\n", "settings", "(keys)", rep.Encrypted, rep.AlreadyEncrypted, rep.Empty)
	total.add(rep)

	fmt.Printf("\nTOTAL  encrypt=%d already=%d empty=%d  (dry-run=%v)\n",
		total.Encrypted, total.AlreadyEncrypted, total.Empty, dryRun)

	if dryRun {
		return nil // defer rolls back
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func migrateColumn(tx *sql.Tx, t EncryptTarget, dryRun bool) (Report, error) {
	var rep Report

	rows, err := tx.Query(fmt.Sprintf("SELECT `%s`, `%s` FROM `%s`", t.IDCol, t.Value, t.Table))
	if err != nil {
		return rep, err
	}
	type work struct {
		id  int64
		val string
	}
	var todo []work
	for rows.Next() {
		var id int64
		var val sql.NullString
		if err := rows.Scan(&id, &val); err != nil {
			rows.Close()
			return rep, err
		}
		switch {
		case !val.Valid || val.String == "":
			rep.Empty++
		case !needsEncryption(val.String):
			rep.AlreadyEncrypted++
		default:
			todo = append(todo, work{id: id, val: val.String})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return rep, err
	}
	rows.Close() // must close before issuing UPDATEs on the same tx

	for _, w := range todo {
		enc, err := crypto.Encrypt(w.val)
		if err != nil {
			return rep, fmt.Errorf("encrypt id=%d: %w", w.id, err)
		}
		if !dryRun {
			if _, err := tx.Exec(
				fmt.Sprintf("UPDATE `%s` SET `%s` = ? WHERE `%s` = ?", t.Table, t.Value, t.IDCol),
				enc, w.id,
			); err != nil {
				return rep, fmt.Errorf("update id=%d: %w", w.id, err)
			}
		}
		rep.Encrypted++
	}
	return rep, nil
}

func migrateSettings(tx *sql.Tx, dryRun bool) (Report, error) {
	var rep Report
	for _, key := range SettingKeys {
		var val sql.NullString
		err := tx.QueryRow("SELECT value FROM settings WHERE `key` = ?", key).Scan(&val)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return rep, fmt.Errorf("select %s: %w", key, err)
		}
		switch {
		case !val.Valid || val.String == "":
			rep.Empty++
			continue
		case !needsEncryption(val.String):
			rep.AlreadyEncrypted++
			continue
		}
		enc, err := crypto.Encrypt(val.String)
		if err != nil {
			return rep, fmt.Errorf("encrypt %s: %w", key, err)
		}
		if !dryRun {
			if _, err := tx.Exec("UPDATE settings SET value = ? WHERE `key` = ?", enc, key); err != nil {
				return rep, fmt.Errorf("update %s: %w", key, err)
			}
		}
		rep.Encrypted++
	}
	return rep, nil
}
