package wstunnel

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/MoeForge/moego/ojson"
	log "github.com/sirupsen/logrus"
)

const (
	headerTunnelRole = "X-Tunnel-Role"
	headerTunnelUUID = "X-Tunnel-UUID"
	headerTunnelName = "X-Tunnel-Name"
	headerTunnelType = "X-Tunnel-Type"
)

// ServeHTTP authenticates access.api-keys, validates device identity, upgrades
// the HTTP connection to WebSocket, and registers the peer in its isolated realm.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "tunnel hub unavailable")
		return
	}
	if r == nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if !strings.EqualFold(r.Method, http.MethodGet) {
		w.Header().Set("Allow", http.MethodGet)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var query url.Values
	if r.URL != nil {
		query = r.URL.Query()
	}

	apiKey, authStatus := h.authenticateRequest(r, query)
	if authStatus == http.StatusServiceUnavailable {
		writeJSONError(w, http.StatusServiceUnavailable, "tunnel hub is closed")
		return
	}
	if authStatus != 0 {
		writeJSONError(w, http.StatusUnauthorized, "invalid or missing api key")
		return
	}

	role, errRole := NormalizeAndValidateRole(firstNonEmptyTrimmed(query.Get("role"), r.Header.Get(headerTunnelRole)))
	if errRole != nil {
		writeJSONError(w, http.StatusBadRequest, errRole.Error())
		return
	}
	uuid, errUUID := NormalizeAndValidateUUID(firstNonEmptyTrimmed(query.Get("uuid"), r.Header.Get(headerTunnelUUID)))
	if errUUID != nil {
		writeJSONError(w, http.StatusBadRequest, errUUID.Error())
		return
	}
	name, errName := NormalizeAndValidateName(firstNonEmptyTrimmed(query.Get("name"), r.Header.Get(headerTunnelName)))
	if errName != nil {
		writeJSONError(w, http.StatusBadRequest, errName.Error())
		return
	}
	var serverType string
	if role == RoleServer {
		var errType error
		serverType, errType = NormalizeAndValidateType(firstNonEmptyTrimmed(query.Get("type"), r.Header.Get(headerTunnelType)))
		if errType != nil {
			writeJSONError(w, http.StatusBadRequest, errType.Error())
			return
		}
	}

	reservation, reserveStatus := h.reserveUUID(apiKey, uuid)
	if reserveStatus != 0 {
		switch reserveStatus {
		case http.StatusServiceUnavailable:
			writeJSONError(w, http.StatusServiceUnavailable, "tunnel hub is closed")
		case http.StatusUnauthorized:
			writeJSONError(w, http.StatusUnauthorized, "invalid or missing api key")
		default:
			writeJSONError(w, http.StatusConflict, "device uuid already connected")
		}
		return
	}

	h.mu.RLock()
	afterReserve := h.testOnAfterReserve
	h.mu.RUnlock()
	if afterReserve != nil {
		afterReserve()
	}

	guardedWriter := &deadlineGuardResponseWriter{ResponseWriter: w}
	conn, errUpgrade := h.upgrader.Upgrade(guardedWriter, r, nil)
	if errUpgrade != nil {
		h.releaseReservation(apiKey, uuid, reservation)
		log.Debugf("wstunnel: websocket upgrade failed: %v", errUpgrade)
		return
	}

	guardedWriter.conn.postUpgrade.Store(true)
	p := newPeer(h, apiKey, uuid, name, serverType, role, conn, h.sendQueueSize, h.maxPeerQueuedBytes)
	h.commitPeer(p, reservation)
}

func (h *Hub) authenticateRequest(r *http.Request, query url.Values) (string, int) {
	candidates := [5]string{
		extractBearerToken(r.Header.Get("Authorization")),
		strings.TrimSpace(r.Header.Get("X-Goog-Api-Key")),
		strings.TrimSpace(r.Header.Get("X-Api-Key")),
		strings.TrimSpace(query.Get("key")),
		strings.TrimSpace(query.Get("auth_token")),
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.closed {
		return "", http.StatusServiceUnavailable
	}
	if len(h.allowedKeys) == 0 {
		return "", http.StatusUnauthorized
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, ok := h.allowedKeys[candidate]; ok {
			return candidate, 0
		}
	}
	return "", http.StatusUnauthorized
}

func extractBearerToken(header string) string {
	trimmed := strings.TrimSpace(header)
	if trimmed == "" {
		return ""
	}
	parts := strings.SplitN(trimmed, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		return strings.TrimSpace(parts[1])
	}
	return trimmed
}

func firstNonEmptyTrimmed(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	doc := ojson.NewDocString("{}")
	_ = doc.Set("error", message)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_, _ = w.Write(doc.Bytes())
}
