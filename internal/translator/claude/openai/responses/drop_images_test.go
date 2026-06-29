package responses

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestStripOldImages_KeepsLastNUserTurns(t *testing.T) {
	big := strings.Repeat("A", 1024)
	out := []byte(`{"messages":[]}`)

	// Create 8 user turns with images, interleaved with assistant messages.
	for i := 0; i < 8; i++ {
		// user with image
		m := []byte(`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}},{"type":"text","text":""}]}`)
		m, _ = sjson.SetBytes(m, "content.0.source.data", big)
		m, _ = sjson.SetBytes(m, "content.1.text", strings.Repeat("x", i))
		out, _ = sjson.SetRawBytes(out, "messages.-1", m)
		// assistant reply
		out, _ = sjson.SetRawBytes(out, "messages.-1", []byte(`{"role":"assistant","content":[{"type":"text","text":"ok"}]}`))
	}

	got := stripOldImages(out)

	// 8 user turns, keep last 6 => first 2 user turns (msg index 0, 2) should be stripped
	// msg[0] = user turn 0 -> stripped
	if ty := gjson.GetBytes(got, "messages.0.content.0.type").String(); ty != "text" {
		t.Fatalf("user turn 0 image should be stripped, got type=%s", ty)
	}
	if txt := gjson.GetBytes(got, "messages.0.content.0.text").String(); !strings.Contains(txt, "omitted") {
		t.Fatalf("user turn 0 should have placeholder, got %s", txt)
	}
	// msg[2] = user turn 1 -> stripped
	if ty := gjson.GetBytes(got, "messages.2.content.0.type").String(); ty != "text" {
		t.Fatalf("user turn 1 image should be stripped, got type=%s", ty)
	}

	// msg[4] = user turn 2 -> kept (part of last 6)
	if ty := gjson.GetBytes(got, "messages.4.content.0.type").String(); ty != "image" {
		t.Fatalf("user turn 2 image should be kept, got type=%s", ty)
	}
	// msg[14] = user turn 7 (last) -> kept
	if ty := gjson.GetBytes(got, "messages.14.content.0.type").String(); ty != "image" {
		t.Fatalf("last user turn image should be kept, got type=%s", ty)
	}
}

func TestStripOldImages_FewerThanNTurns(t *testing.T) {
	big := strings.Repeat("A", 1024)
	out := []byte(`{"messages":[]}`)

	// Only 3 user turns (< 6), all images should survive
	for i := 0; i < 3; i++ {
		m := []byte(`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}]}`)
		m, _ = sjson.SetBytes(m, "content.0.source.data", big)
		out, _ = sjson.SetRawBytes(out, "messages.-1", m)
		out, _ = sjson.SetRawBytes(out, "messages.-1", []byte(`{"role":"assistant","content":[{"type":"text","text":"ok"}]}`))
	}

	got := stripOldImages(out)

	for i := 0; i < 3; i++ {
		idx := i * 2
		if ty := gjson.GetBytes(got, "messages."+string(rune('0'+idx))+".content.0.type").String(); ty != "image" {
			t.Fatalf("user turn %d image should be kept (fewer than 6 turns), got type=%s", i, ty)
		}
	}
}

func TestStripOldImages_NestedToolResult(t *testing.T) {
	big := strings.Repeat("A", 1024)
	out := []byte(`{"messages":[]}`)

	// 8 user turns, images in tool_result
	for i := 0; i < 8; i++ {
		tr := []byte(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}]}]}`)
		tr, _ = sjson.SetBytes(tr, "content.0.content.0.source.data", big)
		out, _ = sjson.SetRawBytes(out, "messages.-1", tr)
		out, _ = sjson.SetRawBytes(out, "messages.-1", []byte(`{"role":"assistant","content":[{"type":"text","text":"ok"}]}`))
	}

	got := stripOldImages(out)

	// First 2 user turns should be stripped
	if ty := gjson.GetBytes(got, "messages.0.content.0.content.0.type").String(); ty != "text" {
		t.Fatalf("nested image in turn 0 should be stripped, got type=%s", ty)
	}
	// Last user turn should be kept
	if ty := gjson.GetBytes(got, "messages.14.content.0.content.0.type").String(); ty != "image" {
		t.Fatalf("nested image in last turn should be kept, got type=%s", ty)
	}
}

func TestStripOldImages_28MBCap(t *testing.T) {
	big := strings.Repeat("A", 15*1024*1024)
	out := []byte(`{"messages":[]}`)

	// Single user message with two huge images (within last 6 turns but over 28MB)
	m := []byte(`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}},{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}]}`)
	m, _ = sjson.SetBytes(m, "content.0.source.data", big)
	m, _ = sjson.SetBytes(m, "content.1.source.data", big)
	out, _ = sjson.SetRawBytes(out, "messages.-1", m)

	got := stripOldImages(out)
	if len(got) > 28*1024*1024 {
		t.Fatalf("payload not reduced: %d bytes", len(got))
	}
}
