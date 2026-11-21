package sqlite

import (
	"errors"

	driver "modernc.org/sqlite"
)

// sqliteFull is SQLite's primary result code for a database or disk that is full (SQLITE_FULL).
const sqliteFull = 13

// IsFull says whether err is SQLite's SQLITE_FULL, extended codes included: the database file could not grow, as on
// a full disk or past PRAGMA max_page_count (issue #910, row 6a).
func IsFull(err error) bool {
	var e *driver.Error

	return errors.As(err, &e) && e.Code()&0xff == sqliteFull
}
