package protocol

import (
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/transport"
)

const (
	DeepSeekHost                    = "chat.deepseek.com"
	DeepSeekLoginURL                = "https://chat.deepseek.com/api/v0/users/login"
	DeepSeekCreateSessionURL        = "https://chat.deepseek.com/api/v0/chat_session/create"
	DeepSeekCreatePowURL            = "https://chat.deepseek.com/api/v0/chat/create_pow_challenge"
	DeepSeekCompletionURL           = "https://chat.deepseek.com/api/v0/chat/completion"
	DeepSeekContinueURL             = "https://chat.deepseek.com/api/v0/chat/continue"
	DeepSeekStopStreamURL           = "https://chat.deepseek.com/api/v0/chat/stop_stream"
	DeepSeekUploadFileURL           = "https://chat.deepseek.com/api/v0/file/upload_file"
	DeepSeekFetchFilesURL           = "https://chat.deepseek.com/api/v0/file/fetch_files"
	DeepSeekFetchSessionURL         = "https://chat.deepseek.com/api/v0/chat_session/fetch_page"
	DeepSeekDeleteSessionURL        = "https://chat.deepseek.com/api/v0/chat_session/delete"
	DeepSeekDeleteAllSessionsURL    = "https://chat.deepseek.com/api/v0/chat_session/delete_all"
	DeepSeekUpdateSettingsURL       = "https://chat.deepseek.com/api/v0/users/update_settings"
	DeepSeekClientSettingsURL       = "https://chat.deepseek.com/api/v0/client/settings"
	DeepSeekClientSettingsReportURL = "https://chat.deepseek.com/api/v0/client/settings/report"
	DeepSeekCompletionTargetPath    = "/api/v0/chat/completion"
	DeepSeekUploadTargetPath        = "/api/v0/file/upload_file"
)

// chromeMajorVersion is sourced from the transport layer so the TLS fingerprint
// and the User-Agent can never drift apart (they once did: TLS said Chrome 133
// while the UA claimed 128).
const chromeMajorVersion = transport.ChromeMajorVersion

var chromeUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + chromeMajorVersion + ".0.0.0 Safari/537.36"

// chromeSecChUA's GREASE brand string and ordering change with Chrome versions.
// This form is taken from a real capture of chat.deepseek.com (Chrome 150).
// Re-verify against a fresh capture when bumping the version.
var chromeSecChUA = "\"Not;A=Brand\";v=\"8\", \"Chromium\";v=\"" + chromeMajorVersion + "\", \"Google Chrome\";v=\"" + chromeMajorVersion + "\""

var defaultStaticBaseHeaders = map[string]string{
	"Host":         "chat.deepseek.com",
	"Accept":       "application/json",
	"Content-Type": "application/json",
	// The real web client does send this header. It was once removed as an
	// App-only header — that was a misjudgement: captures show platform=web
	// browser requests carry it too.
	"x-client-bundle-id": "com.deepseek.chat",
}

// Do not add x-hif-dliq / x-hif-leim headers.
//
// The real web client sends these two base64 values in a normal window. Capture
// analysis showed:
//   - Same value for every request on a given account; unchanged across model
//     switches, file uploads, logout/login cycles.
//   - Completely absent in incognito windows.
//   - login, chat_session/delete etc. never carry them.
//
// Their absence in incognito means they read from persistent storage, not
// hardware (otherwise the same machine + browser would compute the same value).
// So "not sending them" is a real, reproducible browser state — this project
// equates to an incognito session.
//
// More importantly, they are device-level identifiers. Hard-coding one value
// would make every account share a single device fingerprint, and "N accounts
// on one device" is a far stronger correlation signal than their absence.
// Unless per-account independent legitimate values can be obtained, faking
// them only makes things worse.

// webBrowserHeaders are the headers a platform=web DeepSeek browser client sends.
var webBrowserHeaders = map[string]string{
	"User-Agent":         chromeUserAgent,
	"sec-ch-ua":          chromeSecChUA,
	"sec-ch-ua-mobile":   "?0",
	"sec-ch-ua-platform": "\"Windows\"",
	"Origin":             "https://chat.deepseek.com",
	"Referer":            "https://chat.deepseek.com/",
	"sec-fetch-site":     "same-origin",
	"sec-fetch-mode":     "cors",
	"sec-fetch-dest":     "empty",
	// Browser fetch sends */*, not application/json.
	// Only overrides the web platform: login uses App-style headers (see LoginHeaders).
	"Accept": "*/*",
	// Must be declared explicitly: otherwise Go/fhttp auto-injects a gzip-only
	// accept-encoding, and a Chrome-claiming client that only accepts gzip is
	// visibly anomalous. Response decompression is handled in the wire layer.
	"Accept-Encoding": "gzip, deflate, br, zstd",
	// Chrome 12x+ carries priority on fetch/XHR.
	"priority": "u=1, i",
}

// localeTimezones maps locale to IANA timezone. The offset is computed at
// request time from live timezone data, not hardcoded: real browsers report
// the "current" offset, including DST. A hardcoded offset would be wrong half
// the year for DST regions.
//
// Unit is seconds. Previously en_US was written as -420 in minute form, which
// equals UTC-00:07 — a non-existent timezone, more detectable than a fixed
// UTC+8.
var localeTimezones = map[string]string{
	"zh_CN": "Asia/Shanghai",
	"zh_TW": "Asia/Taipei",
	"en_US": "America/Los_Angeles",
	"en_GB": "Europe/London",
	"ja_JP": "Asia/Tokyo",
	"ko_KR": "Asia/Seoul",
	"de_DE": "Europe/Berlin",
	"fr_FR": "Europe/Paris",
	"ru_RU": "Europe/Moscow",
	"es_ES": "Europe/Madrid",
}

const defaultTimezoneOffset = "28800" // UTC+8, fallback for unknown locales

// localeAcceptLanguages maps locale to Accept-Language.
//
// This uses the "mother-tongue-only" Chrome default form (e.g. zh-CN,zh;q=0.9).
// It was once changed to a long form with English fallback — that was captured
// from a Chrome profile with English added; the control group (incognito,
// default config) sends the short form. This header depends on the user's
// language settings, not the browser version, so the short form (default
// install) is the more conservative choice.
var localeAcceptLanguages = map[string]string{
	"zh_CN": "zh-CN,zh;q=0.9",
	"zh_TW": "zh-TW,zh;q=0.9",
	"en_US": "en-US,en;q=0.9",
	"en_GB": "en-GB,en;q=0.9",
	"ja_JP": "ja-JP,ja;q=0.9",
	"ko_KR": "ko-KR,ko;q=0.9",
	"de_DE": "de-DE,de;q=0.9",
	"fr_FR": "fr-FR,fr;q=0.9",
	"ru_RU": "ru-RU,ru;q=0.9",
	"es_ES": "es-ES,es;q=0.9",
}

var defaultSkipContainsPatterns = []string{
	"quasi_status",
	"elapsed_secs",
	"token_usage",
	"pending_fragment",
	"conversation_mode",
	"fragments/-1/status",
	"fragments/-2/status",
	"fragments/-3/status",
}

var defaultSkipExactPaths = []string{
	"response/search_status",
}

type clientConstants struct {
	Name            string `json:"name"`
	Platform        string `json:"platform"`
	Version         string `json:"version"`
	AndroidAPILevel string `json:"android_api_level"`
	Locale          string `json:"locale"`
}

// Hardcoded shared constants (ported from constants_shared.json). Hardcoded
// rather than embedded because the values only change when the upstream web
// client is re-captured, and embedding pulled in time/tzdata for no benefit.
var (
	sharedClient = clientConstants{
		Name:     "DeepSeek",
		Platform: "web",
		Version:  "2.2.0",
		Locale:   "zh_CN",
	}
	sharedBaseHeaderOverrides = map[string]string{
		"Host":         "chat.deepseek.com",
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}
)

// ClientVersion is the DeepSeek web client version advertised in x-client-version.
var ClientVersion = sharedClient.Version

// BaseHeaders is the default header set built from the shared client config.
var BaseHeaders = BuildBaseHeaders(sharedClient, sharedBaseHeaderOverrides)

// SkipContainsPatterns and SkipExactPathSet control which SSE paths are ignored.
var (
	SkipContainsPatterns = cloneStringSlice(defaultSkipContainsPatterns)
	SkipExactPathSet     = toStringSet(defaultSkipExactPaths)
)

// BuildBaseHeaders builds the request header set for the given client config
// and locale overrides. When platform=web it includes browser headers to match
// the Chrome TLS fingerprint; x-client-timezone-offset is set dynamically per
// locale.
func BuildBaseHeaders(client clientConstants, overrides map[string]string) map[string]string {
	out := cloneStringMap(defaultStaticBaseHeaders)
	for k, v := range overrides {
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}

	locale := strings.TrimSpace(client.Locale)
	if locale == "" {
		locale = "zh_CN"
	}
	out["x-client-timezone-offset"] = TimezoneOffsetFor(locale)

	if IsWebPlatform(client.Platform) {
		for k, v := range webBrowserHeaders {
			out[k] = v
		}
		out["Accept-Language"] = AcceptLanguageFor(locale)
	} else if client.Name != "" && client.Version != "" {
		out["User-Agent"] = client.Name + "/" + client.Version
	}

	if client.Platform != "" {
		out["x-client-platform"] = client.Platform
	}
	if client.Version != "" {
		out["x-client-version"] = client.Version
	}
	if client.Locale != "" {
		out["x-client-locale"] = client.Locale
	}
	return out
}

// BaseHeadersFor uses the shared client config and overrides timezone/language
// headers for the given locale.
func BaseHeadersFor(locale string) map[string]string {
	client := normalizeClientConstants(sharedClient)
	client.Locale = strings.TrimSpace(locale)
	if client.Locale == "" {
		client.Locale = "zh_CN"
	}
	return BuildBaseHeaders(client, sharedBaseHeaderOverrides)
}

// LoginHeaders returns conservative headers for login/token-refresh endpoints.
// Login is sensitive to browser headers; using App-style headers without Chrome
// UA and sec-* avoids anomalous responses.
func LoginHeaders(locale string) map[string]string {
	client := normalizeClientConstants(sharedClient)
	client.Locale = strings.TrimSpace(locale)
	if client.Locale == "" {
		client.Locale = "zh_CN"
	}
	out := cloneStringMap(defaultStaticBaseHeaders)
	for k, v := range sharedBaseHeaderOverrides {
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	out["x-client-timezone-offset"] = TimezoneOffsetFor(client.Locale)
	if client.Name != "" && client.Version != "" {
		out["User-Agent"] = client.Name + "/" + client.Version
	}
	if client.Platform != "" {
		out["x-client-platform"] = client.Platform
	}
	if client.Version != "" {
		out["x-client-version"] = client.Version
	}
	if client.Locale != "" {
		out["x-client-locale"] = client.Locale
	}
	return out
}

func normalizeClientConstants(in clientConstants) clientConstants {
	if in.Name == "" {
		in.Name = "DeepSeek"
	}
	if in.Platform == "" {
		in.Platform = "web"
	}
	if in.Locale == "" {
		in.Locale = "zh_CN"
	}
	return in
}

// IsWebPlatform reports whether platform represents the web client.
func IsWebPlatform(platform string) bool {
	return strings.EqualFold(strings.TrimSpace(platform), "web")
}

// TimezoneOffsetFor returns the current timezone offset (seconds, including DST)
// for the given locale. Falls back to UTC+8 for unknown locales or missing
// timezone data.
func TimezoneOffsetFor(locale string) string {
	zone, ok := localeTimezones[strings.TrimSpace(locale)]
	if !ok {
		return defaultTimezoneOffset
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return defaultTimezoneOffset
	}
	_, offset := time.Now().In(loc).Zone()
	return strconv.Itoa(offset)
}

// AcceptLanguageFor returns the Accept-Language for the given locale, falling
// back to Chinese for unknown locales.
func AcceptLanguageFor(locale string) string {
	if lang, ok := localeAcceptLanguages[strings.TrimSpace(locale)]; ok {
		return lang
	}
	return localeAcceptLanguages["zh_CN"]
}

// ChatSessionReferer returns the URL of a conversation page.
// A real browser sending a message has Referer set to the current conversation
// page, not the site root — the root only appears in the first frame of a new
// conversation with no session yet.
func ChatSessionReferer(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "https://chat.deepseek.com/"
	}
	return "https://chat.deepseek.com/a/chat/s/" + sessionID
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringSlice(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func toStringSet(in []string) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		out[v] = struct{}{}
	}
	return out
}

const (
	KeepAliveTimeout  = 5
	StreamIdleTimeout = 300
	MaxKeepaliveCount = 40
)
