package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const codexAgentIdentityHarnessID = "codex-cli"

var (
	errAgentIdentityAuthNotFound = errors.New("auth credential not found")
	errAgentIdentityAuthConflict = errors.New("auth index identifies multiple credentials")
)

type agentIdentityRegistrar interface {
	RegisterAgent(context.Context, codex.AgentRegistration) (string, error)
	RegisterTask(context.Context, codex.AgentIdentityKey) (string, error)
}

type agentIdentityRegistrarFactory func(*coreauth.Auth) agentIdentityRegistrar

type provisionAgentIdentityRequest struct {
	AuthIndex    string `json:"auth_index"`
	RegisterTask *bool  `json:"register_task"`
	Force        bool   `json:"force"`
}

type provisionAgentIdentityResult struct {
	Status    codex.ManagedAgentIdentityState
	AuthIndex string
	HasTask   bool
	Reused    bool
}

// ProvisionAgentIdentity creates and persists a managed Codex Agent Identity.
func (h *Handler) ProvisionAgentIdentity(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var request provisionAgentIdentityRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	request.AuthIndex = strings.TrimSpace(request.AuthIndex)
	if request.AuthIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}
	registerTask := request.RegisterTask == nil || *request.RegisterTask

	var result provisionAgentIdentityResult
	var persisted bool
	finalAuth, err := h.authManager.WithMetadataTransactionByIndex(c.Request.Context(), request.AuthIndex, func(transaction *coreauth.MetadataTransaction) error {
		auth := transaction.Auth()
		if auth == nil {
			return errAgentIdentityAuthNotFound
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			return &agentIdentityStateError{message: "auth credential is not a Codex credential"}
		}
		if coreauth.IsPluginVirtualAuth(auth) || isRuntimeOnlyAuth(auth) {
			return &agentIdentityStateError{message: "auth credential cannot be modified directly"}
		}

		credentials, errCredentials := codex.ManagedAgentIdentityCredentialsFromMetadata(auth.Metadata)
		if errCredentials != nil {
			return &agentIdentityCredentialError{cause: errCredentials}
		}
		var errProvision error
		result, errProvision = h.provisionManagedAgentIdentity(c.Request.Context(), transaction, auth, credentials, registerTask, request.Force)
		persisted = transaction.Persisted()
		return errProvision
	})
	if persisted && finalAuth != nil && h.postAuthPersistHook != nil {
		if errHook := h.postAuthPersistHook(c.Request.Context(), finalAuth.Clone()); errHook != nil {
			if err == nil {
				err = fmt.Errorf("post-auth persist hook failed: %w", errHook)
			}
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, coreauth.ErrAuthStoreUnavailable):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "durable auth store unavailable"})
			return
		case errors.Is(err, coreauth.ErrAuthIndexNotFound), errors.Is(err, errAgentIdentityAuthNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "auth credential not found"})
			return
		case errors.Is(err, coreauth.ErrAuthIndexAmbiguous), errors.Is(err, errAgentIdentityAuthConflict):
			c.JSON(http.StatusConflict, gin.H{"error": "auth_index is not unique"})
			return
		}
		var upstreamError *agentIdentityUpstreamError
		if errors.As(err, &upstreamError) {
			payload := gin.H{
				"error":              upstreamError.message,
				"status":             upstreamError.state,
				"auth_index":         request.AuthIndex,
				"has_tokens":         true,
				"has_agent_identity": upstreamError.hasIdentity,
				"has_task":           false,
			}
			c.JSON(http.StatusBadGateway, payload)
			return
		}
		var conflictError *agentIdentityStateError
		if errors.As(err, &conflictError) {
			c.JSON(http.StatusConflict, gin.H{"error": conflictError.Error()})
			return
		}
		var credentialError *agentIdentityCredentialError
		if errors.As(err, &credentialError) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid Codex credential"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist Agent Identity credential"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":             result.Status,
		"auth_index":         result.AuthIndex,
		"has_tokens":         true,
		"has_agent_identity": true,
		"has_task":           result.HasTask,
		"reused":             result.Reused,
	})
}

// ExportAgentIdentityAuth returns a Codex CLI-compatible auth.json representation.
func (h *Handler) ExportAgentIdentityAuth(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	authIndex := strings.TrimSpace(c.Query("auth_index"))
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}

	auth, err := h.agentIdentityAuthByIndex(authIndex)
	if err != nil {
		switch {
		case errors.Is(err, errAgentIdentityAuthNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "auth credential not found"})
		case errors.Is(err, errAgentIdentityAuthConflict):
			c.JSON(http.StatusConflict, gin.H{"error": "auth_index is not unique"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve auth credential"})
		}
		return
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		c.JSON(http.StatusConflict, gin.H{"error": "auth credential is not a Codex credential"})
		return
	}

	credentials, err := codex.ManagedAgentIdentityCredentialsFromMetadata(auth.Metadata)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid Codex credential"})
		return
	}
	data, err := credentials.MarshalCodexAuthFile()
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "managed Agent Identity is not ready for export"})
		return
	}

	c.Header("Content-Disposition", `attachment; filename="auth.json"`)
	c.Header("Cache-Control", "no-store, private")
	c.Header("Pragma", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "application/json", data)
}

func (h *Handler) provisionManagedAgentIdentity(ctx context.Context, transaction *coreauth.MetadataTransaction, auth *coreauth.Auth, credentials codex.ManagedAgentIdentityCredentials, registerTask, force bool) (provisionAgentIdentityResult, error) {
	result := provisionAgentIdentityResult{AuthIndex: lockedAuthIndex(auth)}
	hasRuntime := strings.TrimSpace(credentials.AgentRuntimeID) != ""
	hasPrivateKey := strings.TrimSpace(credentials.AgentPrivateKey) != ""
	hasTask := strings.TrimSpace(credentials.TaskID) != ""
	hasIdentityMaterial := hasRuntime || hasPrivateKey || hasTask
	bindingSnapshotMissing := hasIdentityMaterial && agentIdentityBindingSnapshotMissing(auth.Metadata)
	if !force && hasIdentityMaterial && (strings.TrimSpace(credentials.AgentAccountID) != strings.TrimSpace(credentials.AccountID) || strings.TrimSpace(credentials.AgentChatGPTUserID) != strings.TrimSpace(credentials.ChatGPTUserID)) {
		return result, &agentIdentityStateError{message: "managed Agent Identity belongs to a different ChatGPT account; re-provision with force=true"}
	}

	if !force && hasRuntime != hasPrivateKey {
		if hasRuntime {
			return result, &agentIdentityStateError{message: "managed Agent Identity is missing its private key; re-provision with force=true"}
		}
		// A persisted private key without a runtime ID resumes the interrupted registration.
	}
	if !force && !hasRuntime && !hasPrivateKey && hasTask {
		return result, &agentIdentityStateError{message: "managed Agent Identity has a task without registration material; re-provision with force=true"}
	}
	if !force && hasRuntime && hasPrivateKey {
		if _, err := codex.ParseAgentIdentityPrivateKey(credentials.AgentPrivateKey); err != nil {
			return result, &agentIdentityStateError{message: "managed Agent Identity private key is invalid; re-provision with force=true"}
		}
		result.Reused = true
		desiredState := codex.ManagedAgentIdentityStateNeedsTask
		if hasTask {
			desiredState = codex.ManagedAgentIdentityStateReady
		}
		setLastRefresh := strings.TrimSpace(credentials.LastRefresh) == ""
		stateChanged := credentials.State != desiredState
		credentials.State = desiredState
		if setLastRefresh {
			credentials.LastRefresh = time.Now().UTC().Format(time.RFC3339)
		}
		if setLastRefresh || stateChanged || bindingSnapshotMissing {
			if _, err := mergeAgentIdentityMetadata(transaction, credentials, setLastRefresh); err != nil {
				return result, err
			}
		}
		if hasTask {
			result.Status = desiredState
			result.HasTask = true
			return result, nil
		}
		if !registerTask {
			result.Status = desiredState
			return result, nil
		}
		return h.registerAndPersistAgentTask(ctx, transaction, auth, credentials, true)
	}

	var keyMaterial codex.AgentKeyMaterial
	if !force && hasPrivateKey {
		publicKey, err := codex.PublicKeySSHFromPrivateKey(credentials.AgentPrivateKey)
		if err != nil {
			return result, &agentIdentityStateError{message: "managed Agent Identity private key is invalid; re-provision with force=true"}
		}
		keyMaterial = codex.AgentKeyMaterial{
			PrivateKeyPKCS8Base64: credentials.AgentPrivateKey,
			PublicKeySSH:          publicKey,
		}
	} else {
		var err error
		keyMaterial, err = codex.GenerateAgentKeyMaterial()
		if err != nil {
			return result, err
		}
		credentials.AgentPrivateKey = keyMaterial.PrivateKeyPKCS8Base64
		credentials.AgentRuntimeID = ""
		credentials.TaskID = ""
		credentials.AgentAccountID = strings.TrimSpace(credentials.AccountID)
		credentials.AgentChatGPTUserID = strings.TrimSpace(credentials.ChatGPTUserID)
		credentials.State = codex.ManagedAgentIdentityStateProvisioning
		if _, err = mergeAgentIdentityMetadata(transaction, credentials, false); err != nil {
			return result, err
		}
	}

	registrar := h.agentIdentityRegistrarFor(auth)
	runtimeID, err := registrar.RegisterAgent(ctx, codex.AgentRegistration{
		AccessToken:      credentials.AccessToken,
		IsFedRAMPAccount: credentials.ChatGPTAccountIsFedRAMP,
		KeyMaterial:      keyMaterial,
		BillOfMaterials: codex.AgentBillOfMaterials{
			AgentVersion:    agentIdentityVersion(),
			AgentHarnessID:  codexAgentIdentityHarnessID,
			RunningLocation: "custom-" + runtime.GOOS,
		},
	})
	if err == nil && strings.TrimSpace(runtimeID) == "" {
		err = errors.New("Agent Identity registration returned an empty runtime ID")
	}
	if err != nil {
		credentials.State = codex.ManagedAgentIdentityStateError
		_, _ = mergeAgentIdentityMetadata(transaction, credentials, false)
		return result, &agentIdentityUpstreamError{
			message:     "Agent Identity registration failed",
			state:       codex.ManagedAgentIdentityStateError,
			hasIdentity: false,
			cause:       err,
		}
	}

	credentials.AgentRuntimeID = strings.TrimSpace(runtimeID)
	credentials.AgentPrivateKey = keyMaterial.PrivateKeyPKCS8Base64
	credentials.TaskID = ""
	credentials.State = codex.ManagedAgentIdentityStateNeedsTask
	setLastRefresh := strings.TrimSpace(credentials.LastRefresh) == ""
	if setLastRefresh {
		credentials.LastRefresh = time.Now().UTC().Format(time.RFC3339)
	}
	if _, err = mergeAgentIdentityMetadata(transaction, credentials, setLastRefresh); err != nil {
		return result, err
	}
	if !registerTask {
		result.Status = codex.ManagedAgentIdentityStateNeedsTask
		return result, nil
	}
	return h.registerAndPersistAgentTask(ctx, transaction, auth, credentials, false)
}

func agentIdentityBindingSnapshotMissing(metadata map[string]any) bool {
	accountID, _ := metadata["agent_identity_account_id"].(string)
	userID, _ := metadata["chatgpt_user_id"].(string)
	return strings.TrimSpace(accountID) == "" || strings.TrimSpace(userID) == ""
}

func (h *Handler) registerAndPersistAgentTask(ctx context.Context, transaction *coreauth.MetadataTransaction, auth *coreauth.Auth, credentials codex.ManagedAgentIdentityCredentials, reused bool) (provisionAgentIdentityResult, error) {
	result := provisionAgentIdentityResult{
		Status:    codex.ManagedAgentIdentityStateNeedsTask,
		AuthIndex: lockedAuthIndex(auth),
		Reused:    reused,
	}
	credentials.TaskID = ""
	registrar := h.agentIdentityRegistrarFor(auth)
	taskID, err := registrar.RegisterTask(ctx, codex.AgentIdentityKey{
		AgentRuntimeID:        credentials.AgentRuntimeID,
		PrivateKeyPKCS8Base64: credentials.AgentPrivateKey,
	})
	if err == nil && strings.TrimSpace(taskID) == "" {
		err = errors.New("task registration returned an empty task ID")
	}
	if err != nil {
		credentials.State = codex.ManagedAgentIdentityStateNeedsTask
		_, _ = mergeAgentIdentityMetadata(transaction, credentials, false)
		return result, &agentIdentityUpstreamError{
			message:     "Agent Identity task registration failed",
			state:       codex.ManagedAgentIdentityStateNeedsTask,
			hasIdentity: true,
			cause:       err,
		}
	}

	credentials.TaskID = taskID
	credentials.State = codex.ManagedAgentIdentityStateReady
	if _, err = mergeAgentIdentityMetadata(transaction, credentials, false); err != nil {
		return result, err
	}
	result.Status = codex.ManagedAgentIdentityStateReady
	result.HasTask = true
	return result, nil
}

func mergeAgentIdentityMetadata(transaction *coreauth.MetadataTransaction, credentials codex.ManagedAgentIdentityCredentials, setLastRefresh bool) (*coreauth.Auth, error) {
	updates := map[string]any{
		"auth_mode":                  codex.CodexAuthModeChatGPT,
		"agent_identity_account_id":  strings.TrimSpace(credentials.AgentAccountID),
		"agent_runtime_id":           strings.TrimSpace(credentials.AgentRuntimeID),
		"agent_private_key":          strings.TrimSpace(credentials.AgentPrivateKey),
		"chatgpt_user_id":            strings.TrimSpace(credentials.AgentChatGPTUserID),
		"email":                      strings.TrimSpace(credentials.Email),
		"plan_type":                  strings.TrimSpace(credentials.PlanType),
		"chatgpt_account_is_fedramp": credentials.ChatGPTAccountIsFedRAMP,
		"task_id":                    credentials.TaskID,
		"agent_identity_state":       string(credentials.State),
	}
	if setLastRefresh {
		updates["last_refresh"] = strings.TrimSpace(credentials.LastRefresh)
	}
	return transaction.Merge(updates)
}

func (h *Handler) agentIdentityAuthByIndex(authIndex string) (*coreauth.Auth, error) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" || h == nil || h.authManager == nil {
		return nil, errAgentIdentityAuthNotFound
	}
	var match *coreauth.Auth
	for _, auth := range h.authManager.List() {
		if auth == nil || lockedAuthIndex(auth) != authIndex {
			continue
		}
		if match != nil {
			return nil, errAgentIdentityAuthConflict
		}
		match = auth
	}
	if match == nil {
		return nil, errAgentIdentityAuthNotFound
	}
	return match, nil
}

func (h *Handler) agentIdentityRegistrarFor(auth *coreauth.Auth) agentIdentityRegistrar {
	if h.agentIdentityRegistrar != nil {
		return h.agentIdentityRegistrar(auth)
	}
	return codex.NewAgentIdentityClient(&http.Client{Transport: h.apiCallTransport(auth)})
}

func agentIdentityVersion() string {
	if version := strings.TrimSpace(buildinfo.Version); version != "" {
		return version
	}
	return "dev"
}

type agentIdentityStateError struct {
	message string
}

type agentIdentityCredentialError struct {
	cause error
}

func (err *agentIdentityCredentialError) Error() string {
	return err.cause.Error()
}

func (err *agentIdentityCredentialError) Unwrap() error {
	return err.cause
}

func (err *agentIdentityStateError) Error() string {
	return err.message
}

type agentIdentityUpstreamError struct {
	message     string
	state       codex.ManagedAgentIdentityState
	hasIdentity bool
	cause       error
}

func (err *agentIdentityUpstreamError) Error() string {
	return err.message
}

func (err *agentIdentityUpstreamError) Unwrap() error {
	return err.cause
}
