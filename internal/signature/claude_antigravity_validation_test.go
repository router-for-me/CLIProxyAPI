package signature

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature/signaturetest"
	"google.golang.org/protobuf/encoding/protowire"
)

// Generate the envelope at test time; no captured signature files are needed.
func antigravityCAQSFixture(t *testing.T) string {
	t.Helper()
	return signaturetest.AntigravityCAQS()
}

func TestAntigravityCAQSRecognitionAndReplay(t *testing.T) {
	for _, channelID := range []uint64{16, 17, 18, 19, 20} {
		sig := signaturetest.AntigravityCAQSWithChannel(channelID)
		info, errInspect := InspectAntigravityClaudeCAQSSignature(sig)
		if errInspect != nil {
			t.Fatalf("channel %d: %v", channelID, errInspect)
		}
		if info.EnvelopeVersion != 4 || info.ChannelID != channelID || info.Infrastructure == nil || *info.Infrastructure != 2 || info.BlockKind != "thinking" || info.ModelText != "" || info.SignatureLen != 1020 || !info.SignatureInContainer {
			t.Fatalf("channel %d: unexpected envelope: %+v", channelID, info)
		}
		if !maybeSelfDescribingSignatureEnvelope(sig) || DetectSignatureProvider(sig) != SignatureProviderClaude {
			t.Fatalf("channel %d: double CAQS must be detected as Claude", channelID)
		}
		for _, opts := range []ClaudeSignatureValidationOptions{{}, {Strict: true}, {PrefixOnly: true}, {Base64Only: true}} {
			if !IsValidClaudeThinkingSignature(sig, opts) {
				t.Fatalf("channel %d: signature rejected in mode %+v", channelID, opts)
			}
			normalized, errNormalize := NormalizeClaudeThinkingSignature(sig, opts)
			if errNormalize != nil || normalized != sig {
				t.Fatalf("channel %d: normalization changed the wrapper: %v", channelID, errNormalize)
			}
		}
		if normalized, ok := CompatibleAntigravityClaudeThinkingSignature(sig); !ok || normalized != sig {
			t.Fatalf("channel %d: Antigravity replay must preserve the original bytes", channelID)
		}
		for _, target := range []SignatureProvider{SignatureProviderClaude, SignatureProviderGemini, SignatureProviderGPT, SignatureProviderKimi, SignatureProviderGrok} {
			if d := DecideSignatureCompatibility(target, sig, SignatureBlockKindClaudeThinking); d.Compatible {
				t.Errorf("channel %d: Google wrapper must not be replayed directly to %s", channelID, target)
			}
		}
		inner, _ := base64.StdEncoding.DecodeString(sig)
		if !IsValidClaudeCAISSignature(string(inner)) {
			t.Fatalf("channel %d: inner CAQS is invalid", channelID)
		}
		if _, ok := CompatibleAntigravityClaudeThinkingSignature(string(inner)); ok {
			t.Fatalf("channel %d: bare CAQS must not be promoted to Antigravity wire format", channelID)
		}
	}
}

func TestAntigravityCAQSRejectsMalformedWrappers(t *testing.T) {
	sig := antigravityCAQSFixture(t)
	inner, _ := base64.StdEncoding.DecodeString(sig)
	raw, _ := base64.StdEncoding.DecodeString(string(inner))
	wrap := func(b []byte) string {
		return base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString(b)))
	}
	wrongVersion := append([]byte(nil), raw...)
	wrongVersion[1] = 2
	wrongInfra := append([]byte(nil), raw...)
	// Locate the channel infrastructure field without touching the ciphertext.
	index := strings.Index(string(wrongInfra), string([]byte{8, 18, 16, 2, 24, 2}))
	if index < 0 {
		t.Fatal("fixture channel missing")
	}
	wrongInfra[index+3] = 1
	badField := append([]byte(nil), raw...)
	badField[index+2] = 18
	futureVersion := append([]byte(nil), raw...)
	futureVersion[1] = 5
	noChannelContainer, _ := extractClaudeBytesField(raw, 2, "container")
	noChannelBlock, _ := extractClaudeBytesField(noChannelContainer, 1, "channel")
	// Strip the leading channel_id field (tag 0x08, varint 18) from the channel block.
	noChannel := protowire.AppendTag(nil, 1, protowire.BytesType)
	noChannel = protowire.AppendBytes(noChannel, noChannelBlock[2:])
	noChannel = append(noChannel, noChannelContainer[2+len(noChannelBlock):]...)
	wrongChannel := protowire.AppendTag([]byte{8, 4}, 2, protowire.BytesType)
	wrongChannel = protowire.AppendBytes(wrongChannel, noChannel)
	// Build an otherwise valid CAQS with its signature in the old channel slot.
	misplaced := defaultClaudeCAISParts("claude-opus-5-5")
	misplaced.topEnvelope = 4
	misplaced.channelID = 18
	misplacedRaw, _ := base64.StdEncoding.DecodeString(misplaced.encode())
	container, _ := extractClaudeBytesField(misplacedRaw, 2, "container")
	channel, _ := extractClaudeBytesField(container, 1, "channel")
	channel = protowire.AppendTag(channel, 2, protowire.VarintType)
	channel = protowire.AppendVarint(channel, 2)
	newContainer := protowire.AppendTag(nil, 1, protowire.BytesType)
	newContainer = protowire.AppendBytes(newContainer, channel)
	misplacedPayload := []byte{8, 4}
	misplacedPayload = protowire.AppendTag(misplacedPayload, 2, protowire.BytesType)
	misplacedPayload = protowire.AppendBytes(misplacedPayload, newContainer)
	// Add a harmless unknown varint to make the inner encoding padded.
	paddedPayload := append(append([]byte(nil), raw...), 32, 0)
	badPadding := []byte(base64.StdEncoding.EncodeToString(paddedPayload))
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	padIndex := len(badPadding) - 1
	for padIndex >= 0 && badPadding[padIndex] == '=' {
		padIndex--
	}
	if padIndex == len(badPadding)-1 {
		t.Fatal("fixture must have padding")
	}
	badPadding[padIndex] = alphabet[strings.IndexByte(alphabet, badPadding[padIndex])|1]
	originalContainer, _ := extractClaudeBytesField(raw, 2, "container")
	originalChannel, _ := extractClaudeBytesField(originalContainer, 1, "channel")
	narrationChannel := []byte(strings.Replace(string(originalChannel), "\x42\x08thinking", "\x42\x09narration", 1))
	narrationContainer := protowire.AppendTag(nil, 1, protowire.BytesType)
	narrationContainer = protowire.AppendBytes(narrationContainer, narrationChannel)
	narrationContainer = append(narrationContainer, originalContainer[2+len(originalChannel):]...)
	narrationPayload := protowire.AppendTag([]byte{8, 4}, 2, protowire.BytesType)
	narrationPayload = protowire.AppendBytes(narrationPayload, narrationContainer)
	cases := map[string]string{
		"empty": "", "prefix only": "Q0FRUw==", "truncated": sig[:len(sig)-8],
		"triple encoding":     base64.StdEncoding.EncodeToString([]byte(sig)),
		"wrapped native CAQS": base64.StdEncoding.EncodeToString([]byte(observedFable51CAQSSample)),
		"wrapped native CAIS": base64.StdEncoding.EncodeToString([]byte(testClaudeCAISSignature("claude-opus-5"))),
		"version":             wrap(wrongVersion), "infra": wrap(wrongInfra), "infra wire type": wrap(badField),
		"future version": wrap(futureVersion), "channel": wrap(wrongChannel),
		"narration":            wrap(narrationPayload),
		"signature location":   wrap(misplacedPayload),
		"inner trailing space": base64.StdEncoding.EncodeToString(append(append([]byte(nil), inner...), ' ')),
		"inner trailing tab":   base64.StdEncoding.EncodeToString(append(append([]byte(nil), inner...), '\t')),
		"inner padding bits":   base64.StdEncoding.EncodeToString(badPadding),
		"outer newline":        sig[:8] + "\n" + sig[8:],
		"inner newline":        base64.StdEncoding.EncodeToString(append([]byte("CAQS\n"), inner[4:]...)),
		"inner label":          base64.StdEncoding.EncodeToString(append([]byte("Claude#"), inner...)),
		"inner whitespace":     base64.StdEncoding.EncodeToString(append([]byte(" "), inner...)),
		"oversize":             "Q" + strings.Repeat("A", MaxClaudeThinkingSignatureLen),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if _, errInspect := InspectAntigravityClaudeCAQSSignature(value); errInspect == nil {
				t.Fatal("malformed envelope accepted")
			}
			for _, opts := range []ClaudeSignatureValidationOptions{{}, {Strict: true}, {PrefixOnly: true}, {Base64Only: true}} {
				if IsValidClaudeThinkingSignature(value, opts) {
					t.Fatalf("invalid wrapper accepted in mode %+v", opts)
				}
			}
			if got, ok := CompatibleAntigravityClaudeThinkingSignature(value); ok || got != "" {
				t.Fatal("invalid wrapper replayable")
			}
		})
	}
}
