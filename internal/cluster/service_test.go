package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func testServiceConfig(nodes ...internalconfig.ClusterNode) *internalconfig.Config {
	return &internalconfig.Config{
		Cluster: internalconfig.ClusterConfig{
			Enabled:               true,
			NodeID:                "node-a",
			AdvertiseURL:          "http://node-a.example.com",
			PollIntervalSeconds:   internalconfig.DefaultClusterPollSeconds,
			ForwardTimeoutSeconds: internalconfig.DefaultClusterTimeoutSeconds,
			Nodes:                 nodes,
		},
	}
}

func TestFilterModels(t *testing.T) {
	t.Parallel()

	discovered := []string{"model-a", "model-b", "model-c"}

	if got := filterModels(internalconfig.ClusterModelListModeBlack, []string{"model-b"}, discovered); !slices.Equal(got, []string{"model-a", "model-c"}) {
		t.Fatalf("black filter = %v", got)
	}
	if got := filterModels(internalconfig.ClusterModelListModeWhite, []string{"model-b", "model-c"}, discovered); !slices.Equal(got, []string{"model-b", "model-c"}) {
		t.Fatalf("white filter = %v", got)
	}
	if got := filterModels(internalconfig.ClusterModelListModeWhite, nil, discovered); len(got) != 0 {
		t.Fatalf("white filter with empty allowlist = %v, want empty", got)
	}
}

func TestValidatePeerStateDuplicateNodeID(t *testing.T) {
	t.Parallel()

	_, err := validatePeerState("node-b", State{
		NodeID:       "node-b",
		AdvertiseURL: "http://node-b.example.com",
		Version:      buildinfo.Version,
	}, map[string]string{"node-b": "node-x"}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "conflicts with peer") {
		t.Fatalf("validatePeerState() error = %v", err)
	}
}

func TestValidatePeerStateDuplicateAdvertiseURL(t *testing.T) {
	t.Parallel()

	_, err := validatePeerState("node-c", State{
		NodeID:       "node-c",
		AdvertiseURL: "http://shared.example.com",
		Version:      buildinfo.Version,
	}, map[string]string{}, map[string]string{"http://shared.example.com": "node-b"})
	if err == nil || !strings.Contains(err.Error(), "conflicts on advertise_url") {
		t.Fatalf("validatePeerState() error = %v", err)
	}
}

func TestValidatePeerVersionRequiresExactMatch(t *testing.T) {
	t.Parallel()

	err := validatePeerVersion("v6.1.0", "v6.1.1")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("validatePeerVersion() error = %v", err)
	}
}

func TestValidatePeerVersionExactMatchOK(t *testing.T) {
	t.Parallel()

	if err := validatePeerVersion("v6.1.0", "v6.1.0"); err != nil {
		t.Fatalf("validatePeerVersion() error = %v", err)
	}
}

func TestServiceListModelsFallsBackToNextAPIKey(t *testing.T) {
	t.Parallel()

	var authHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		switch r.Header.Get("Authorization") {
		case "Bearer bad-key":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad key"}`))
		case "Bearer good-key":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model-a"},{"id":"model-b"}]}`))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()

	peer := internalconfig.ClusterNode{
		ID:            "node-b",
		Enabled:       true,
		ManagementURL: server.URL,
		ManagementKey: "peer-mgmt-key",
		APIKeys:       []string{"bad-key", "good-key"},
		ModelListMode: internalconfig.ClusterModelListModeBlack,
	}
	svc := NewService(testServiceConfig(peer))

	models, err := svc.ListModels(context.Background(), peer, server.URL)
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if !slices.Equal(models, []string{"model-a", "model-b"}) {
		t.Fatalf("ListModels() models = %v", models)
	}
	if !slices.Equal(authHeaders, []string{"Bearer bad-key", "Bearer good-key"}) {
		t.Fatalf("authorization order = %v", authHeaders)
	}

	authHeaders = nil
	models, err = svc.ListModels(context.Background(), peer, server.URL)
	if err != nil {
		t.Fatalf("second ListModels() error = %v", err)
	}
	if !slices.Equal(models, []string{"model-a", "model-b"}) {
		t.Fatalf("second ListModels() models = %v", models)
	}
	if len(authHeaders) == 0 || authHeaders[0] != "Bearer good-key" {
		t.Fatalf("preferred key not reused, authorization order = %v", authHeaders)
	}
}

func TestServiceReconcileOnceBuildsFilteredCatalog(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/cluster/state":
			if got := r.Header.Get("X-Management-Key"); got != "peer-mgmt-key" {
				t.Fatalf("management key = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(State{
				NodeID:       "node-b",
				AdvertiseURL: server.URL,
				Version:      buildinfo.Version,
			})
		case "/v1/models":
			if got := r.Header.Get("Authorization"); got != "Bearer peer-public-key" {
				t.Fatalf("public auth = %q", got)
			}
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model-a"},{"id":"model-b"},{"id":"model-c"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	peer := internalconfig.ClusterNode{
		ID:            "node-b",
		Enabled:       true,
		ManagementURL: server.URL,
		ManagementKey: "peer-mgmt-key",
		APIKeys:       []string{"peer-public-key"},
		ModelListMode: internalconfig.ClusterModelListModeWhite,
		ModelList:     []string{"model-b"},
	}

	svc := NewService(testServiceConfig(peer))
	if err := svc.reconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcileOnce() error = %v", err)
	}

	snapshot := svc.Snapshot()
	entry, ok := snapshot.Peers["node-b"]
	if !ok {
		t.Fatalf("snapshot missing peer node-b: %+v", snapshot.Peers)
	}
	if !entry.Active {
		t.Fatalf("peer entry should be active: %+v", entry)
	}
	if !slices.Equal(entry.DiscoveredModels, []string{"model-a", "model-b", "model-c"}) {
		t.Fatalf("discovered models = %v", entry.DiscoveredModels)
	}
	if !slices.Equal(entry.FilteredModels, []string{"model-b"}) {
		t.Fatalf("filtered models = %v", entry.FilteredModels)
	}
}

func TestServiceReconcileOnceMarksAdvertiseURLConflictInactive(t *testing.T) {
	t.Parallel()

	publicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model-a"}]}`))
	}))
	defer publicServer.Close()

	sharedAdvertiseURL := publicServer.URL
	newPeerServer := func(nodeID string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v0/management/cluster/state":
				_ = json.NewEncoder(w).Encode(State{
					NodeID:       nodeID,
					AdvertiseURL: sharedAdvertiseURL,
					Version:      buildinfo.Version,
				})
			default:
				http.NotFound(w, r)
			}
		}))
	}

	serverB := newPeerServer("node-b")
	defer serverB.Close()
	serverC := newPeerServer("node-c")
	defer serverC.Close()

	svc := NewService(testServiceConfig(
		internalconfig.ClusterNode{
			ID:            "node-b",
			Enabled:       true,
			ManagementURL: serverB.URL,
			ManagementKey: "key-b",
			APIKeys:       []string{"public-b"},
			ModelListMode: internalconfig.ClusterModelListModeBlack,
		},
		internalconfig.ClusterNode{
			ID:            "node-c",
			Enabled:       true,
			ManagementURL: serverC.URL,
			ManagementKey: "key-c",
			APIKeys:       []string{"public-c"},
			ModelListMode: internalconfig.ClusterModelListModeBlack,
		},
	))

	if err := svc.reconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcileOnce() error = %v", err)
	}

	snapshot := svc.Snapshot()
	if snapshot.Peers["node-b"].Active {
		t.Fatalf("node-b should be inactive on advertise_url conflict: %+v", snapshot.Peers["node-b"])
	}
	if snapshot.Peers["node-c"].Active {
		t.Fatalf("node-c should be inactive on advertise_url conflict: %+v", snapshot.Peers["node-c"])
	}
	if !strings.Contains(snapshot.Peers["node-b"].Error, "conflicts on advertise_url") {
		t.Fatalf("node-b error = %q", snapshot.Peers["node-b"].Error)
	}
	if !strings.Contains(snapshot.Peers["node-c"].Error, "conflicts on advertise_url") {
		t.Fatalf("node-c error = %q", snapshot.Peers["node-c"].Error)
	}
}

func TestServiceReconcileOnceMarksDuplicateNodeIDInactive(t *testing.T) {
	t.Parallel()

	newPeerServer := func(nodeID, advertiseURL string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v0/management/cluster/state":
				_ = json.NewEncoder(w).Encode(State{
					NodeID:       nodeID,
					AdvertiseURL: advertiseURL,
					Version:      buildinfo.Version,
				})
			case "/v1/models":
				_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model-a"}]}`))
			default:
				http.NotFound(w, r)
			}
		}))
	}

	serverB := newPeerServer("node-b", "http://node-b.example.com")
	defer serverB.Close()
	serverC := newPeerServer("node-b", "http://node-c.example.com")
	defer serverC.Close()

	svc := NewService(testServiceConfig(
		internalconfig.ClusterNode{
			ID:            "node-b",
			Enabled:       true,
			ManagementURL: serverB.URL,
			ManagementKey: "key-b",
			APIKeys:       []string{"public-b"},
			ModelListMode: internalconfig.ClusterModelListModeBlack,
		},
		internalconfig.ClusterNode{
			ID:            "node-c",
			Enabled:       true,
			ManagementURL: serverC.URL,
			ManagementKey: "key-c",
			APIKeys:       []string{"public-c"},
			ModelListMode: internalconfig.ClusterModelListModeBlack,
		},
	))

	if err := svc.reconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcileOnce() error = %v", err)
	}

	snapshot := svc.Snapshot()
	if snapshot.Peers["node-b"].Active {
		t.Fatalf("node-b should be inactive on node_id conflict: %+v", snapshot.Peers["node-b"])
	}
	if snapshot.Peers["node-c"].Active {
		t.Fatalf("node-c should be inactive on node_id conflict: %+v", snapshot.Peers["node-c"])
	}
	if !strings.Contains(snapshot.Peers["node-b"].Error, "conflicts with peer") {
		t.Fatalf("node-b error = %q", snapshot.Peers["node-b"].Error)
	}
	if !strings.Contains(snapshot.Peers["node-c"].Error, "conflicts with peer") {
		t.Fatalf("node-c error = %q", snapshot.Peers["node-c"].Error)
	}
}
