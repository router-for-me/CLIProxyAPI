package managementasset

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/selfupdate"
)

// proxx: a panel release can declare the backend API level it needs with a line
// `proxx-min-backend-api: N` in its release body (optionally inside an HTML comment). When N is
// above this backend's level the release is skipped and the installed panel is kept (ADR 0007).
var panelMinBackendAPIRe = regexp.MustCompile(`(?mi)^[ \t]*(?:<!--[ \t]*)?proxx-min-backend-api:[ \t]*(\d+)`)

// panelMinBackendAPI returns the level a release body declares (0 when absent).
func panelMinBackendAPI(body string) int {
	m := panelMinBackendAPIRe.FindStringSubmatch(body)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// proxxPanelCompatible returns an error when the panel release in the GitHub release JSON needs a
// newer backend than this one.
func proxxPanelCompatible(releaseJSON []byte) error {
	need := panelMinBackendAPI(ojson.GetBytes(releaseJSON, "body").String())
	if need > selfupdate.APILevel {
		return fmt.Errorf("panel release needs backend api level %d (this backend has %d); keeping the installed panel", need, selfupdate.APILevel)
	}
	return nil
}
