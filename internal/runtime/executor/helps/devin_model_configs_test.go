package helps

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestParseDevinCliModelConfigs(t *testing.T) {
	entry := func(uid, label string, vendor, contextLength uint64, multimodal bool) []byte {
		var b []byte
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendString(b, label)
		if multimodal {
			b = protowire.AppendTag(b, 5, protowire.VarintType)
			b = protowire.AppendVarint(b, 1)
		}
		b = protowire.AppendTag(b, 10, protowire.VarintType)
		b = protowire.AppendVarint(b, vendor)
		// Unknown fields must be skipped.
		b = protowire.AppendTag(b, 12, protowire.BytesType)
		b = protowire.AppendString(b, "ignored")
		b = protowire.AppendTag(b, 18, protowire.VarintType)
		b = protowire.AppendVarint(b, contextLength)
		b = protowire.AppendTag(b, 22, protowire.BytesType)
		b = protowire.AppendString(b, uid)
		return b
	}
	var resp []byte
	for _, e := range [][]byte{
		entry("gpt-6-astra-high", "GPT-6 Astra High", 2, 400000, true),
		entry("", "No UID", 1, 1, false),
		entry("swe-2", "SWE-2", 1, 200000, false),
	} {
		resp = protowire.AppendTag(resp, 1, protowire.BytesType)
		resp = protowire.AppendBytes(resp, e)
	}
	resp = protowire.AppendTag(resp, 2, protowire.VarintType)
	resp = protowire.AppendVarint(resp, 7)

	got, err := ParseDevinCliModelConfigs(resp)
	if err != nil {
		t.Fatalf("ParseDevinCliModelConfigs() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[0].UID != "gpt-6-astra-high" || got[0].Label != "GPT-6 Astra High" || got[0].VendorID != 2 || got[0].ContextLength != 400000 || !got[0].Multimodal {
		t.Errorf("first model = %+v", got[0])
	}
	if got[1].UID != "swe-2" || got[1].Multimodal {
		t.Errorf("second model = %+v", got[1])
	}

	if _, err := ParseDevinCliModelConfigs([]byte{0xff}); err == nil {
		t.Error("expected error for malformed payload")
	}
}
