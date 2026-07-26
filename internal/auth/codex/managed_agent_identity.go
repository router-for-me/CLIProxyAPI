package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CodexAuthModeChatGPT is the auth.json mode for managed ChatGPT credentials.
const CodexAuthModeChatGPT = "chatgpt"

// ManagedAgentIdentityState describes whether durable Agent Identity material is ready for use.
type ManagedAgentIdentityState string

const (
	ManagedAgentIdentityStateProvisioning ManagedAgentIdentityState = "provisioning"
	ManagedAgentIdentityStateNeedsTask    ManagedAgentIdentityState = "needs_task"
	ManagedAgentIdentityStateReady        ManagedAgentIdentityState = "ready"
	ManagedAgentIdentityStateError        ManagedAgentIdentityState = "error"
)

// ManagedAgentIdentityCredentials is the canonical flat representation stored by CLIProxyAPI.
// AccountID and ChatGPTUserID describe the current OAuth binding, while the Agent-prefixed
// fields snapshot the account and user that own the registered key. CodexAuthFile converts
// this representation to the nested schema consumed by Codex CLI.
type ManagedAgentIdentityCredentials struct {
	IDToken                 string                    `json:"id_token"`
	AccessToken             string                    `json:"access_token"`
	RefreshToken            string                    `json:"refresh_token"`
	AccountID               string                    `json:"account_id"`
	ChatGPTUserID           string                    `json:"current_chatgpt_user_id"`
	LastRefresh             string                    `json:"last_refresh"`
	AgentRuntimeID          string                    `json:"agent_runtime_id"`
	AgentPrivateKey         string                    `json:"agent_private_key"`
	AgentAccountID          string                    `json:"agent_identity_account_id"`
	AgentChatGPTUserID      string                    `json:"chatgpt_user_id"`
	Email                   string                    `json:"email"`
	PlanType                string                    `json:"plan_type"`
	ChatGPTAccountIsFedRAMP bool                      `json:"chatgpt_account_is_fedramp"`
	TaskID                  string                    `json:"task_id,omitempty"`
	State                   ManagedAgentIdentityState `json:"agent_identity_state,omitempty"`
}

// CodexAuthFile is the managed ChatGPT auth.json schema consumed by Codex CLI.
type CodexAuthFile struct {
	AuthMode      string                   `json:"auth_mode"`
	OpenAIAPIKey  *string                  `json:"OPENAI_API_KEY"`
	Tokens        CodexAuthTokens          `json:"tokens"`
	LastRefresh   string                   `json:"last_refresh"`
	AgentIdentity CodexAgentIdentityRecord `json:"agent_identity"`
}

// CodexAuthTokens contains the OAuth token bundle in Codex CLI's nested format.
type CodexAuthTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

// CodexAgentIdentityRecord is the record form of Codex CLI's agent_identity field.
type CodexAgentIdentityRecord struct {
	AgentRuntimeID          string `json:"agent_runtime_id"`
	AgentPrivateKey         string `json:"agent_private_key"`
	AccountID               string `json:"account_id"`
	ChatGPTUserID           string `json:"chatgpt_user_id"`
	Email                   string `json:"email"`
	PlanType                string `json:"plan_type"`
	ChatGPTAccountIsFedRAMP bool   `json:"chatgpt_account_is_fedramp"`
	TaskID                  string `json:"task_id,omitempty"`
}

// ManagedAgentIdentityCredentialsFromMetadata normalizes a flat CLIProxyAPI credential.
// JWT claims are used as identity metadata only; token validity is established upstream.
func ManagedAgentIdentityCredentialsFromMetadata(metadata map[string]any) (ManagedAgentIdentityCredentials, error) {
	idToken, err := metadataString(metadata, "id_token")
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	accessToken, err := metadataString(metadata, "access_token")
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	refreshToken, err := metadataString(metadata, "refresh_token")
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	requiredTokens := []struct {
		name  string
		value string
	}{
		{name: "id_token", value: idToken},
		{name: "access_token", value: accessToken},
		{name: "refresh_token", value: refreshToken},
	}
	for _, token := range requiredTokens {
		if strings.TrimSpace(token.value) == "" {
			return ManagedAgentIdentityCredentials{}, fmt.Errorf("managed Agent Identity credential is missing %s", token.name)
		}
	}

	claims, err := ParseJWTToken(idToken)
	if err != nil {
		return ManagedAgentIdentityCredentials{}, fmt.Errorf("parse managed Agent Identity id_token: %w", err)
	}
	metadataAccountID, err := metadataString(metadata, "account_id")
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	claimAccountID := claims.GetAccountID()
	// A selected workspace intentionally overrides the default account in the ID token.
	accountID := claimAccountID
	if strings.TrimSpace(metadataAccountID) != "" {
		accountID = strings.TrimSpace(metadataAccountID)
	}
	if accountID == "" {
		return ManagedAgentIdentityCredentials{}, errors.New("managed Agent Identity credential is missing account_id")
	}

	userID := claims.GetUserID()
	if userID == "" {
		return ManagedAgentIdentityCredentials{}, errors.New("managed Agent Identity credential is missing chatgpt_user_id")
	}

	lastRefresh, err := normalizeMetadataTimestamp(metadata, "last_refresh")
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	agentRuntimeID, err := metadataString(metadata, "agent_runtime_id")
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	agentPrivateKey, err := metadataString(metadata, "agent_private_key")
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	agentAccountID, err := metadataString(metadata, "agent_identity_account_id")
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	agentUserID, err := metadataString(metadata, "chatgpt_user_id")
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	taskID, err := metadataString(metadata, "task_id")
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	state, err := metadataString(metadata, "agent_identity_state")
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	normalizedState := ManagedAgentIdentityState(strings.TrimSpace(state))
	switch normalizedState {
	case "", ManagedAgentIdentityStateProvisioning, ManagedAgentIdentityStateNeedsTask, ManagedAgentIdentityStateReady, ManagedAgentIdentityStateError:
	default:
		return ManagedAgentIdentityCredentials{}, fmt.Errorf("managed Agent Identity credential has unknown state %q", normalizedState)
	}
	email, err := metadataString(metadata, "email")
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	planType, err := metadataString(metadata, "plan_type")
	if err != nil {
		return ManagedAgentIdentityCredentials{}, err
	}
	if claimEmail := claims.GetUserEmail(); claimEmail != "" {
		email = claimEmail
	}
	if claimPlanType := claims.GetPlanType(); claimPlanType != "" {
		planType = claimPlanType
	}

	agentRuntimeID = strings.TrimSpace(agentRuntimeID)
	agentPrivateKey = strings.TrimSpace(agentPrivateKey)
	agentAccountID = strings.TrimSpace(agentAccountID)
	agentUserID = strings.TrimSpace(agentUserID)
	hasAgentMaterial := agentRuntimeID != "" || agentPrivateKey != "" || strings.TrimSpace(taskID) != ""
	if !hasAgentMaterial {
		agentAccountID = accountID
		agentUserID = userID
	} else {
		// Records created before the binding snapshot was introduced belong to
		// the selected account that carried the material.
		if agentAccountID == "" {
			agentAccountID = accountID
		}
		if agentUserID == "" {
			agentUserID = userID
		}
	}

	return ManagedAgentIdentityCredentials{
		IDToken:                 idToken,
		AccessToken:             accessToken,
		RefreshToken:            refreshToken,
		AccountID:               accountID,
		ChatGPTUserID:           userID,
		LastRefresh:             lastRefresh,
		AgentRuntimeID:          agentRuntimeID,
		AgentPrivateKey:         agentPrivateKey,
		AgentAccountID:          agentAccountID,
		AgentChatGPTUserID:      agentUserID,
		Email:                   strings.TrimSpace(email),
		PlanType:                normalizeAgentIdentityPlanType(planType),
		ChatGPTAccountIsFedRAMP: claims.IsFedRAMPAccount(),
		TaskID:                  taskID,
		State:                   normalizedState,
	}, nil
}

func normalizeAgentIdentityPlanType(planType string) string {
	switch strings.ToLower(strings.TrimSpace(planType)) {
	case "":
		return "unknown"
	case "free":
		return "free"
	case "go":
		return "go"
	case "plus":
		return "plus"
	case "pro":
		return "pro"
	case "prolite":
		return "prolite"
	case "team":
		return "team"
	case "self_serve_business_usage_based":
		return "self_serve_business_usage_based"
	case "business":
		return "business"
	case "enterprise_cbp_usage_based":
		return "enterprise_cbp_usage_based"
	case "hc":
		return "enterprise"
	case "enterprise":
		return "enterprise"
	case "education":
		return "edu"
	case "edu":
		return "edu"
	default:
		return "unknown"
	}
}

// CodexAuthFile validates the managed credential and returns the Codex CLI-compatible auth.json form.
func (credentials ManagedAgentIdentityCredentials) CodexAuthFile() (CodexAuthFile, error) {
	if err := credentials.validateForExport(); err != nil {
		return CodexAuthFile{}, err
	}

	lastRefresh, err := time.Parse(time.RFC3339, strings.TrimSpace(credentials.LastRefresh))
	if err != nil {
		return CodexAuthFile{}, errors.New("last_refresh must be a valid RFC3339 timestamp")
	}

	return CodexAuthFile{
		AuthMode:     CodexAuthModeChatGPT,
		OpenAIAPIKey: nil,
		Tokens: CodexAuthTokens{
			IDToken:      credentials.IDToken,
			AccessToken:  credentials.AccessToken,
			RefreshToken: credentials.RefreshToken,
			AccountID:    strings.TrimSpace(credentials.AccountID),
		},
		LastRefresh: lastRefresh.UTC().Format(time.RFC3339Nano),
		AgentIdentity: CodexAgentIdentityRecord{
			AgentRuntimeID:          strings.TrimSpace(credentials.AgentRuntimeID),
			AgentPrivateKey:         strings.TrimSpace(credentials.AgentPrivateKey),
			AccountID:               strings.TrimSpace(credentials.AgentAccountID),
			ChatGPTUserID:           strings.TrimSpace(credentials.AgentChatGPTUserID),
			Email:                   strings.TrimSpace(credentials.Email),
			PlanType:                normalizeAgentIdentityPlanType(credentials.PlanType),
			ChatGPTAccountIsFedRAMP: credentials.ChatGPTAccountIsFedRAMP,
			TaskID:                  credentials.TaskID,
		},
	}, nil
}

// MarshalCodexAuthFile returns an indented auth.json document with a trailing newline.
func (credentials ManagedAgentIdentityCredentials) MarshalCodexAuthFile() ([]byte, error) {
	authFile, err := credentials.CodexAuthFile()
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(authFile, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal Codex auth file: %w", err)
	}
	return append(data, '\n'), nil
}

func (credentials ManagedAgentIdentityCredentials) validateForExport() error {
	required := []struct {
		name  string
		value string
	}{
		{name: "id_token", value: credentials.IDToken},
		{name: "access_token", value: credentials.AccessToken},
		{name: "refresh_token", value: credentials.RefreshToken},
		{name: "account_id", value: credentials.AccountID},
		{name: "last_refresh", value: credentials.LastRefresh},
		{name: "agent_runtime_id", value: credentials.AgentRuntimeID},
		{name: "agent_private_key", value: credentials.AgentPrivateKey},
		{name: "current token chatgpt_user_id", value: credentials.ChatGPTUserID},
		{name: "agent_identity_account_id", value: credentials.AgentAccountID},
		{name: "Agent Identity chatgpt_user_id", value: credentials.AgentChatGPTUserID},
		{name: "plan_type", value: credentials.PlanType},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("managed Agent Identity credential is missing %s", field.name)
		}
	}

	switch credentials.State {
	case "", ManagedAgentIdentityStateNeedsTask:
	case ManagedAgentIdentityStateReady:
		if strings.TrimSpace(credentials.TaskID) == "" {
			return errors.New("managed Agent Identity credential is ready but missing task_id")
		}
	case ManagedAgentIdentityStateProvisioning, ManagedAgentIdentityStateError:
		return fmt.Errorf("managed Agent Identity credential is not exportable in state %q", credentials.State)
	default:
		return fmt.Errorf("managed Agent Identity credential has unknown state %q", credentials.State)
	}

	if _, err := ParseAgentIdentityPrivateKey(credentials.AgentPrivateKey); err != nil {
		return fmt.Errorf("managed Agent Identity credential has invalid agent_private_key: %w", err)
	}
	if strings.TrimSpace(credentials.AgentAccountID) != strings.TrimSpace(credentials.AccountID) {
		return errors.New("managed Agent Identity account binding does not match the selected account")
	}
	if strings.TrimSpace(credentials.AgentChatGPTUserID) != strings.TrimSpace(credentials.ChatGPTUserID) {
		return errors.New("managed Agent Identity user binding does not match the current token")
	}
	return nil
}

func metadataString(metadata map[string]any, key string) (string, error) {
	value, ok := metadata[key]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("managed Agent Identity field %s must be a string", key)
	}
	return text, nil
}

func normalizeMetadataTimestamp(metadata map[string]any, key string) (string, error) {
	value, ok := metadata[key]
	if !ok || value == nil {
		return "", nil
	}
	var timestamp time.Time
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return "", nil
		}
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(typed))
		if err != nil {
			return "", fmt.Errorf("managed Agent Identity field %s must be an RFC3339 timestamp", key)
		}
		timestamp = parsed
	case time.Time:
		if typed.IsZero() {
			return "", nil
		}
		timestamp = typed
	default:
		return "", fmt.Errorf("managed Agent Identity field %s must be a string or time.Time", key)
	}
	return timestamp.UTC().Format(time.RFC3339Nano), nil
}
