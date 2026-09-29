package session

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

func TestHasExplicitSessionDoesNotRescanLargePayload(t *testing.T) {
	payload := []byte(`{"messages":["` + strings.Repeat("x", 1<<20) + `"]}`)

	singleLookup := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			root := util.ParseGJSONBytesNoCopy(payload)
			if root.Get("missing_session_id").Exists() {
				b.Fatal("unexpected session ID")
			}
		}
	})
	detection := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if hasExplicitSession(nil, payload) {
				b.Fatal("unexpected explicit session")
			}
		}
	})

	if detection.NsPerOp() > singleLookup.NsPerOp()*12 {
		t.Fatalf("explicit session detection rescans large payload: detection %d ns/op, single lookup %d ns/op", detection.NsPerOp(), singleLookup.NsPerOp())
	}
}

func TestExtractSessionInfoDoesNotRescanLargePayload(t *testing.T) {
	payload := []byte(`{"messages":["` + strings.Repeat("x", 1<<20) + `"]}`)

	singleLookup := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			root := util.ParseGJSONBytesNoCopy(payload)
			if root.Get("missing_session_id").Exists() {
				b.Fatal("unexpected session ID")
			}
		}
	})
	extraction := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if info, ok := ExtractSessionInfo(nil, payload, nil); ok {
				b.Fatalf("unexpected session: %q", info.SessionID)
			}
		}
	})

	// A request without session fields must not rescan a large message for every candidate key.
	if extraction.NsPerOp() > singleLookup.NsPerOp()*12 {
		t.Fatalf("session extraction rescans large payload: extraction %d ns/op, single lookup %d ns/op", extraction.NsPerOp(), singleLookup.NsPerOp())
	}
}
