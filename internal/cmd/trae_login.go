package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

// DoTraeLogin imports the existing local Trae CLI session and model catalog.
func DoTraeLogin(cfg *config.Config, options *LoginOptions) {
	manager := newAuthManager()
	record, savedPath, errLogin := manager.Login(context.Background(), "trae", cfg, nil)
	if errLogin != nil {
		log.Errorf("Trae CLI authentication import failed: %v", errLogin)
		return
	}
	if savedPath != "" {
		fmt.Printf("Authentication reference saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Imported Trae CLI login for %s\n", record.Label)
	}
	fmt.Println("Trae CLI authentication import successful!")
}
