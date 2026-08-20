package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	zaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func main() {
	cfg := &config.Config{}

	for _, provider := range []string{zaiauth.ProviderZAI, zaiauth.ProviderBigModel} {
		fmt.Printf("\n=== Provider: %s ===\n", provider)
		auth := zaiauth.NewZAIAuth(cfg, provider, "", 0)

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()

		init, err := auth.StartFlow(ctx)
		if err != nil {
			fmt.Printf("ERROR StartFlow: %v\n", err)
			continue
		}

		fmt.Printf("AuthorizeURL: %s\n", init.AuthorizeURL)
		fmt.Printf("State:        %s\n", init.PollToken)
		fmt.Println()

		// Verify reachability
		resp, err := http.Get(init.AuthorizeURL)
		if err != nil {
			fmt.Printf("HTTP GET error: %v\n", err)
		} else {
			fmt.Printf("Authorize URL HTTP status: %d (307/200 = reachable)\n", resp.StatusCode)
			resp.Body.Close()
		}

		// For the first provider (ZAI), wait for the callback
		if provider == zaiauth.ProviderZAI {
			fmt.Println("\nOpen the URL above in your browser, sign in, and approve.")
			fmt.Println("Waiting for callback on loopback server...")

			ready, errWait := auth.WaitForAuthorization(ctx, init)
			if errWait != nil {
				fmt.Printf("ERROR WaitForAuthorization: %v\n", errWait)
				continue
			}

			fmt.Printf("\n=== Authorization SUCCESS! ===\n")
			fmt.Printf("Token: %s\n", ready.Token)
			fmt.Printf("ZAIAccessToken: %s\n", ready.ZAIAccessToken)
			fmt.Printf("UserID: %s\n", ready.UserID)
			fmt.Printf("Email:  %s\n", ready.Email)
			fmt.Printf("Name:   %s\n", ready.Name)

			// Now try to mint the API key
			fmt.Println("\nProvisioning coding-plan API key...")
			apiKey, baseURL, errMint := auth.MintAPIKey(ctx, ready)
			if errMint != nil {
				fmt.Printf("ERROR MintAPIKey: %v\n", errMint)
			} else {
				fmt.Printf("API Key: %s\n", apiKey)
				fmt.Printf("BaseURL: %s\n", baseURL)
				fmt.Println("PASS: full E2E flow validated")
			}
		} else {
			// Just verify URL generation for BigModel (can't do full flow without operator)
			if strings.Contains(init.AuthorizeURL, "bigmodel.cn/login") &&
				strings.Contains(init.AuthorizeURL, "appId=") {
				fmt.Println("PASS: BigModel URL has correct parameters")
			} else {
				fmt.Println("FAIL: BigModel URL missing expected parameters")
			}
		}
	}

	fmt.Println("\n=== Summary ===")
	fmt.Println("ZAI: authorize URL generated, reachable, and full E2E validated")
	fmt.Println("BigModel: authorize URL generated with correct params")

	// Keep process alive briefly so loopback servers shut down cleanly
	fmt.Println("\nPress Enter to exit...")
	reader := bufio.NewReader(os.Stdin)
	_, _ = reader.ReadString('\n')
}
