package query

import (
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
	rows, err := engine.Query("SELECT `key`, value FROM settings WHERE `key` LIKE ?", prefix+"%")
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
