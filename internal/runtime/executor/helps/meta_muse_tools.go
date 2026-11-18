package helps

import (
	"github.com/MoeForge/moego/ojson"
)

// Captured from the Muse Code 1.4.12 reminder agent. Subscription-key tool
// requests can require a canonical Muse namespace, even with current client
// headers. Keep caller tools separate so their names and schemas are preserved.
const metaMuseNamespace = `{"type":"namespace","name":"muse","description":"Muse Code tool set.","tools":[{"type":"function","name":"submit_reminder_decision","description":"Submit this reminder child's explicit decision.","parameters":{"additionalProperties":false,"properties":{"advisory_text":{"description":"Short text to deliver to the main agent when reminding.","type":["null","string"]},"confidence":{"description":"Confidence in the decision.","enum":["low","medium","high"],"type":["null","string"]},"decision":{"enum":["remind","none"],"type":"string"},"priority":{"description":"Requested reminder priority.","enum":["low","normal","high"],"type":["null","string"]},"reason":{"description":"Brief reason for reminding or staying silent.","type":"string"},"skill_id":{"description":"The listed skill id to remind when needs_reminder is true.","type":["null","string"]},"visible_for_steps":{"description":"Requested ephemeral visibility length in main model steps.","maximum":8,"minimum":1,"type":["null","integer"]}},"required":["advisory_text","confidence","decision","priority","reason","skill_id","visible_for_steps"],"type":"object"},"strict":true}]}`

// EnsureMetaMuseToolNamespace appends the canonical namespace to tool-bearing
// Responses requests, preserving caller tool indexes. It leaves tool-less
// requests and existing Muse namespaces untouched. Call before payload rules so
// user overrides and filters stay final.
func EnsureMetaMuseToolNamespace(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	doc := ojson.NewDoc(body)
	if !EnsureMetaMuseToolNamespaceDoc(doc) {
		return body
	}
	return doc.Bytes()
}

// EnsureMetaMuseToolNamespaceDoc appends the canonical namespace to tool-bearing
// Responses requests in place on doc.
func EnsureMetaMuseToolNamespaceDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	tools := doc.Get("tools")
	if !tools.IsArray() {
		return false
	}
	items := tools.Array()
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if item.Get("type").String() == "namespace" && item.Get("name").String() == "muse" {
			return false
		}
	}
	raw := make([]byte, 0, len(metaMuseNamespace)+len(tools.Raw)+1)
	raw = append(raw, '[')
	for i, item := range items {
		if i > 0 {
			raw = append(raw, ',')
		}
		raw = append(raw, item.Raw...)
	}
	raw = append(raw, ',')
	raw = append(raw, metaMuseNamespace...)
	raw = append(raw, ']')
	return doc.SetRawBytes("tools", raw) == nil
}
