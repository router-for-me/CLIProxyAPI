package helps

import (
	"github.com/MoeForge/moego/base64"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func testVertexBareTinkInteractionsSignature() string {
	payload := []byte{0x01, 0x8f, 0x3d, 0x6b, 0x5f}
	for len(payload) < 405 {
		payload = append(payload, byte(0xA0+len(payload)%0x20))
	}
	return base64.StdEncoding.EncodeToString(payload)
}

func TestSanitizeGeminiInteractionsUnsupportedInputIDs_ScopesVertexBareTink(t *testing.T) {
	sig := testVertexBareTinkInteractionsSignature()
	body := []byte(`{"input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"thought","signature":"` + sig + `"}]}`)

	public := SanitizeGeminiInteractionsUnsupportedInputIDs(body, false)
	if ojson.GetBytes(public, `input.#(type=="thought")`).Exists() {
		t.Fatalf("public Interactions endpoint must drop the Vertex bare-Tink thought: %s", public)
	}

	vertex := SanitizeGeminiInteractionsUnsupportedInputIDs(body, true)
	if got := ojson.GetBytes(vertex, `input.#(type=="thought").signature`).String(); got != sig {
		t.Fatalf("Vertex Interactions endpoint must keep the bare-Tink signature, got %q", got)
	}
}
