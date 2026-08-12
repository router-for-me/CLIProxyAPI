package managementdashboard

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedAssets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
	}{
		{name: DashboardAsset, contentType: "text/html; charset=utf-8"},
		{name: StylesheetAsset, contentType: "text/css; charset=utf-8"},
		{name: ScriptAsset, contentType: "text/javascript; charset=utf-8"},
		{name: LauncherAsset, contentType: "text/css; charset=utf-8"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data, contentType, ok := Asset(tt.name)
			if !ok {
				t.Fatalf("Asset(%q) was not found", tt.name)
			}
			if len(bytes.TrimSpace(data)) == 0 {
				t.Fatalf("Asset(%q) is empty", tt.name)
			}
			if contentType != tt.contentType {
				t.Fatalf("Asset(%q) content type = %q, want %q", tt.name, contentType, tt.contentType)
			}
		})
	}
}

func TestAssetRejectsNonExactNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "Dashboard.html", "/dashboard.html", "usage/dashboard.html", "../dashboard.html"} {
		data, contentType, ok := Asset(name)
		if ok || data != nil || contentType != "" {
			t.Fatalf("Asset(%q) = (%q, %q, %t), want an exact-name rejection", name, data, contentType, ok)
		}
	}
}

func TestAssetReturnsIndependentCopies(t *testing.T) {
	t.Parallel()

	first := DashboardHTML()
	if len(first) == 0 {
		t.Fatal("DashboardHTML() is empty")
	}
	original := first[0]
	first[0] ^= 0xff
	second := DashboardHTML()
	if second[0] != original {
		t.Fatal("DashboardHTML() returned mutable embedded storage")
	}
}

func TestDashboardAssetsUseOnlySafeLocalEndpoints(t *testing.T) {
	t.Parallel()

	combined := allAssets(t)
	for _, endpoint := range []string{
		"/v0/management/api-key-profiles",
		"/v0/management/client-key-usage",
		"/v0/management/usage/export.csv",
	} {
		if !bytes.Contains(combined, []byte(endpoint)) {
			t.Errorf("dashboard assets do not reference required endpoint %q", endpoint)
		}
	}

	externalReference := regexp.MustCompile(`(?i)https?://`)
	if match := externalReference.Find(combined); match != nil {
		t.Fatalf("dashboard assets contain an external HTTP reference: %q", match)
	}

	fixedSecretPatterns := []*regexp.Regexp{
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`),
		regexp.MustCompile(`(?i)authorization\s*:\s*['"]bearer\s+[A-Za-z0-9._-]{8,}`),
		regexp.MustCompile(`(?i)(?:secret|password|api[_-]?key)\s*[:=]\s*['"][^'"$]{8,}['"]`),
	}
	for _, pattern := range fixedSecretPatterns {
		if match := pattern.Find(combined); match != nil {
			t.Fatalf("dashboard assets appear to contain a fixed secret: %q", match)
		}
	}
}

func TestInjectLauncher(t *testing.T) {
	t.Parallel()

	page := []byte("<!doctype html><html><head><title>Management</title></head><body><main>official panel</main></body></html>")
	got := InjectLauncher(page)
	if bytes.Equal(got, page) {
		t.Fatal("InjectLauncher() did not change an HTML document")
	}

	launcherAt := bytes.Index(got, []byte(launcherMarker))
	bodyEndAt := bytes.Index(bytes.ToLower(got), []byte("</body"))
	if launcherAt < 0 || bodyEndAt < 0 || launcherAt >= bodyEndAt {
		t.Fatalf("launcher was not inserted before </body>: %q", got)
	}
	for _, required := range []string{
		`href="/management/usage/launcher.css"`,
		`href="/management/usage"`,
		`target="_blank"`,
		`rel="noopener noreferrer"`,
		`class="cliproxy-usage-launcher"`,
	} {
		if !bytes.Contains(got, []byte(required)) {
			t.Errorf("injected launcher is missing %q", required)
		}
	}
	if !bytes.Equal(page, []byte("<!doctype html><html><head><title>Management</title></head><body><main>official panel</main></body></html>")) {
		t.Fatal("InjectLauncher() mutated its input")
	}
}

func TestInjectLauncherIsCaseInsensitiveAndIdempotent(t *testing.T) {
	t.Parallel()

	page := []byte("\xef\xbb\xbf  <!DOCTYPE HTML><HTML><HEAD><TITLE>İ用量</TITLE></HEAD><BODY class=\"app\">panel</BODY></HTML>")
	once := InjectLauncher(page)
	twice := InjectLauncher(once)
	if !bytes.Equal(twice, once) {
		t.Fatal("InjectLauncher() duplicated or changed an existing launcher")
	}
	if count := bytes.Count(once, []byte(`data-cliproxy-usage-launcher="link"`)); count != 1 {
		t.Fatalf("injected launcher count = %d, want 1", count)
	}
	if !bytes.Contains(once, []byte("<TITLE>İ用量</TITLE>")) {
		t.Fatal("InjectLauncher() corrupted non-ASCII content")
	}
}

func TestInjectLauncherLeavesNonDocumentsUnchanged(t *testing.T) {
	t.Parallel()

	tests := map[string][]byte{
		"nil":             nil,
		"empty":           {},
		"plain text":      []byte("plain text </body>"),
		"json":            []byte(`{"value":"<html><body>not a document</body></html>"}`),
		"body fragment":   []byte("<body>fragment</body>"),
		"bodyless html":   []byte("<!doctype html><html><main>fragment</main></html>"),
		"similar tags":    []byte("<htmlish><bodyguard>text</bodyguard></htmlish>"),
		"unclosed body":   []byte("<!doctype html><html><body>text</html>"),
		"existing marker": []byte("<!doctype html><html><body><span data-cliproxy-usage-launcher></span></body></html>"),
	}
	for name, input := range tests {
		name := name
		input := input
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			before := append([]byte(nil), input...)
			got := InjectLauncher(input)
			if !bytes.Equal(got, before) {
				t.Fatalf("InjectLauncher() changed %s input:\n%s", name, strings.TrimSpace(string(got)))
			}
		})
	}
}

func allAssets(t *testing.T) []byte {
	t.Helper()
	var combined []byte
	for _, name := range []string{DashboardAsset, StylesheetAsset, ScriptAsset, LauncherAsset} {
		data, _, ok := Asset(name)
		if !ok {
			t.Fatalf("Asset(%q) was not found", name)
		}
		combined = append(combined, data...)
		combined = append(combined, '\n')
	}
	return combined
}
