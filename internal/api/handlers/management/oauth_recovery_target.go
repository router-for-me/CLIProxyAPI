package management

import (
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func applyOAuthRecoveryTargetLocation(record *coreauth.Auth, target oauthRecoveryTarget) {
	record.ID = target.AuthID
	targetFileName := strings.TrimSpace(target.FileName)
	if targetFileName == "" {
		targetFileName = target.AuthID
	}
	targetPath := strings.TrimSpace(target.Path)
	if targetPath == "" && strings.ContainsAny(target.AuthID, "/\\") {
		targetFileName = target.AuthID
	}
	record.FileName = targetFileName
	if record.Attributes == nil {
		record.Attributes = make(map[string]string)
	}
	if targetPath != "" {
		record.Attributes[coreauth.AttributePath] = targetPath
		record.Attributes[coreauth.AttributeSource] = targetPath
	} else {
		delete(record.Attributes, coreauth.AttributePath)
		record.Attributes[coreauth.AttributeSource] = targetFileName
	}
}
