package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCodexFingerprintUpdaterBuildsCompleteProfile(t *testing.T) {
	server := newCodexFingerprintFixtureServer(t, nil)
	defer server.Close()

	profile, err := fetchCodexFingerprintProfile(
		context.Background(),
		server.Client(),
		codexFingerprintFixtureSources(server.URL),
		validTestCodexFingerprintProfile(),
	)
	if err != nil {
		t.Fatalf("fetchCodexFingerprintProfile() error = %v", err)
	}
	if profile.Version != "0.147.0" {
		t.Fatalf("version = %q, want 0.147.0", profile.Version)
	}
	if profile.SourceRevision != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("source revision = %q", profile.SourceRevision)
	}
	if profile.Originator != "codex_cli_rs" {
		t.Fatalf("originator = %q", profile.Originator)
	}
	if profile.WebsocketBeta != "responses_websockets=2026-03-01" {
		t.Fatalf("websocket beta = %q", profile.WebsocketBeta)
	}
	if profile.Headers.ParentThreadID != "x-codex-parent-thread-id" {
		t.Fatalf("parent thread header = %q", profile.Headers.ParentThreadID)
	}
	if profile.Headers.SessionID != "session-id" || profile.Headers.ThreadID != "thread-id" {
		t.Fatalf("session/thread headers = %q/%q", profile.Headers.SessionID, profile.Headers.ThreadID)
	}
	if profile.MetadataKeys.TurnStartedAtUnixMS != "turn_started_at_unix_ms" {
		t.Fatalf("turn start metadata key = %q", profile.MetadataKeys.TurnStartedAtUnixMS)
	}
	if err = validateCodexFingerprintProfile(profile); err != nil {
		t.Fatalf("updated profile validation error = %v", err)
	}
}

func TestCodexFingerprintUpdaterRejectsPartialSourceFailure(t *testing.T) {
	server := newCodexFingerprintFixtureServer(t, map[string]int{"/client": http.StatusBadGateway})
	defer server.Close()

	_, err := fetchCodexFingerprintProfile(
		context.Background(),
		server.Client(),
		codexFingerprintFixtureSources(server.URL),
		validTestCodexFingerprintProfile(),
	)
	if err == nil || !strings.Contains(err.Error(), "client source") {
		t.Fatalf("fetch error = %v, want client source failure", err)
	}
}

func TestCodexFingerprintUpdaterRejectsMalformedOfficialContract(t *testing.T) {
	server := newCodexFingerprintFixtureServer(t, nil)
	defer server.Close()
	sources := codexFingerprintFixtureSources(server.URL)
	sources.DefaultClientURL = server.URL + "/missing-originator"

	_, err := fetchCodexFingerprintProfile(
		context.Background(),
		server.Client(),
		sources,
		validTestCodexFingerprintProfile(),
	)
	if err == nil || !strings.Contains(err.Error(), "DEFAULT_ORIGINATOR") {
		t.Fatalf("fetch error = %v, want missing DEFAULT_ORIGINATOR", err)
	}
}

func TestCodexFingerprintUpdaterRejectsOversizedSource(t *testing.T) {
	server := newCodexFingerprintFixtureServer(t, nil)
	defer server.Close()
	sources := codexFingerprintFixtureSources(server.URL)
	sources.ClientURL = server.URL + "/oversized"

	_, err := fetchCodexFingerprintProfile(
		context.Background(),
		server.Client(),
		sources,
		validTestCodexFingerprintProfile(),
	)
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("fetch error = %v, want source size error", err)
	}
}

func TestCodexFingerprintUpdaterUnchangedProfileKeepsRevision(t *testing.T) {
	profile := validTestCodexFingerprintProfile()
	store := newCodexFingerprintProfileStore(profile)
	changed, err := store.update(profile)
	if err != nil {
		t.Fatalf("store.update() error = %v", err)
	}
	if changed {
		t.Fatal("store.update() changed = true, want false")
	}
	if _, revision := store.snapshot(); revision != 1 {
		t.Fatalf("revision = %d, want 1", revision)
	}
}

func TestPinCodexFingerprintSourcesToRevision(t *testing.T) {
	t.Parallel()

	sources := codexFingerprintSourceSet{
		DefaultClientURL:     "https://raw.githubusercontent.com/openai/codex/main/codex-rs/login/src/auth/default_client.rs",
		ClientURL:            "https://raw.githubusercontent.com/openai/codex/main/codex-rs/core/src/client.rs",
		ResponsesMetadataURL: "https://raw.githubusercontent.com/openai/codex/main/codex-rs/core/src/responses_metadata.rs",
		RequestHeadersURL:    "https://raw.githubusercontent.com/openai/codex/main/codex-rs/codex-api/src/requests/headers.rs",
		DistTagsURL:          "https://registry.npmjs.org/-/package/@openai%2Fcodex/dist-tags",
	}

	pinned := pinCodexFingerprintSourcesToRevision(sources, "0123456789abcdef")
	for name, sourceURL := range map[string]string{
		"default client":     pinned.DefaultClientURL,
		"client":             pinned.ClientURL,
		"responses metadata": pinned.ResponsesMetadataURL,
		"request headers":    pinned.RequestHeadersURL,
	} {
		if !strings.Contains(sourceURL, "/openai/codex/0123456789abcdef/") {
			t.Fatalf("%s URL was not pinned: %s", name, sourceURL)
		}
	}
	if pinned.DistTagsURL != sources.DistTagsURL {
		t.Fatalf("NPM URL changed: %s", pinned.DistTagsURL)
	}
}

func codexFingerprintFixtureSources(baseURL string) codexFingerprintSourceSet {
	return codexFingerprintSourceSet{
		DistTagsURL:          baseURL + "/npm",
		DefaultClientURL:     baseURL + "/default-client",
		ClientURL:            baseURL + "/client",
		ResponsesMetadataURL: baseURL + "/responses-metadata",
		RequestHeadersURL:    baseURL + "/request-headers",
		CommitURL:            baseURL + "/commit",
	}
}

func newCodexFingerprintFixtureServer(t *testing.T, statuses map[string]int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status := statuses[r.URL.Path]; status != 0 {
			http.Error(w, http.StatusText(status), status)
			return
		}
		switch r.URL.Path {
		case "/npm":
			fmt.Fprint(w, `{"latest":"0.147.0"}`)
		case "/default-client":
			fmt.Fprint(w, `
pub const DEFAULT_ORIGINATOR: &str = "codex_cli_rs";
pub fn get_codex_user_agent() -> String {
    let build_version = env!("CARGO_PKG_VERSION");
    format!("{}/{build_version}", DEFAULT_ORIGINATOR)
}`)
		case "/client":
			fmt.Fprint(w, `
pub const X_CODEX_INSTALLATION_ID_HEADER: &str = "x-codex-installation-id";
pub const X_CODEX_TURN_STATE_HEADER: &str = "x-codex-turn-state";
pub const X_CODEX_TURN_METADATA_HEADER: &str = "x-codex-turn-metadata";
pub const X_CODEX_PARENT_THREAD_ID_HEADER: &str = "x-codex-parent-thread-id";
pub const X_CODEX_WINDOW_ID_HEADER: &str = "x-codex-window-id";
pub const X_OPENAI_SUBAGENT_HEADER: &str = "x-openai-subagent";
pub const X_RESPONSESAPI_INCLUDE_TIMING_METRICS_HEADER: &str = "x-responsesapi-include-timing-metrics";
const RESPONSES_WEBSOCKETS_V2_BETA_HEADER_VALUE: &str = "responses_websockets=2026-03-01";
headers.insert("x-client-request-id", header_value);`)
		case "/responses-metadata":
			fmt.Fprint(w, `
pub(crate) const INSTALLATION_ID_KEY: &str = "installation_id";
pub(crate) const SESSION_ID_KEY: &str = "session_id";
pub(crate) const THREAD_ID_KEY: &str = "thread_id";
pub(crate) const TURN_ID_KEY: &str = "turn_id";
pub(crate) const WINDOW_ID_KEY: &str = "window_id";
pub(crate) const REQUEST_KIND_KEY: &str = "request_kind";
pub(crate) const TURN_STARTED_AT_UNIX_MS_KEY: &str = "turn_started_at_unix_ms";
pub(crate) const PARENT_THREAD_ID_KEY: &str = "parent_thread_id";
pub(crate) const PARENT_TURN_ID_KEY: &str = "parent_turn_id";
pub(crate) const SUBAGENT_KIND_KEY: &str = "subagent_kind";`)
		case "/request-headers":
			fmt.Fprint(w, `
pub fn build_session_headers(session_id: Option<String>, thread_id: Option<String>) -> HeaderMap {
    let mut headers = HeaderMap::new();
    if let Some(id) = session_id {
        insert_header(&mut headers, "session-id", &id);
    }
    if let Some(id) = thread_id {
        insert_header(&mut headers, "thread-id", &id);
    }
    headers
}`)
		case "/commit":
			fmt.Fprint(w, `{"sha":"0123456789abcdef0123456789abcdef01234567"}`)
		case "/missing-originator":
			fmt.Fprint(w, `pub fn get_codex_user_agent() -> String { env!("CARGO_PKG_VERSION").to_string() }`)
		case "/oversized":
			fmt.Fprint(w, strings.Repeat("x", maxCodexFingerprintSourceSize+1))
		default:
			http.NotFound(w, r)
		}
	}))
}
