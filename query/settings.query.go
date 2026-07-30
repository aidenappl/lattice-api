package query

import (
	"fmt"

	"github.com/aidenappl/lattice-api/db"
)

func GetSetting(engine db.Queryable, key string) (string, error) {
	var value string
	err := engine.QueryRow("SELECT value FROM settings WHERE `key` = ?", key).Scan(&value)
	return value, err
}

func SetSetting(engine db.Queryable, key, value string) error {
	_, err := engine.Exec(
		"INSERT INTO settings (`key`, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = VALUES(value)",
		key, value,
	)
	return err
}

func DeleteSetting(engine db.Queryable, key string) error {
	_, err := engine.Exec("DELETE FROM settings WHERE `key` = ?", key)
	return err
}

// GetSettingsByPrefix returns all settings matching a prefix
func GetSettingsByPrefix(engine db.Queryable, prefix string) (map[string]string, error) {
	rows, err := engine.Query(fmt.Sprintf("SELECT `key`, value FROM settings WHERE `key` LIKE ? LIMIT %d", db.MAX_LIMIT), prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		result[k] = v
	}
	return result, rows.Err()
}

// DeleteSettingExisted deletes a key and reports whether a row was actually
// removed.
//
// ─────────────────────────────────────────────────────────────────────────────
// ⚠️ THIS EXISTS TO MAKE SSO STATE CONSUMPTION ATOMIC, AND THE RETURN VALUE IS
// THE WHOLE POINT.
//
// The SSO callback must accept a `state` at most once — a state that can be
// consumed twice is a state an attacker can replay by capturing the callback URL,
// letting the real one complete, then replaying it.
//
// `DeleteSetting` cannot express that: it reports success whether or not a row
// was there, so two concurrent callbacks would both "succeed" and both proceed.
// This variant returns RowsAffected, and MariaDB's row lock guarantees exactly
// one of N concurrent callers sees true. The caller treats winning the DELETE —
// not having read the row — as authorisation to use it.
//
// Do not "simplify" this into a SELECT followed by DeleteSetting. That
// reintroduces the exact window the design closes.
// ─────────────────────────────────────────────────────────────────────────────
func DeleteSettingExisted(engine db.Queryable, key string) (bool, error) {
	res, err := engine.Exec("DELETE FROM settings WHERE `key` = ?", key)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
