//go:build solaris && !illumos

package usagestats

import (
	"errors"
	"fmt"
	"runtime"
)

// errSQLiteDriver reports that modernc.org/sqlite does not support this target.
// The solaris build tag also covers illumos, which the driver does support.
var errSQLiteDriver = fmt.Errorf("usagestats: SQLite on %s: %w", runtime.GOOS, errors.ErrUnsupported)
