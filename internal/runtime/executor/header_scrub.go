package executor

import (
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

// scrubProxyAndFingerprintHeaders delegates to the shared utility in internal/misc.
func scrubProxyAndFingerprintHeaders(req *http.Request) {
	misc.ScrubProxyAndFingerprintHeaders(req)
}
