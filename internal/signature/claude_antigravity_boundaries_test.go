package signature

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestAntigravityCAQSNativeSanitizerRejectsPrefixedWrappers(t *testing.T) {
	sig := antigravityCAQSFixture(t)
	for _, test := range []struct {
		name, prefix    string
		wantDetected    SignatureProvider
		wantAntigravity bool
	}{
		{"bare", "", SignatureProviderClaude, true},
		{"single prefix", "claude#", SignatureProviderClaude, true},
		{"alias prefix", "anthropic#", SignatureProviderClaude, true},
		{"duplicate prefix", "claude#claude#", SignatureProviderUnknown, false},
		{"nested unknown prefix", "claude#junk#", SignatureProviderUnknown, false},
		{"nested alias prefix", "anthropic#cais#", SignatureProviderUnknown, false},
		{"unknown prefix", "junk#", SignatureProviderUnknown, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := test.prefix + sig
			if got := DetectSignatureProvider(raw); got != test.wantDetected {
				t.Errorf("detected = %q, want %q", got, test.wantDetected)
			}
			decision := DecideSignatureCompatibility(SignatureProviderClaude, raw, SignatureBlockKindClaudeThinking)
			if decision.Compatible || decision.Action != SignatureActionDropBlock || decision.NormalizedSignature != "" {
				t.Errorf("native Claude must reject Q wrapper: %+v", decision)
			}
			normalized, ok := CompatibleAntigravityClaudeThinkingSignature(raw)
			if ok != test.wantAntigravity || (ok && normalized != sig) || (!ok && normalized != "") {
				t.Errorf("Antigravity compatibility = %t, normalized length = %d", ok, len(normalized))
			}
			encoded, errMarshal := json.Marshal(raw)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reasoning","signature":` + string(encoded) + `},{"type":"text","text":"answer"}]}]}`)
			output, report := SanitizeClaudeMessagesForClaudeUpstream(payload, "claude-opus-5-5")
			parts := gjson.GetBytes(output, "messages.0.content").Array()
			if len(parts) != 1 || parts[0].Get("type").String() != "text" || parts[0].Get("text").String() != "answer" || report.DroppedBlocks != 1 || report.Preserved != 0 {
				t.Errorf("native sanitizer retained Google thinking wrapper: report=%+v", report)
			}
		})
	}
}

func TestAntigravityCAQSRejectsDuplicateSubmessages(t *testing.T) {
	inner, errDecode := base64.StdEncoding.DecodeString(antigravityCAQSFixture(t))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	raw, errDecode := base64.StdEncoding.DecodeString(string(inner))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	container, errExtract := extractClaudeBytesField(raw, 2, "container")
	if errExtract != nil {
		t.Fatal(errExtract)
	}
	channel, errExtract := extractClaudeBytesField(container, 1, "channel")
	if errExtract != nil {
		t.Fatal(errExtract)
	}
	bytesField := func(field protowire.Number, value []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, field, protowire.BytesType), value)
	}
	payload := func(containers ...[]byte) []byte {
		out := []byte{8, 4}
		for _, value := range containers {
			out = append(out, bytesField(2, value)...)
		}
		return append(out, 24, 1)
	}
	for _, test := range []struct {
		name         string
		firstChannel []byte
	}{
		{"malformed tag", []byte{0x80}},
		{"infrastructure wire type", []byte{0x12, 0x01, 0x02}},
		{"old signature slot", []byte{0x2a, 0x01, 0x01}},
		{"empty", nil},
		{"valid duplicate", channel},
	} {
		for _, scope := range []string{"channel", "container"} {
			t.Run(scope+"/"+test.name, func(t *testing.T) {
				first := bytesField(1, test.firstChannel)
				var duplicated []byte
				if scope == "channel" {
					duplicated = payload(append(first, container...))
				} else {
					duplicated = payload(first, container)
				}
				assertAntigravityCAQSDuplicateRejected(t, duplicated)
			})
		}
	}
	for _, first := range []struct {
		name  string
		value []byte
	}{
		{"malformed container", []byte{0x80}},
		{"empty container", nil},
		{"valid container duplicate", container},
	} {
		t.Run(first.name, func(t *testing.T) {
			assertAntigravityCAQSDuplicateRejected(t, payload(first.value, container))
		})
	}
}

func assertAntigravityCAQSDuplicateRejected(t *testing.T, raw []byte) {
	t.Helper()
	inner := base64.StdEncoding.EncodeToString(raw)
	// Keep legacy native CAIS/CAQS parsing behavior outside the new Q gate.
	if _, errInspect := InspectClaudeCAISSignature(inner); errInspect != nil {
		t.Fatalf("native parser behavior changed: %v", errInspect)
	}
	sig := base64.StdEncoding.EncodeToString([]byte(inner))
	if _, errInspect := InspectAntigravityClaudeCAQSSignature(sig); errInspect == nil {
		t.Error("duplicate submessage passed Q structural validation")
	}
	for _, opts := range []ClaudeSignatureValidationOptions{{}, {Strict: true}, {PrefixOnly: true}, {Base64Only: true}} {
		if IsValidClaudeThinkingSignature(sig, opts) {
			t.Errorf("duplicate submessage accepted in mode %+v", opts)
		}
		if normalized, errNormalize := NormalizeClaudeThinkingSignature(sig, opts); errNormalize == nil || normalized != "" {
			t.Errorf("duplicate submessage normalized in mode %+v", opts)
		}
	}
	if DetectSignatureProvider(sig) != SignatureProviderUnknown {
		t.Error("duplicate submessage classified as a valid signature")
	}
	if normalized, ok := CompatibleAntigravityClaudeThinkingSignature(sig); ok || normalized != "" {
		t.Error("duplicate submessage allowed for Antigravity replay")
	}
}
