package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// loginManual is the traditional Claude login: print the authorization URL, let the user approve it
// in any browser, and read back the "code#state" string Claude's own page displays. Nothing listens
// on a local port and no browser is opened, so it works from a phone or over SSH.
func (a *ClaudeAuthenticator) loginManual(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if opts.Prompt == nil {
		return nil, errors.New("claude manual login needs a prompt to read the code from")
	}
	pkceCodes, err := claude.GeneratePKCECodes()
	if err != nil {
		return nil, fmt.Errorf("claude pkce generation failed: %w", err)
	}
	state, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("claude state generation failed: %w", err)
	}
	authSvc := claude.NewClaudeAuth(cfg)
	authURL, state, err := authSvc.GenerateAuthURLWithRedirect(state, pkceCodes, claude.ManualRedirectURI)
	if err != nil {
		return nil, fmt.Errorf("claude authorization url generation failed: %w", err)
	}

	fmt.Printf("Open this URL in a browser signed in to the Claude account to use, approve it, then paste the code Claude shows:\n\n%s\n\n", authURL)
	// The prompt blocks on stdin, so read it in the background and stay cancellable: Ctrl-C or a
	// termination signal must end the login (and release the profile lock) without a line of input.
	type answer struct {
		text string
		err  error
	}
	answered := make(chan answer, 1)
	go func() {
		text, errPrompt := opts.Prompt("Paste the code: ")
		answered <- answer{text, errPrompt}
	}()
	var input string
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case reply := <-answered:
		if reply.err != nil {
			return nil, reply.err
		}
		input = reply.text
	}
	code, gotState, err := splitManualCode(input)
	if err != nil {
		return nil, err
	}
	if gotState != state {
		return nil, claude.NewAuthenticationError(claude.ErrInvalidState, errors.New("the pasted code is for a different login; start again"))
	}

	authBundle, err := authSvc.ExchangeCodeForTokensWithRedirect(ctx, code, state, pkceCodes, claude.ManualRedirectURI)
	if err != nil {
		return nil, claude.NewAuthenticationError(claude.ErrCodeExchangeFailed, err)
	}
	return a.recordFor(authSvc, authBundle)
}

// splitManualCode reads what Claude's code page shows, "code#state". Surrounding whitespace and an
// accidental line break from a wrapped terminal are ignored.
func splitManualCode(input string) (code, state string, err error) {
	compact := strings.Join(strings.Fields(input), "")
	code, state, found := strings.Cut(compact, "#")
	if !found || code == "" || state == "" {
		return "", "", errors.New("expected the code Claude shows, in the form CODE#STATE")
	}
	return code, state, nil
}

func (a *ClaudeAuthenticator) recordFor(authSvc *claude.ClaudeAuth, authBundle *claude.ClaudeAuthBundle) (*coreauth.Auth, error) {
	tokenStorage := authSvc.CreateTokenStorage(authBundle)
	if tokenStorage == nil || tokenStorage.Email == "" {
		return nil, fmt.Errorf("claude token storage missing account information")
	}
	fileName := claude.CredentialFileName(tokenStorage.Email, tokenStorage.OrganizationUUID, tokenStorage.AccountUUID)
	metadata := map[string]any{"email": tokenStorage.Email}
	if tokenStorage.AccountUUID != "" {
		metadata["account_uuid"] = tokenStorage.AccountUUID
	}
	if tokenStorage.OrganizationUUID != "" {
		metadata["organization_uuid"] = tokenStorage.OrganizationUUID
	}
	if tokenStorage.OrganizationName != "" {
		metadata["organization_name"] = tokenStorage.OrganizationName
	}
	if len(tokenStorage.DeviceIDs) > 0 {
		metadata[claude.ClaudeDeviceIDsMetadataKey] = append([]string(nil), tokenStorage.DeviceIDs...)
	}
	fmt.Println("Claude authentication successful")
	return &coreauth.Auth{ID: fileName, Provider: a.Provider(), FileName: fileName, Storage: tokenStorage, Metadata: metadata}, nil
}
