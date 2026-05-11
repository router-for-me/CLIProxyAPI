package cmd

import (
	"context"
	"fmt"
	"os"

	btauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/bt"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func DoBTLogin(cfg *config.Config) {
	if cfg == nil || len(cfg.BTKey) == 0 {
		log.Error("No BT credentials configured. Add bt entries to config.yaml.")
		return
	}

	store := sdkAuth.GetTokenStore()
	successCount := 0

	for i, entry := range cfg.BTKey {
		phone := entry.Phone
		password := entry.Password
		if phone == "" || password == "" {
			log.Warnf("bt[%d]: missing phone or password, skipping", i)
			continue
		}

		fmt.Printf("Logging in BT account: %s ...\n", phone)

		token, err := btauth.Login(phone, password)
		if err != nil {
			log.Errorf("bt[%d]: login failed for phone %s: %v", i, phone, err)
			continue
		}

		authID := fmt.Sprintf("bt-%s.json", phone)
		auth := &cliproxyauth.Auth{
			ID:       authID,
			FileName: authID,
			Provider: "bt",
			Label:    phone,
			Storage:  token,
			Metadata: map[string]any{
				"type":        "bt",
				"bt_phone":    phone,
				"bt_password": password,
				"uid":         token.UID,
				"access_key":  token.AccessKey,
				"serverid":    token.ServerID,
			},
		}

		if cfg != nil {
			if dirSetter, ok := store.(interface{ SetBaseDir(string) }); ok {
				dirSetter.SetBaseDir(cfg.AuthDir)
			}
		}

		savedPath, err := store.Save(context.Background(), auth)
		if err != nil {
			log.Errorf("bt[%d]: failed to save credentials: %v", i, err)
			continue
		}

		fmt.Printf("BT authentication saved to %s\n", savedPath)
		fmt.Printf("Authenticated as %s (uid: %s)\n", phone, token.UID)
		successCount++
	}

	if successCount > 0 {
		fmt.Printf("\nBT authentication successful! (%d account(s))\n", successCount)
	} else {
		fmt.Println("\nNo BT accounts were authenticated successfully.")
		os.Exit(1)
	}
}
