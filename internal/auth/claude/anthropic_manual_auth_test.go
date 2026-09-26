package claude

import (
	"io"
	"net/http"
	"net/url"
	"testing"
)

// The expected values mirror the authorization URL Claude Code 2.1.223 opens when
// it cannot bind a local callback port.
func TestGenerateManualAuthURLMatchesNativeClaudeCodeFlow(t *testing.T) {
	auth := &ClaudeAuth{}

	rawURL, state, err := auth.GenerateManualAuthURL("state-value", &PKCECodes{CodeChallenge: "challenge", CodeVerifier: "verifier"})
	if err != nil {
		t.Fatalf("GenerateManualAuthURL() error = %v", err)
	}
	if state != "state-value" {
		t.Fatalf("state = %q, want state-value", state)
	}

	parsed, errParse := url.Parse(rawURL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	if got := parsed.Scheme + "://" + parsed.Host + parsed.Path; got != ManualAuthURL {
		t.Fatalf("authorize endpoint = %q, want %q", got, ManualAuthURL)
	}

	want := map[string]string{
		"code":                  "true",
		"client_id":             ClientID,
		"response_type":         "code",
		"redirect_uri":          ManualRedirectURI,
		"scope":                 "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload",
		"code_challenge":        "challenge",
		"code_challenge_method": "S256",
		"state":                 "state-value",
	}
	q := parsed.Query()
	for name, wantValue := range want {
		if got := q.Get(name); got != wantValue {
			t.Fatalf("%s = %q, want %q", name, got, wantValue)
		}
	}
	if len(q) != len(want) {
		t.Fatalf("query = %v, want exactly %d parameters", q, len(want))
	}

	// Byte-for-byte match against a URL captured from Claude Code 2.1.223, with the
	// per-login challenge and state substituted. url.Values.Encode would sort the
	// keys alphabetically and produce a query no native client ever sends.
	wantURL := ManualAuthURL + "?code=true" +
		"&client_id=" + ClientID +
		"&response_type=code" +
		"&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback" +
		"&scope=org%3Acreate_api_key+user%3Aprofile+user%3Ainference+user%3Asessions%3Aclaude_code+user%3Amcp_servers+user%3Afile_upload" +
		"&code_challenge=challenge" +
		"&code_challenge_method=S256" +
		"&state=state-value"
	if rawURL != wantURL {
		t.Fatalf("authorize URL =\n%s\nwant\n%s", rawURL, wantURL)
	}
}

// The local-callback flow must keep its own redirect_uri and scope.
func TestGenerateAuthURLKeepsLocalCallbackFlow(t *testing.T) {
	auth := &ClaudeAuth{}

	rawURL, _, err := auth.GenerateAuthURL("state-value", &PKCECodes{CodeChallenge: "challenge"})
	if err != nil {
		t.Fatalf("GenerateAuthURL() error = %v", err)
	}
	parsed, errParse := url.Parse(rawURL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	if got := parsed.Scheme + "://" + parsed.Host + parsed.Path; got != AuthURL {
		t.Fatalf("authorize endpoint = %q, want %q", got, AuthURL)
	}
	if got := parsed.Query().Get("redirect_uri"); got != RedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, RedirectURI)
	}
	if got := parsed.Query().Get("scope"); got != ClaudeOAuthScope {
		t.Fatalf("scope = %q, want %q", got, ClaudeOAuthScope)
	}
}

// Anthropic binds the code to the redirect_uri that produced it, so the manual
// exchange must send the hosted callback and split the pasted "<code>#<state>".
func TestExchangeCodeForTokensWithRedirectUsesManualRedirect(t *testing.T) {
	var tokenBody []byte

	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case TokenURL:
					body, errRead := io.ReadAll(req.Body)
					if errRead != nil {
						t.Fatal(errRead)
					}
					tokenBody = body
					return jsonResponse(req, `{"access_token":"access","refresh_token":"refresh","expires_in":28800}`), nil
				case ProfileURL:
					return jsonResponse(req, `{"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email":"user@example.com"},"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Example Org"}}`), nil
				case RolesURL:
					return jsonResponse(req, `{"roles":["claude_code_user"]}`), nil
				default:
					t.Fatalf("unexpected OAuth request URL %s", req.URL)
					return nil, nil
				}
			}),
		},
	}

	if _, err := auth.ExchangeCodeForTokensWithRedirect(t.Context(), "auth-code#state-value", "state-value", ManualRedirectURI, &PKCECodes{CodeVerifier: "verifier"}); err != nil {
		t.Fatalf("ExchangeCodeForTokensWithRedirect() error = %v", err)
	}

	wantBody := `{"grant_type":"authorization_code","code":"auth-code","redirect_uri":"` + ManualRedirectURI + `","client_id":"` + ClientID + `","code_verifier":"verifier","state":"state-value"}`
	if got := string(tokenBody); got != wantBody {
		t.Fatalf("exchange body = %q, want %q", got, wantBody)
	}
}

// An empty redirect_uri falls back to the local callback so existing callers keep
// their behavior.
func TestExchangeCodeForTokensWithRedirectDefaultsToLocalCallback(t *testing.T) {
	var tokenBody []byte

	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case TokenURL:
					body, errRead := io.ReadAll(req.Body)
					if errRead != nil {
						t.Fatal(errRead)
					}
					tokenBody = body
					return jsonResponse(req, `{"access_token":"access","refresh_token":"refresh","expires_in":28800}`), nil
				case ProfileURL:
					return jsonResponse(req, `{"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email":"user@example.com"},"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Example Org"}}`), nil
				case RolesURL:
					return jsonResponse(req, `{"roles":["claude_code_user"]}`), nil
				default:
					t.Fatalf("unexpected OAuth request URL %s", req.URL)
					return nil, nil
				}
			}),
		},
	}

	if _, err := auth.ExchangeCodeForTokensWithRedirect(t.Context(), "auth-code", "state-value", "", &PKCECodes{CodeVerifier: "verifier"}); err != nil {
		t.Fatalf("ExchangeCodeForTokensWithRedirect() error = %v", err)
	}

	wantBody := `{"grant_type":"authorization_code","code":"auth-code","redirect_uri":"` + RedirectURI + `","client_id":"` + ClientID + `","code_verifier":"verifier","state":"state-value"}`
	if got := string(tokenBody); got != wantBody {
		t.Fatalf("exchange body = %q, want %q", got, wantBody)
	}
}
