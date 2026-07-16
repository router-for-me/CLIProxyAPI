package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const maxManagementPriority = int64(1<<31 - 1)
const maxPriorityRequestBytes = 16 * 1024

type patchAuthFilePriorityRequest struct {
	Name             string          `json:"name"`
	ExpectedRevision string          `json:"expected_revision"`
	Operation        string          `json:"operation"`
	Priority         json.RawMessage `json:"priority"`
}

func (h *Handler) PatchAuthFilePriority(c *gin.Context) {
	if h == nil || h.authManager == nil {
		writePriorityError(c, http.StatusNotFound, "management_unavailable", "auth manager unavailable")
		return
	}
	rawBody, errRead := io.ReadAll(io.LimitReader(c.Request.Body, maxPriorityRequestBytes+1))
	errDuplicate := rejectDuplicateTopLevelJSONFields(rawBody)
	if errRead != nil || len(rawBody) > maxPriorityRequestBytes || errDuplicate != nil {
		writePriorityError(c, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	var req patchAuthFilePriorityRequest
	decoder := json.NewDecoder(bytes.NewReader(rawBody))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&req); errDecode != nil {
		writePriorityError(c, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if errTrailing := rejectTrailingJSON(decoder); errTrailing != nil {
		writePriorityError(c, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.ExpectedRevision = strings.TrimSpace(req.ExpectedRevision)
	req.Operation = strings.ToLower(strings.TrimSpace(req.Operation))
	if req.Name == "" {
		writePriorityError(c, http.StatusBadRequest, "invalid_name", "name is required")
		return
	}
	if req.ExpectedRevision == "" {
		writePriorityError(c, http.StatusConflict, "revision_conflict", "expected_revision is required")
		return
	}

	mutation := coreauth.PriorityMutation{}
	switch coreauth.PriorityMutationOperation(req.Operation) {
	case coreauth.PriorityMutationSet:
		priority, errPriority := decodeStrictPriority(req.Priority)
		if errPriority != nil {
			writePriorityError(c, http.StatusBadRequest, "invalid_priority", "priority must be a non-negative 32-bit integer")
			return
		}
		mutation.Operation = coreauth.PriorityMutationSet
		mutation.Priority = priority
	case coreauth.PriorityMutationUnset:
		if len(bytes.TrimSpace(req.Priority)) != 0 {
			writePriorityError(c, http.StatusBadRequest, "invalid_priority", "unset must omit priority")
			return
		}
		mutation.Operation = coreauth.PriorityMutationUnset
	default:
		writePriorityError(c, http.StatusBadRequest, "invalid_operation", "operation must be set or unset")
		return
	}

	target, errTarget := findPriorityMutationTarget(h.authManager, req.Name)
	if errTarget != nil {
		writePriorityMutationError(c, errTarget)
		return
	}
	result, errMutation := h.authManager.MutatePriority(c.Request.Context(), target.ID, req.ExpectedRevision, mutation)
	if errMutation != nil {
		writePriorityMutationError(c, errMutation)
		return
	}
	name := strings.TrimSpace(result.Auth.FileName)
	if name == "" {
		name = result.Auth.ID
	}
	priority := gin.H{"present": result.Priority.Present}
	if result.Priority.Present {
		priority["value"] = result.Priority.Value
	}
	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"id":        result.Auth.ID,
		"name":      name,
		"revision":  result.Revision,
		"priority":  priority,
		"persisted": true,
	})
}

func rejectDuplicateTopLevelJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, errToken := decoder.Token()
	if errToken != nil {
		return errToken
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return errors.New("request must be a JSON object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, errToken := decoder.Token()
		if errToken != nil {
			return errToken
		}
		key, okKey := keyToken.(string)
		if !okKey {
			return errors.New("invalid JSON object key")
		}
		if _, exists := seen[key]; exists {
			return errors.New("duplicate JSON field")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if errDecode := decoder.Decode(&value); errDecode != nil {
			return errDecode
		}
	}
	if _, errEnd := decoder.Token(); errEnd != nil {
		return errEnd
	}
	return rejectTrailingJSON(decoder)
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	errDecode := decoder.Decode(&trailing)
	if errors.Is(errDecode, io.EOF) {
		return nil
	}
	if errDecode == nil {
		return errors.New("trailing JSON value")
	}
	return errDecode
}

func decodeStrictPriority(raw json.RawMessage) (int, error) {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 {
		return 0, errors.New("missing priority")
	}
	for i, ch := range value {
		if ch < '0' || ch > '9' || (i == 0 && ch == '0' && len(value) > 1) {
			return 0, errors.New("priority is not a canonical integer")
		}
	}
	parsed, errParse := strconv.ParseInt(string(value), 10, 32)
	if errParse != nil || parsed < 0 || parsed > maxManagementPriority {
		return 0, errors.New("priority outside safe range")
	}
	return int(parsed), nil
}

func findPriorityMutationTarget(manager *coreauth.Manager, name string) (*coreauth.Auth, error) {
	if auth, ok := manager.GetByID(name); ok && auth != nil {
		return auth, nil
	}
	var target *coreauth.Auth
	for _, auth := range manager.List() {
		if auth == nil || auth.FileName != name {
			continue
		}
		if target != nil {
			return nil, coreauth.ErrAuthRevisionConflict
		}
		target = auth
	}
	if target == nil {
		return nil, coreauth.ErrAuthNotFound
	}
	return target, nil
}

func writePriorityMutationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, coreauth.ErrAuthNotFound):
		writePriorityError(c, http.StatusNotFound, "auth_not_found", "auth file not found")
	case errors.Is(err, coreauth.ErrAuthRevisionConflict), errors.Is(err, coreauth.ErrAuthSourceConflict):
		writePriorityError(c, http.StatusConflict, "revision_conflict", "auth changed before mutation")
	case errors.Is(err, coreauth.ErrPriorityMutationRoutingIncompatible):
		writePriorityError(c, http.StatusUnprocessableEntity, "routing_incompatible", "priority mutation incompatible with active websocket routing")
	case errors.Is(err, coreauth.ErrPriorityMutationUnsupported):
		writePriorityError(c, http.StatusUnprocessableEntity, "priority_mutation_unsupported", "priority mutation unsupported")
	default:
		writePriorityError(c, http.StatusInternalServerError, "persistence_failed", "priority mutation failed")
	}
}

func writePriorityError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": message, "code": code})
}

func addPriorityManagementState(entry gin.H, auth *coreauth.Auth) {
	if entry == nil || auth == nil {
		return
	}
	entry["revision"] = auth.Revision()
	_, present := auth.Metadata["priority"]
	entry["priority_present"] = present
	if !present {
		entry["priority"] = 0
	} else if _, exists := entry["priority"]; !exists {
		entry["priority"] = runtimePriority(auth)
	}
}

func runtimePriority(auth *coreauth.Auth) int {
	if auth == nil || auth.Attributes == nil {
		return 0
	}
	value := strings.TrimSpace(auth.Attributes["priority"])
	if value == "" {
		return 0
	}
	priority, errPriority := strconv.Atoi(value)
	if errPriority != nil {
		return 0
	}
	return priority
}
