package discovery

import (
	"reflect"
	"strings"
	"testing"
)

func TestReviewBuildTXTStrictValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*TXTOptions)
	}{
		{"uppercase protocol", func(o *TXTOptions) { o.Protocols = []string{"Responses"} }},
		{"padded token", func(o *TXTOptions) { o.AuthMethods = []string{" api_key"} }},
		{"empty token", func(o *TXTOptions) { o.Protocols = []string{""} }},
		{"unicode identity", func(o *TXTOptions) { o.InstanceID = "é" }},
		{"control product", func(o *TXTOptions) { o.Product = "cpa\n" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := DefaultTXTOptions()
			tc.mutate(&o)
			if _, err := BuildTXTRecords(o); err == nil {
				t.Fatal("expected invalid optional value to fail")
			}
		})
	}
	o := DefaultTXTOptions()
	o.Protocols = []string{"future_2", strings.Repeat("a", 100)}
	o.Product = " product "
	records, err := BuildTXTRecords(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := ParseTXTRecords(records); got[txtKeyProtocols] != strings.Join(o.Protocols, ",") || got[txtKeyProduct] != o.Product {
		t.Fatalf("values changed: %#v", got)
	}
}

func TestReviewParseTXTKeys(t *testing.T) {
	t.Parallel()
	got := ParseTXTRecords([]string{" TLS=0", "TLS =0", "TLſ=0", "\x01tls=0", "TLS", "tls=1", "Path=/first", "PATH=/second"})
	if got["tls"] != "" || got["path"] != "/first" || got[" tls"] != "0" || got["tls "] != "0" {
		t.Fatalf("key spelling or first occurrence lost: %#v", got)
	}
	if _, ok := got["tlſ"]; ok {
		t.Fatal("non-ASCII key accepted")
	}
	var svc DiscoveredService
	if applyTXTMetadata(&svc, ParseTXTRecords([]string{"txtvers=1", "path=/", " TLS=0", "auth=1"})) {
		t.Fatal("padded key satisfied required field")
	}
}

func TestReviewMountPathRFC3986(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path string
		valid      bool
	}{
		{"pchar", "/AZaz09-._~!$&'()*+,;=:@/x", true},
		{"embedded dots", "/a..b/.hidden", true},
		{"brackets", "/a[b]", false}, {"quotes", "/a\"b", false},
		{"angle", "/a<b>", false}, {"pipe", "/a|b", false},
		{"caret", "/a^b", false}, {"backtick", "/a`b", false},
		{"braces", "/a{b}", false}, {"unicode", "/é", false},
		{"encoded", "/a%41", false}, {"scheme", "/a://b", false},
		{"dot segment", "/a/../b", false}, {"empty segment", "/a//b", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateMountPath(tc.path); (got != "") != tc.valid {
				t.Fatalf("validateMountPath(%q)=%q", tc.path, got)
			}
		})
	}
}

func TestReviewDraftOptionalValidation(t *testing.T) {
	t.Parallel()
	core := []string{"txtvers=1", "path=/ai", "tls=1", "auth=0"}
	for _, key := range []string{txtKeyProtocols, txtKeyAuthType, txtKeyProduct, txtKeyID, txtKeyRole} {
		for _, value := range []string{"", "x\x00", "é", "x\x7f"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				var svc DiscoveredService
				if applyTXTMetadata(&svc, ParseTXTRecords(append(append([]string{}, core...), key+"="+value))) {
					t.Fatalf("accepted invalid %s=%q", key, value)
				}
			})
		}
	}
	for _, value := range []string{"Responses", "responses,", "responses,,messages", ",responses", "responses, messages", "responses\t", "future.token"} {
		t.Run(value, func(t *testing.T) {
			var svc DiscoveredService
			if applyTXTMetadata(&svc, ParseTXTRecords(append(append([]string{}, core...), "protocols="+value))) {
				t.Fatalf("accepted invalid token list %q", value)
			}
		})
	}
	var svc DiscoveredService
	records := append(core, "protocols=responses,future_2,"+strings.Repeat("a", 100), "auth_type=api_key,future_auth", "product=  ")
	if !applyTXTMetadata(&svc, ParseTXTRecords(records)) || len(svc.Endpoints) != 1 || svc.Endpoints["responses"] != "/ai/v1/responses" || len(svc.Protocols) != 1 || len(svc.AuthMethods) != 1 || svc.AuthMethods[0] != "api_key" {
		t.Fatalf("valid unknown tokens rejected or given routes: %#v", svc)
	}
}

func TestReviewSchemaIsolationAndLegacyBases(t *testing.T) {
	t.Parallel()
	var svc DiscoveredService
	draft := []string{"txtvers=1", "path=/", "tls=0", "auth=1", "instance_id=legacy", "node_role=legacy", "auth_methods=api_key", "api_openai=/v1"}
	if !applyTXTMetadata(&svc, ParseTXTRecords(draft)) || svc.InstanceID != "" || svc.NodeRole != "" || len(svc.AuthMethods) != 0 || len(svc.Endpoints) != 0 {
		t.Fatalf("legacy leaked into draft: %#v", svc)
	}
	legacy := []string{"version=1", "tls=0", "auth_required=false", "api_openai=/v1", "api_anthropic=/bad/../path", "api_gemini=/proxy/v1beta", "id=draft", "role=draft", "auth_type=oauth", "path=/ignored"}
	if !applyTXTMetadata(&svc, ParseTXTRecords(legacy)) {
		t.Fatal("legacy without protocols rejected")
	}
	want := map[string]string{legacyKeyOpenAI: "/v1", legacyKeyGemini: "/proxy/v1beta"}
	if !reflect.DeepEqual(svc.Endpoints, want) || len(svc.Protocols) != 0 || svc.InstanceID != "" || svc.NodeRole != "" || len(svc.AuthMethods) != 0 || svc.Path != "" {
		t.Fatalf("legacy invented capability or used draft fields: %#v", svc)
	}
	if applyTXTMetadata(&svc, ParseTXTRecords(append(legacy, "txtvers=2"))) {
		t.Fatal("unsupported draft fell back to legacy")
	}
	if applyTXTMetadata(&svc, ParseTXTRecords(append(legacy, "txtvers=1"))) {
		t.Fatal("invalid draft fell back to legacy")
	}
}

func TestReviewDraftWireBudget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		records []string
		valid   bool
	}{
		{"exact string", []string{strings.Repeat("a", 255)}, true},
		{"oversize string", []string{strings.Repeat("a", 256)}, false},
		{"exact payload", []string{strings.Repeat("a", 255), strings.Repeat("b", 143)}, true},
		{"oversize payload", []string{strings.Repeat("a", 255), strings.Repeat("b", 144)}, false},
		{"duplicates count", []string{strings.Repeat("a", 199), strings.Repeat("a", 199), ""}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateDraftTXTRecords(tc.records); got != tc.valid {
				t.Fatalf("wire validation=%v, want %v", got, tc.valid)
			}
		})
	}
}
