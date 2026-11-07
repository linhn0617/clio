package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/linhn0617/clio/internal/timeutil"
)

// RawRetentionKey holds the rolling raw_json retention window ("14d"); unset means off.
const RawRetentionKey = "raw_retention"

// Setting returns the stored value for key; ok is false when the key is unset.
func (d *DB) Setting(ctx context.Context, key string) (value string, ok bool, err error) {
	err = d.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// PutSetting stores value under key, replacing any previous value.
func (d *DB) PutSetting(ctx context.Context, key, value string) error {
	_, err := d.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// DeleteSetting removes key; deleting an unset key is not an error.
func (d *DB) DeleteSetting(ctx context.Context, key string) error {
	_, err := d.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

// RawRetention returns the raw_json retention window, 0 when unset. A stored
// value that no longer parses comes back in stored together with the parse
// error, so callers can name it in a warning while treating retention as off.
func (d *DB) RawRetention(ctx context.Context) (window time.Duration, stored string, err error) {
	v, ok, err := d.Setting(ctx, RawRetentionKey)
	if err != nil || !ok {
		return 0, "", err
	}
	w, err := timeutil.ParseRetention(v)
	if err != nil {
		return 0, v, err
	}
	return w, v, nil
}
