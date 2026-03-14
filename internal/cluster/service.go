package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const discoveryUserAgent = "CLIProxyAPI-Cluster/1.0"

// Service owns peer control-plane helpers and the remote catalog reconciler.
type Service struct {
	mu       sync.RWMutex
	cfg      configSnapshot
	runtime  map[string]*peerRuntime
	catalog  CatalogSnapshot
	onUpdate CatalogUpdateHandler

	started bool
	cancel  context.CancelFunc
	done    chan struct{}

	trigger chan struct{}
}

// NewService constructs a cluster service from the latest runtime config snapshot.
func NewService(cfg *internalconfig.Config) *Service {
	svc := &Service{
		trigger: make(chan struct{}, 1),
		runtime: make(map[string]*peerRuntime),
	}
	svc.applyConfigLocked(cfg)
	svc.catalog = emptyCatalogSnapshot(svc.cfg)
	return svc
}

// Start launches the background reconciliation loop.
func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	s.started = true
	s.mu.Unlock()

	go s.run(runCtx, done)
	s.triggerReconcile()
}

// Stop cancels the background reconciliation loop and waits for it to exit.
func (s *Service) Stop(context.Context) error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	cancel := s.cancel
	done := s.done
	s.cancel = nil
	s.done = nil
	s.started = false
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	return nil
}

// UpdateConfig swaps the active config snapshot and schedules an immediate reconcile.
func (s *Service) UpdateConfig(cfg *internalconfig.Config) {
	if s == nil {
		return
	}

	s.mu.Lock()
	s.applyConfigLocked(cfg)
	s.catalog = trimCatalogSnapshot(s.catalog, s.cfg)
	snapshot := cloneCatalogSnapshot(s.catalog)
	handler := s.onUpdate
	s.mu.Unlock()

	if handler != nil {
		handler(snapshot)
	}
	s.triggerReconcile()
}

// Snapshot returns a thread-safe copy of the current remote catalog.
func (s *Service) Snapshot() CatalogSnapshot {
	if s == nil {
		return CatalogSnapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneCatalogSnapshot(s.catalog)
}

// SetCatalogUpdateHandler registers a callback invoked after catalog snapshots change.
func (s *Service) SetCatalogUpdateHandler(handler CatalogUpdateHandler) {
	if s == nil {
		return
	}

	s.mu.Lock()
	s.onUpdate = handler
	snapshot := cloneCatalogSnapshot(s.catalog)
	s.mu.Unlock()

	if handler != nil {
		handler(snapshot)
	}
}

// ResolvePeer finds a configured peer by its node ID.
func (s *Service) ResolvePeer(targetID string) (internalconfig.ClusterNode, error) {
	if s == nil {
		return internalconfig.ClusterNode{}, ErrClusterDisabled
	}

	trimmedTarget := strings.TrimSpace(targetID)
	s.mu.RLock()
	snapshot := s.cfg
	s.mu.RUnlock()

	if !snapshot.Enabled {
		return internalconfig.ClusterNode{}, ErrClusterDisabled
	}
	for _, peer := range snapshot.Nodes {
		if peer.ID != trimmedTarget {
			continue
		}
		if !peer.Enabled {
			return internalconfig.ClusterNode{}, ErrPeerDisabled
		}
		return cloneClusterNode(peer), nil
	}
	return internalconfig.ClusterNode{}, ErrPeerNotFound
}

// ProxyManagementRequest forwards a selected management request to the target peer.
func (s *Service) ProxyManagementRequest(ctx context.Context, targetID, method, requestPath, rawQuery string, headers http.Header, body []byte) (*http.Response, error) {
	peer, err := s.ResolvePeer(targetID)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	timeout := s.cfg.ForwardTimeout
	client := s.newHTTPClientLocked(timeout)
	s.mu.RUnlock()

	forwardURL, err := joinURL(peer.ManagementURL, requestPath, stripTargetQuery(rawQuery))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, forwardURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyForwardHeaders(req.Header, headers)
	req.Header.Set("X-Management-Key", peer.ManagementKey)
	req.Header.Del("Authorization")
	req.Header.Del("X-Management-Key")
	req.Header.Set("X-Management-Key", peer.ManagementKey)
	return client.Do(req)
}

// FetchState retrieves a peer's cluster/state handshake payload.
func (s *Service) FetchState(ctx context.Context, peer internalconfig.ClusterNode) (State, error) {
	client := s.newHTTPClient(s.forwardTimeout())
	targetURL, err := joinURL(peer.ManagementURL, "/v0/management/cluster/state", "")
	if err != nil {
		return State{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return State{}, err
	}
	req.Header.Set("X-Management-Key", peer.ManagementKey)
	req.Header.Set("User-Agent", discoveryUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return State{}, fmt.Errorf("fetch cluster state from %s: %w", peer.ID, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return State{}, fmt.Errorf("fetch cluster state from %s: status %d: %s", peer.ID, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload State
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return State{}, fmt.Errorf("decode cluster state from %s: %w", peer.ID, err)
	}
	payload.NodeID = strings.TrimSpace(payload.NodeID)
	payload.AdvertiseURL = strings.TrimSpace(payload.AdvertiseURL)
	payload.Version = strings.TrimSpace(payload.Version)
	return payload, nil
}

// ListModels retrieves the peer's OpenAI-style /v1/models catalog using configured public API keys.
func (s *Service) ListModels(ctx context.Context, peer internalconfig.ClusterNode, advertiseURL string) ([]string, error) {
	client := s.newHTTPClient(s.forwardTimeout())
	targetURL, err := joinURL(advertiseURL, "/v1/models", "")
	if err != nil {
		return nil, err
	}

	order := s.apiKeyOrder(peer)
	var lastAuthErr error
	for _, key := range order {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("User-Agent", discoveryUserAgent)

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list /v1/models from %s: %w", peer.ID, err)
		}

		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read /v1/models from %s: %w", peer.ID, readErr)
		}

		switch resp.StatusCode {
		case http.StatusOK:
			models, err := parseModelIDs(body)
			if err != nil {
				return nil, fmt.Errorf("decode /v1/models from %s: %w", peer.ID, err)
			}
			s.setPreferredAPIKey(peer.ID, key)
			return models, nil
		case http.StatusUnauthorized, http.StatusForbidden:
			lastAuthErr = fmt.Errorf("list /v1/models from %s: public API key rejected", peer.ID)
			continue
		default:
			return nil, fmt.Errorf("list /v1/models from %s: status %d: %s", peer.ID, resp.StatusCode, strings.TrimSpace(string(body)))
		}
	}

	s.setPreferredAPIKey(peer.ID, "")
	if lastAuthErr != nil {
		return nil, lastAuthErr
	}
	return nil, fmt.Errorf("list /v1/models from %s: no usable API keys", peer.ID)
}

func (s *Service) run(ctx context.Context, done chan struct{}) {
	defer close(done)

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.trigger:
		case <-timer.C:
		}

		if err := s.reconcileOnce(ctx); err != nil && !contextCanceled(err) {
			log.WithError(err).Warn("cluster reconcile failed")
		}

		next := s.pollInterval()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(next)
	}
}

func (s *Service) triggerReconcile() {
	if s == nil {
		return
	}
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

func (s *Service) reconcileOnce(ctx context.Context) error {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	snapshot := s.cfg
	s.mu.RUnlock()

	catalog := emptyCatalogSnapshot(snapshot)
	catalog.UpdatedAt = time.Now().UTC()
	if !snapshot.Enabled {
		s.mu.Lock()
		s.catalog = catalog
		handler := s.onUpdate
		s.mu.Unlock()
		if handler != nil {
			handler(cloneCatalogSnapshot(catalog))
		}
		return nil
	}

	peersByID := make(map[string]internalconfig.ClusterNode, len(snapshot.Nodes))

	for _, peer := range snapshot.Nodes {
		if !peer.Enabled {
			continue
		}
		peersByID[peer.ID] = cloneClusterNode(peer)

		entry := PeerCatalog{
			ConfiguredID: peer.ID,
			CheckedAt:    time.Now().UTC(),
		}

		peerCtx, cancel := context.WithTimeout(ctx, snapshot.ForwardTimeout)
		state, err := s.FetchState(peerCtx, peer)
		cancel()
		if err != nil {
			entry.Error = err.Error()
			catalog.Peers[peer.ID] = entry
			continue
		}

		entry.NodeID = state.NodeID
		entry.AdvertiseURL = state.AdvertiseURL
		entry.Version = state.Version

		normalizedAdvertiseURL, err := validatePeerState(peer.ID, state, nil, nil)
		if err != nil {
			entry.Error = err.Error()
			catalog.Peers[peer.ID] = entry
			continue
		}
		entry.AdvertiseURL = normalizedAdvertiseURL
		catalog.Peers[peer.ID] = entry
	}
	catalog = markConflictingPeersInactive(catalog)

	for configuredID, entry := range catalog.Peers {
		if strings.TrimSpace(entry.Error) != "" {
			continue
		}
		if err := validatePeerVersion(buildinfo.Version, entry.Version); err != nil {
			entry.Error = err.Error()
			catalog.Peers[configuredID] = entry
			continue
		}

		peer, ok := peersByID[configuredID]
		if !ok {
			continue
		}

		peerCtx, cancel := context.WithTimeout(ctx, snapshot.ForwardTimeout)
		models, err := s.ListModels(peerCtx, peer, entry.AdvertiseURL)
		cancel()
		if err != nil {
			entry.Error = err.Error()
			catalog.Peers[configuredID] = entry
			continue
		}

		entry.Active = true
		entry.DiscoveredModels = cloneStrings(models)
		entry.FilteredModels = filterModels(peer.ModelListMode, peer.ModelList, models)
		catalog.Peers[configuredID] = entry
	}

	s.mu.Lock()
	s.catalog = catalog
	handler := s.onUpdate
	s.mu.Unlock()
	if handler != nil {
		handler(cloneCatalogSnapshot(catalog))
	}
	return nil
}

func (s *Service) applyConfigLocked(cfg *internalconfig.Config) {
	var snapshot configSnapshot
	if cfg != nil {
		snapshot.Enabled = cfg.Cluster.Enabled
		snapshot.NodeID = cfg.Cluster.NodeID
		snapshot.AdvertiseURL = cfg.Cluster.AdvertiseURL
		snapshot.PollInterval = time.Duration(cfg.Cluster.PollIntervalSeconds) * time.Second
		snapshot.ForwardTimeout = time.Duration(cfg.Cluster.ForwardTimeoutSeconds) * time.Second
		snapshot.PreferLocal = cfg.Cluster.PreferLocal
		snapshot.RegisterNodePrefixAlias = cfg.Cluster.RegisterNodePrefixAlias
		snapshot.ProxyURL = strings.TrimSpace(cfg.ProxyURL)
		if len(cfg.Cluster.Nodes) > 0 {
			snapshot.Nodes = make([]internalconfig.ClusterNode, 0, len(cfg.Cluster.Nodes))
			for _, peer := range cfg.Cluster.Nodes {
				snapshot.Nodes = append(snapshot.Nodes, cloneClusterNode(peer))
			}
		}
	}
	if snapshot.PollInterval <= 0 {
		snapshot.PollInterval = time.Duration(internalconfig.DefaultClusterPollSeconds) * time.Second
	}
	if snapshot.ForwardTimeout <= 0 {
		snapshot.ForwardTimeout = time.Duration(internalconfig.DefaultClusterTimeoutSeconds) * time.Second
	}

	nextRuntime := make(map[string]*peerRuntime, len(snapshot.Nodes))
	for _, peer := range snapshot.Nodes {
		if existing := s.runtime[peer.ID]; existing != nil {
			nextRuntime[peer.ID] = existing
			continue
		}
		nextRuntime[peer.ID] = &peerRuntime{}
	}

	s.cfg = snapshot
	s.runtime = nextRuntime
}

func (s *Service) pollInterval() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.PollInterval <= 0 {
		return time.Duration(internalconfig.DefaultClusterPollSeconds) * time.Second
	}
	return s.cfg.PollInterval
}

func (s *Service) forwardTimeout() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.ForwardTimeout
}

func (s *Service) newHTTPClient(timeout time.Duration) *http.Client {
	s.mu.RLock()
	client := s.newHTTPClientLocked(timeout)
	s.mu.RUnlock()
	return client
}

func (s *Service) newHTTPClientLocked(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = time.Duration(internalconfig.DefaultClusterTimeoutSeconds) * time.Second
	}
	client := &http.Client{Timeout: timeout}
	transport, _, err := proxyutil.BuildHTTPTransport(s.cfg.ProxyURL)
	if err != nil {
		log.WithError(err).Warn("cluster proxy transport setup failed")
	}
	if transport != nil {
		client.Transport = transport
	}
	return client
}

func (s *Service) apiKeyOrder(peer internalconfig.ClusterNode) []string {
	keys := cloneStrings(peer.APIKeys)
	if len(keys) == 0 {
		return nil
	}

	s.mu.RLock()
	preferred := ""
	if runtime := s.runtime[peer.ID]; runtime != nil {
		preferred = runtime.PreferredAPIKey
	}
	s.mu.RUnlock()

	if preferred == "" {
		return keys
	}

	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == preferred {
			out = append(out, key)
			break
		}
	}
	for _, key := range keys {
		if key == preferred {
			continue
		}
		out = append(out, key)
	}
	return out
}

func (s *Service) setPreferredAPIKey(peerID, apiKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runtime := s.runtime[peerID]
	if runtime == nil {
		runtime = &peerRuntime{}
		s.runtime[peerID] = runtime
	}
	runtime.PreferredAPIKey = apiKey
}

func validatePeerState(configuredID string, state State, seenNodeIDs, seenAdvertiseURLs map[string]string) (string, error) {
	if state.NodeID == "" {
		return "", fmt.Errorf("peer %s returned empty node_id", configuredID)
	}
	if state.NodeID != configuredID {
		return "", fmt.Errorf("peer %s returned mismatched node_id %q", configuredID, state.NodeID)
	}
	if state.AdvertiseURL == "" {
		return "", fmt.Errorf("peer %s returned empty advertise_url", configuredID)
	}
	normalizedAdvertiseURL, err := normalizePeerURL(state.AdvertiseURL)
	if err != nil {
		return "", fmt.Errorf("peer %s returned invalid advertise_url: %w", configuredID, err)
	}
	if existingPeer, exists := seenNodeIDs[state.NodeID]; exists && existingPeer != configuredID {
		return "", fmt.Errorf("peer %s conflicts with peer %s on node_id %q", configuredID, existingPeer, state.NodeID)
	}
	if existingNodeID, exists := seenAdvertiseURLs[normalizedAdvertiseURL]; exists && existingNodeID != state.NodeID {
		return "", fmt.Errorf("peer %s conflicts on advertise_url %q with node_id %q", configuredID, normalizedAdvertiseURL, existingNodeID)
	}
	return normalizedAdvertiseURL, nil
}

func validatePeerVersion(localVersion, remoteVersion string) error {
	localVersion = strings.TrimSpace(localVersion)
	remoteVersion = strings.TrimSpace(remoteVersion)
	if localVersion == "" || remoteVersion == "" {
		return fmt.Errorf("peer version must be present and exactly match the local version")
	}
	if localVersion != remoteVersion {
		return fmt.Errorf("peer version %q does not match local version %q", remoteVersion, localVersion)
	}
	return nil
}

func parseModelIDs(body []byte) ([]string, error) {
	var payload struct {
		Data   []map[string]any `json:"data"`
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	models := collectModelIDs(payload.Data)
	if len(models) == 0 {
		models = collectModelIDs(payload.Models)
	}
	return models, nil
}

func collectModelIDs(items []map[string]any) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		candidates := []string{stringValue(item["id"]), stringValue(item["name"])}
		for _, candidate := range candidates {
			if candidate == "" {
				continue
			}
			if _, exists := seen[candidate]; exists {
				break
			}
			seen[candidate] = struct{}{}
			out = append(out, candidate)
			break
		}
	}
	return out
}

func filterModels(mode string, filters, discovered []string) []string {
	if len(discovered) == 0 {
		return nil
	}
	if mode == "" {
		mode = internalconfig.ClusterModelListModeBlack
	}
	filterSet := make(map[string]struct{}, len(filters))
	for _, model := range filters {
		filterSet[model] = struct{}{}
	}

	out := make([]string, 0, len(discovered))
	for _, model := range discovered {
		_, exists := filterSet[model]
		switch mode {
		case internalconfig.ClusterModelListModeWhite:
			if exists {
				out = append(out, model)
			}
		default:
			if !exists {
				out = append(out, model)
			}
		}
	}
	return out
}

func stripTargetQuery(rawQuery string) string {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}
	values.Del("target")
	return values.Encode()
}

func joinURL(baseURL, requestPath, rawQuery string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(requestPath, "/") {
		requestPath = "/" + requestPath
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + requestPath
	parsed.RawQuery = rawQuery
	return parsed.String(), nil
}

func normalizePeerURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("unsupported URL scheme %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("URL host is required")
	}
	parsed.Scheme = scheme
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

func copyForwardHeaders(dst, src http.Header) {
	for key, values := range src {
		switch {
		case strings.EqualFold(key, "Authorization"):
			continue
		case strings.EqualFold(key, "X-Management-Key"):
			continue
		case strings.EqualFold(key, "Host"):
			continue
		case isHopByHopHeader(key):
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func isHopByHopHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "connection", "proxy-connection", "keep-alive", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func emptyCatalogSnapshot(cfg configSnapshot) CatalogSnapshot {
	return CatalogSnapshot{
		Enabled:      cfg.Enabled,
		NodeID:       cfg.NodeID,
		AdvertiseURL: cfg.AdvertiseURL,
		Peers:        make(map[string]PeerCatalog),
	}
}

func trimCatalogSnapshot(existing CatalogSnapshot, cfg configSnapshot) CatalogSnapshot {
	trimmed := emptyCatalogSnapshot(cfg)
	trimmed.UpdatedAt = existing.UpdatedAt
	if !cfg.Enabled {
		trimmed.UpdatedAt = time.Now().UTC()
		return trimmed
	}
	if len(existing.Peers) == 0 {
		return trimmed
	}
	allowed := make(map[string]struct{}, len(cfg.Nodes))
	for _, peer := range cfg.Nodes {
		if peer.Enabled {
			allowed[peer.ID] = struct{}{}
		}
	}
	for id, peer := range existing.Peers {
		if _, ok := allowed[id]; ok {
			trimmed.Peers[id] = clonePeerCatalog(peer)
		}
	}
	return trimmed
}

func cloneCatalogSnapshot(snapshot CatalogSnapshot) CatalogSnapshot {
	out := CatalogSnapshot{
		Enabled:      snapshot.Enabled,
		NodeID:       snapshot.NodeID,
		AdvertiseURL: snapshot.AdvertiseURL,
		UpdatedAt:    snapshot.UpdatedAt,
	}
	if len(snapshot.Peers) > 0 {
		out.Peers = make(map[string]PeerCatalog, len(snapshot.Peers))
		for id, peer := range snapshot.Peers {
			out.Peers[id] = clonePeerCatalog(peer)
		}
	}
	return out
}

func clonePeerCatalog(peer PeerCatalog) PeerCatalog {
	clone := peer
	clone.DiscoveredModels = cloneStrings(peer.DiscoveredModels)
	clone.FilteredModels = cloneStrings(peer.FilteredModels)
	return clone
}

func markConflictingPeersInactive(snapshot CatalogSnapshot) CatalogSnapshot {
	if len(snapshot.Peers) == 0 {
		return snapshot
	}

	nodeOwners := make(map[string][]string)
	advertiseOwners := make(map[string][]string)
	for configuredID, peer := range snapshot.Peers {
		if nodeID := strings.ToLower(strings.TrimSpace(peer.NodeID)); nodeID != "" {
			nodeOwners[nodeID] = append(nodeOwners[nodeID], configuredID)
		}
		if advertiseURL, err := normalizePeerURL(peer.AdvertiseURL); err == nil && advertiseURL != "" {
			advertiseOwners[advertiseURL] = append(advertiseOwners[advertiseURL], configuredID)
		}
	}

	markConflict := func(ids []string, message func(string, []string) string) {
		if len(ids) < 2 {
			return
		}
		sort.Strings(ids)
		for _, configuredID := range ids {
			peer := snapshot.Peers[configuredID]
			others := make([]string, 0, len(ids)-1)
			for _, otherID := range ids {
				if otherID == configuredID {
					continue
				}
				others = append(others, otherID)
			}
			peer.Active = false
			peer.FilteredModels = nil
			peer.Error = appendConflictError(peer.Error, message(configuredID, others))
			snapshot.Peers[configuredID] = peer
		}
	}

	for nodeID, ids := range nodeOwners {
		value := nodeID
		markConflict(ids, func(configuredID string, others []string) string {
			return fmt.Sprintf("peer %s conflicts with peer(s) %s on node_id %q", configuredID, strings.Join(others, ", "), value)
		})
	}
	for advertiseURL, ids := range advertiseOwners {
		value := advertiseURL
		markConflict(ids, func(configuredID string, others []string) string {
			return fmt.Sprintf("peer %s conflicts on advertise_url %q with peer(s) %s", configuredID, value, strings.Join(others, ", "))
		})
	}

	return snapshot
}

func appendConflictError(existing, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	switch {
	case existing == "":
		return next
	case next == "":
		return existing
	default:
		return existing + "; " + next
	}
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func cloneClusterNode(node internalconfig.ClusterNode) internalconfig.ClusterNode {
	clone := node
	clone.APIKeys = cloneStrings(node.APIKeys)
	clone.ModelList = cloneStrings(node.ModelList)
	return clone
}

func stringValue(value any) string {
	str, _ := value.(string)
	return strings.TrimSpace(str)
}

func contextCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
