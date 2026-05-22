package deepseek

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestSolveAndBuildPowHeader(t *testing.T) {
	target := DeepSeekHashV1([]byte("salt_123_7"))
	header, err := SolveAndBuildPowHeader(context.Background(), PowChallenge{
		Algorithm:  "DeepSeekHashV1",
		Challenge:  hex.EncodeToString(target[:]),
		Salt:       "salt",
		ExpireAt:   123,
		Difficulty: 8,
		Signature:  "sig",
		TargetPath: "/api/v0/chat/completion",
	})
	if err != nil {
		t.Fatalf("SolveAndBuildPowHeader() error = %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := int(payload["answer"].(float64)); got != 7 {
		t.Fatalf("answer = %d, want 7", got)
	}
	if payload["target_path"] != "/api/v0/chat/completion" {
		t.Fatalf("target_path = %v", payload["target_path"])
	}
}
