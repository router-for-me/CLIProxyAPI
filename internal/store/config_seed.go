package store

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

// hashLocalConfigForSeeding hashes every plaintext management secret in the local
// config file, as startup does right after, and returns the file's contents to
// seed an empty remote store with. ok is false when the file may still hold a
// plaintext secret: nothing may be uploaded then, and the remote stays empty until
// a later start seeds it.
func hashLocalConfigForSeeding(configPath, backend string) (data []byte, ok bool) {
	data, errHash := config.HashSecretKeysInFile(configPath)
	if errHash != nil {
		log.WithError(errHash).Warnf("%s: not seeding the remote config, it may hold a plaintext management secret", backend)
		return nil, false
	}
	return data, true
}
