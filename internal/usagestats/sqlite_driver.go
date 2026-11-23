//go:build !solaris || illumos

package usagestats

// Pure-Go SQLite driver, registered as "sqlite".
import _ "modernc.org/sqlite"

// errSQLiteDriver is nil where the SQLite driver is available.
var errSQLiteDriver error
