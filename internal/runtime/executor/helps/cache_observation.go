package helps

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"strconv"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Capture is process-local and finite. Reloading the same configuration does not
// reset its budget. A different capture cannot displace an unexpired capture.
var cacheCapture = struct {
	sync.Mutex
	active *cacheCaptureScope
}{}

type cacheCaptureScope struct {
	config  config.CacheObservationConfig
	expires time.Time
	key     [32]byte
	started int
}

type cacheComponent struct {
	Path         string `json:"path"`
	Digest       string `json:"digest"`
	PrefixDigest string `json:"prefix_digest"`
}

type cacheMarker struct {
	Path string `json:"path"`
	TTL  string `json:"ttl"`
}

type cacheObservation struct {
	Phase                string                `json:"phase"`
	Schema               string                `json:"schema"`
	RequestID            string                `json:"request_id"`
	CaptureDigest        string                `json:"capture_digest"`
	SessionDigest        string                `json:"session_digest"`
	CredentialDigest     string                `json:"credential_digest"`
	RouteDigest          string                `json:"route_digest"`
	Provider             string                `json:"provider"`
	RequestedModelDigest string                `json:"requested_model_digest"`
	ServedModelDigest    string                `json:"served_model_digest,omitempty"`
	RequestedAt          time.Time             `json:"requested_at"`
	CompletedAt          time.Time             `json:"completed_at"`
	BodyBytes            int                   `json:"body_bytes"`
	BodyDigest           string                `json:"body_digest,omitempty"`
	ShapeStatus          string                `json:"shape_status"`
	Components           []cacheComponent      `json:"components,omitempty"`
	Settings             map[string]string     `json:"settings,omitempty"`
	Markers              []cacheMarker         `json:"markers,omitempty"`
	UsageState           string                `json:"usage_state"`
	UsageFields          []string              `json:"usage_fields,omitempty"`
	Tokens               *usage.TokenBreakdown `json:"tokens,omitempty"`
	CacheWrite5m         *int64                `json:"cache_write_5m,omitempty"`
	CacheWrite1h         *int64                `json:"cache_write_1h,omitempty"`
	Failed               bool                  `json:"failed"`
	StatusCode           int                   `json:"status_code,omitempty"`
	LatencyMillis        int64                 `json:"latency_ms,omitempty"`
	Stream               bool                  `json:"stream"`
	RequestedTierDigest  string                `json:"requested_tier_digest"`
	ServedTierDigest     string                `json:"served_tier_digest,omitempty"`
	// The scope key and raw request are never serialized or retained.
	scope *cacheCaptureScope
}

func (s *cacheCaptureScope) digest(label, value string) string {
	h := hmac.New(sha256.New, s.key[:])
	writeCacheHash(h, label)
	writeCacheHash(h, value)
	return hex.EncodeToString(h.Sum(nil))
}

func writeCacheHash(h hash.Hash, value string) {
	_, _ = fmt.Fprintf(h, "%d:", len(value))
	_, _ = h.Write([]byte(value))
}

func beginCacheCapture(c config.CacheObservationConfig, session string, now time.Time) *cacheCaptureScope {
	if c.SessionID == "" || session != c.SessionID || c.MaxAttempts < 1 || c.MaxAttempts > 100 {
		return nil
	}
	expires, err := time.Parse(time.RFC3339, c.ExpiresAt)
	if err != nil || !now.Before(expires) || expires.Sub(now) > time.Hour {
		return nil
	}
	cacheCapture.Lock()
	defer cacheCapture.Unlock()
	s := cacheCapture.active
	if s == nil || !now.Before(s.expires) {
		s = &cacheCaptureScope{config: c, expires: expires}
		if _, errRandom := rand.Read(s.key[:]); errRandom != nil {
			return nil
		}
		cacheCapture.active = s
	}
	if s.config != c || s.started >= c.MaxAttempts {
		return nil
	}
	s.started++
	return s
}

// ObserveCacheRequest must be called with the complete final body, after payload
// rules, signing and normalization, beside RecordAPIRequest. It does not touch
// transport bytes or enable request/body logging. Unsupported paths stay unknown.
func (r *UsageReporter) ObserveCacheRequest(ctx context.Context, cfg *config.Config, body []byte, route string) {
	if r == nil || cfg == nil || cfg.CacheObservation == nil || r.cacheObservation != nil {
		return
	}
	if r.provider != "claude" && r.provider != "codex" {
		return
	}
	session := util.SessionIDFromContext(ctx)
	if session == "" {
		session = r.sessionID
	}
	s := beginCacheCapture(*cfg.CacheObservation, session, time.Now())
	if s == nil {
		return
	}
	o := &cacheObservation{
		Schema: "provider-cache-observation-v1", RequestID: r.RequestID(),
		CaptureDigest: s.digest("capture", s.config.ExpiresAt), SessionDigest: s.digest("session", session),
		CredentialDigest: s.digest("credential", r.authIndex), RouteDigest: s.digest("route", route),
		Provider: r.provider, RequestedModelDigest: s.digest("requested-model", r.model),
		RequestedAt: r.requestedAt, BodyBytes: len(body), ShapeStatus: "complete", UsageState: "absent", scope: s,
	}
	r.cacheObservation = o
	defer o.emit("request")
	// Never hash a truncated preview and describe it as a complete request.
	if len(body) > 8<<20 {
		o.ShapeStatus = "oversized"
		return
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		o.ShapeStatus = "invalid_json"
		return
	}
	o.BodyDigest = s.digest("body", string(body))
	o.Settings = make(map[string]string)
	for _, path := range []string{"model", "thinking", "reasoning", "output_config", "text", "tool_choice", "parallel_tool_calls", "service_tier", "speed", "cache_control", "prompt_cache_key", "prompt_cache_retention", "context_management"} {
		if value := gjson.GetBytes(body, path); value.Exists() {
			o.Settings[path] = s.digest("setting:"+path, value.Raw)
		}
	}
	prefix := hmac.New(sha256.New, s.key[:])
	for _, path := range []string{"tools", "system", "instructions", "messages", "input"} {
		value := gjson.GetBytes(body, path)
		if !value.Exists() {
			continue
		}
		items := []gjson.Result{value}
		if value.IsArray() {
			items = value.Array()
		}
		for i, item := range items {
			if len(o.Components) == 512 {
				o.ShapeStatus = "component_limit"
				return
			}
			componentPath := path
			if value.IsArray() {
				componentPath += "." + strconv.Itoa(i)
			}
			writeCacheHash(prefix, componentPath)
			writeCacheHash(prefix, item.Raw)
			o.Components = append(o.Components, cacheComponent{componentPath, s.digest("component:"+componentPath, item.Raw), hex.EncodeToString(prefix.Sum(nil))})
			o.observeMarker(componentPath, item.Get("cache_control"))
			if content := item.Get("content"); content.IsArray() {
				for j, block := range content.Array() {
					o.observeMarker(componentPath+".content."+strconv.Itoa(j), block.Get("cache_control"))
				}
			}
		}
	}
	o.observeMarker("", gjson.GetBytes(body, "cache_control"))
}

func (o *cacheObservation) observeMarker(path string, marker gjson.Result) {
	if !marker.IsObject() || marker.Get("type").String() != "ephemeral" {
		return
	}
	if len(o.Markers) >= 512 {
		o.ShapeStatus = "component_limit"
		return
	}
	ttl := "default"
	if value := marker.Get("ttl"); value.Exists() {
		ttl = value.String()
		if ttl != "5m" && ttl != "1h" {
			ttl = "unknown"
		}
	}
	if path != "" {
		path += "."
	}
	o.Markers = append(o.Markers, cacheMarker{path + "cache_control", ttl})
}

// ObserveCacheUsage extracts only numeric presence and TTL counters from an
// unmodified provider response. Missing fields remain absent, including streams.
func (r *UsageReporter) ObserveCacheUsage(payload []byte) {
	if r == nil || r.cacheObservation == nil {
		return
	}
	data := jsonPayload(payload)
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return
	}
	root := gjson.ParseBytes(data)
	for _, path := range []string{"usage", "message.usage", "response.usage"} {
		node := root.Get(path)
		if !node.IsObject() {
			continue
		}
		for _, field := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
			if node.Get(field).Type == gjson.Number {
				r.cacheObservation.UsageState = "reported"
				found := false
				for _, present := range r.cacheObservation.UsageFields {
					found = found || present == field
				}
				if !found {
					r.cacheObservation.UsageFields = append(r.cacheObservation.UsageFields, field)
				}
			}
		}
		for field, destination := range map[string]**int64{
			"cache_creation.ephemeral_5m_input_tokens": &r.cacheObservation.CacheWrite5m,
			"cache_creation.ephemeral_1h_input_tokens": &r.cacheObservation.CacheWrite1h,
		} {
			value := node.Get(field)
			tokens, err := strconv.ParseInt(value.Raw, 10, 64)
			if value.Type == gjson.Number && err == nil && tokens >= 0 {
				*destination = &tokens
			}
		}
	}
}

func (r *UsageReporter) publishCacheObservation(record usage.Record) {
	o := r.cacheObservation
	if o == nil {
		return
	}
	o.CompletedAt = time.Now()
	o.Failed, o.StatusCode = record.Failed, record.Fail.StatusCode
	o.LatencyMillis, o.Stream = record.Latency.Milliseconds(), record.Stream
	o.RequestedTierDigest = o.scope.digest("requested-tier", record.ServiceTier)
	if record.ResponseServiceTier != "" {
		o.ServedTierDigest = o.scope.digest("served-tier", record.ResponseServiceTier)
	}
	if record.ResponseModel != "" {
		o.ServedModelDigest = o.scope.digest("served-model", record.ResponseModel)
	}
	if o.UsageState == "reported" {
		tokens := record.Detail.TokenBreakdown
		o.Tokens = &tokens
	}
	o.emit("completion")
}

func (o *cacheObservation) emit(phase string) {
	o.Phase = phase
	// Serialize this allowlisted record, never usage.Record, headers, failures or
	// UpstreamRequestLog. Content and identity values appear only as scoped HMACs.
	encoded, err := json.Marshal(o)
	if err == nil {
		log.WithField("cache_observation", string(encoded)).Info("provider cache observation")
	}
}
