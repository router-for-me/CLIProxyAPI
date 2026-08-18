package synthesizer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// commandCodeCLIAuthFile is the credential file written by `cmdc login`.
// The Command Code Go subscription does not provide a provider-scoped API key;
// the CLI stores the subscription credential here and uses it directly as
// `Authorization: Bearer <apiKey>` against /alpha/generate. CLIProxyAPI reuses
// that same credential so users only ever need `cmdc login`.
const commandCodeCLIAuthFile = ".commandcode/auth.json"

// commandCodeCLIAuthPathFn resolves the path of the cmdc CLI credential file.
// It is a variable so tests can redirect it without touching the real login.
var commandCodeCLIAuthPathFn = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("commandcode cli auth: resolve home dir: %w", err)
	}
	return filepath.Join(home, filepath.FromSlash(commandCodeCLIAuthFile)), nil
}

// SetCommandCodeCLIAuthPathFnForTest overrides the CLI credential path resolver
// for tests in other packages. It returns the previous resolver for restore.
func SetCommandCodeCLIAuthPathFnForTest(fn func() (string, error)) func() (string, error) {
	orig := commandCodeCLIAuthPathFn
	commandCodeCLIAuthPathFn = fn
	return orig
}

// commandCodeCLICredential mirrors the JSON schema written by `cmdc login`
// (Command Code CLI v1.27.x). Only apiKey is required for execution.
type commandCodeCLICredential struct {
	APIKey          string `json:"apiKey"`
	UserID          string `json:"userId"`
	UserName        string `json:"userName"`
	KeyName         string `json:"keyName"`
	AuthenticatedAt string `json:"authenticatedAt"`
}

// synthesizeCommandCodeCLI imports the credential produced by `cmdc login`
// into a CLIProxyAPI Auth entry. It is the default Go path: no provider API
// key needs to be generated or configured. If the CLI credential is missing
// or malformed it returns nil (unauthenticated, never panics).
func (s *ConfigSynthesizer) synthesizeCommandCodeCLI(ctx *SynthesisContext) []*coreauth.Auth {
	if ctx == nil || ctx.Config == nil || ctx.IDGenerator == nil {
		return nil
	}
	path, errPath := commandCodeCLIAuthPathFn()
	if errPath != nil {
		log.Debugf("commandcode cli auth: %v", errPath)
		return nil
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if !os.IsNotExist(errRead) {
			log.Debugf("commandcode cli auth: read %s: %v", path, errRead)
		}
		return nil
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}

	var cred commandCodeCLICredential
	if errUnmarshal := json.Unmarshal(data, &cred); errUnmarshal != nil {
		log.Warnf("commandcode cli auth: malformed credential file %s: %v", path, errUnmarshal)
		return nil
	}
	apiKey := strings.TrimSpace(cred.APIKey)
	if apiKey == "" {
		log.Warnf("commandcode cli auth: credential file %s has no apiKey; treating as unauthenticated", path)
		return nil
	}

	id, _ := ctx.IDGenerator.Next("commandcode:cli", apiKey)
	label := "commandcode-cli"
	if cred.UserName != "" {
		label = "commandcode-cli:" + cred.UserName
	}
	a := &coreauth.Auth{
		ID:       id,
		Provider: constant.CommandCode,
		Label:    label,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"source":  "cli:commandcode",
			"api_key": apiKey,
		},
		CreatedAt: ctx.Now,
		UpdatedAt: ctx.Now,
	}
	if cred.UserID != "" {
		a.Attributes["user_id"] = cred.UserID
	}
	return []*coreauth.Auth{a}
}
