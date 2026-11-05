package ollama

import (
	"context"
	"os"
	"testing"

	"github.com/joho/godotenv"
)

// Explicit opt-in only. The environment file is loaded by the process; neither
// its contents nor upstream account data are printed or retained in fixtures.
func TestLiveOfficialQuota(t *testing.T) {
	if os.Getenv("OLLAMA_LIVE_TEST") != "1" {
		t.Skip("live account check disabled")
	}
	if os.Getenv("OLLAMA_API_KEY") == "" {
		_ = godotenv.Load("../../../.env", "../../.env")
	}
	key := os.Getenv("OLLAMA_API_KEY")
	if key == "" {
		t.Fatal("OLLAMA_API_KEY is not available to the test process")
	}
	client := NewClient()
	for _, rng := range []UsageRange{UsageRange24h, UsageRange7d, UsageRange30d} {
		data, err := client.FetchUsage(context.Background(), key, rng, UsageScopeSelf)
		if err != nil {
			t.Fatalf("official usage %s: %s", rng, sanitizeErrorMessage(err))
		}
		if data.Range != string(rng) {
			t.Fatal("upstream range mismatch")
		}
		t.Logf("official usage %s: schema validated", rng)
	}
	if _, err := client.FetchBalance(context.Background(), key); err != nil {
		t.Fatalf("official balance: %s", sanitizeErrorMessage(err))
	}
	t.Log("official balance: schema validated; no account values retained")
}
