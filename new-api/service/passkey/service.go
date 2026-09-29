package passkey

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/go-webauthn/webauthn/protocol"
	webauthn "github.com/go-webauthn/webauthn/webauthn"
)

var ErrRPIDUnavailable = system_setting.ErrPasskeyRPIDUnavailable

// BuildWebAuthn constructs a WebAuthn instance using the current passkey settings and request context.
func BuildWebAuthn(r *http.Request) (*webauthn.WebAuthn, error) {
	return BuildWebAuthnForRPID(r, "")
}

// BuildWebAuthnForRPID uses a single configured RP ID, never a list of IDs to
// try against the same signed response. An empty ID is for new registrations.
func BuildWebAuthnForRPID(r *http.Request, selectedRPID string) (*webauthn.WebAuthn, error) {
	settings := system_setting.PasskeySettingsSnapshot()

	displayName := strings.TrimSpace(settings.RPDisplayName)
	if displayName == "" {
		displayName = common.SystemName
	}

	origins, err := resolveOrigins(r, &settings)
	if err != nil {
		return nil, err
	}

	rpID, err := resolveRPID(r, &settings, origins)
	if err != nil {
		return nil, err
	}
	if selectedRPID != "" && selectedRPID != rpID {
		if !slices.Contains(settings.RelyingPartyIDs(), selectedRPID) {
			return nil, ErrRPIDUnavailable
		}
		rpID = selectedRPID
	}
	origins = originsForRPID(origins, rpID)
	if len(origins) == 0 {
		return nil, ErrRPIDUnavailable
	}
	if origin := r.Header.Get("Origin"); origin != "" && !protocol.IsOriginInHaystack(origin, origins) {
		return nil, ErrRPIDUnavailable
	}

	selection := protocol.AuthenticatorSelection{
		ResidentKey:        protocol.ResidentKeyRequirementRequired,
		RequireResidentKey: protocol.ResidentKeyRequired(),
		UserVerification:   protocol.UserVerificationRequirement(settings.UserVerification),
	}
	if selection.UserVerification == "" {
		selection.UserVerification = protocol.VerificationPreferred
	}
	if attachment := strings.TrimSpace(settings.AttachmentPreference); attachment != "" {
		selection.AuthenticatorAttachment = protocol.AuthenticatorAttachment(attachment)
	}

	config := &webauthn.Config{
		RPID:                   rpID,
		RPDisplayName:          displayName,
		RPOrigins:              origins,
		AuthenticatorSelection: selection,
		Debug:                  common.DebugEnabled,
		Timeouts: webauthn.TimeoutsConfig{
			Login: webauthn.TimeoutConfig{
				Enforce:    true,
				Timeout:    2 * time.Minute,
				TimeoutUVD: 2 * time.Minute,
			},
			Registration: webauthn.TimeoutConfig{
				Enforce:    true,
				Timeout:    2 * time.Minute,
				TimeoutUVD: 2 * time.Minute,
			},
		},
	}

	return webauthn.New(config)
}

// BuildLoginWebAuthn gives a known credential's binding precedence over a
// browser's last-successful-domain hint. All choices remain server controlled.
func BuildLoginWebAuthn(r *http.Request, hint, credentialRPID string) (*webauthn.WebAuthn, []string, error) {
	settings := system_setting.PasskeySettingsSnapshot()
	origins, err := resolveOrigins(r, &settings)
	if err != nil {
		return nil, nil, err
	}
	primary, err := resolveRPID(r, &settings, origins)
	if err != nil {
		return nil, nil, err
	}
	configured := append([]string{primary}, settings.RelyingPartyIDs()...)
	available := []string{}
	for _, id := range configured {
		if credentialRPID != "" && id != credentialRPID {
			continue
		}
		allowedOrigins := originsForRPID(origins, id)
		if len(allowedOrigins) == 0 || (r.Header.Get("Origin") != "" && !protocol.IsOriginInHaystack(r.Header.Get("Origin"), allowedOrigins)) {
			continue
		}
		if !slices.Contains(available, id) {
			available = append(available, id)
		}
	}
	if len(available) == 0 {
		return nil, nil, ErrRPIDUnavailable
	}
	selected := credentialRPID
	if selected == "" {
		selected = hint
	}
	if selected == "" {
		selected = available[0]
	}
	if !slices.Contains(available, selected) {
		return nil, nil, ErrRPIDUnavailable
	}
	wa, err := BuildWebAuthnForRPID(r, selected)
	return wa, available, err
}

func originsForRPID(origins []string, rpID string) []string {
	allowed := []string{}
	for _, origin := range origins {
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		domain := strings.ToLower(rpID)
		if !strings.Contains(domain, ".") && domain != "localhost" {
			if host == domain && parsed.Scheme == "https" {
				allowed = append(allowed, origin)
			}
			continue
		}
		if host == domain || strings.HasSuffix(host, "."+domain) {
			allowed = append(allowed, origin)
		}
	}
	return allowed
}

func resolveOrigins(r *http.Request, settings *system_setting.PasskeySettings) ([]string, error) {
	originsStr := strings.TrimSpace(settings.Origins)
	if originsStr != "" {
		originList := strings.Split(originsStr, ",")
		origins := make([]string, 0, len(originList))
		for _, origin := range originList {
			trimmed := strings.TrimSpace(origin)
			if trimmed == "" {
				continue
			}
			if !settings.AllowInsecureOrigin && strings.HasPrefix(strings.ToLower(trimmed), "http://") {
				return nil, fmt.Errorf("Passkey 不允许使用不安全的 Origin: %s", trimmed)
			}
			origins = append(origins, trimmed)
		}
		if len(origins) == 0 {
			// 如果配置了Origins但过滤后为空，使用自动推导
			goto autoDetect
		}
		return origins, nil
	}

autoDetect:
	scheme := detectScheme(r)
	if scheme == "http" && !settings.AllowInsecureOrigin && r.Host != "localhost" && r.Host != "127.0.0.1" && !strings.HasPrefix(r.Host, "127.0.0.1:") && !strings.HasPrefix(r.Host, "localhost:") {
		return nil, fmt.Errorf("Passkey 仅支持 HTTPS，当前访问: %s://%s，请在 Passkey 设置中允许不安全 Origin 或配置 HTTPS", scheme, r.Host)
	}
	// 优先使用请求的完整Host（包含端口）
	host := r.Host

	// 如果无法从请求获取Host，尝试从ServerAddress获取
	if host == "" && system_setting.ServerAddress != "" {
		if parsed, err := url.Parse(system_setting.ServerAddress); err == nil && parsed.Host != "" {
			host = parsed.Host
			if scheme == "" && parsed.Scheme != "" {
				scheme = parsed.Scheme
			}
		}
	}
	if host == "" {
		return nil, fmt.Errorf("无法确定 Passkey 的 Origin，请在系统设置或 Passkey 设置中指定。当前 Host: '%s', ServerAddress: '%s'", r.Host, system_setting.ServerAddress)
	}
	if scheme == "" {
		scheme = "https"
	}
	origin := fmt.Sprintf("%s://%s", scheme, host)
	return []string{origin}, nil
}

func resolveRPID(r *http.Request, settings *system_setting.PasskeySettings, origins []string) (string, error) {
	rpID := strings.TrimSpace(settings.RPID)
	if rpID != "" {
		return hostWithoutPort(rpID), nil
	}
	if len(origins) == 0 {
		return "", errors.New("Passkey 未配置 Origin，无法推导 RPID")
	}
	parsed, err := url.Parse(origins[0])
	if err != nil {
		return "", fmt.Errorf("无法解析 Passkey Origin: %w", err)
	}
	return hostWithoutPort(parsed.Host), nil
}

func hostWithoutPort(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if strings.Contains(host, ":") {
		if host, _, err := net.SplitHostPort(host); err == nil {
			return host
		}
	}
	return host
}

func detectScheme(r *http.Request) string {
	if r == nil {
		return ""
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		parts := strings.Split(proto, ",")
		return strings.ToLower(strings.TrimSpace(parts[0]))
	}
	if r.TLS != nil {
		return "https"
	}
	if r.URL != nil && r.URL.Scheme != "" {
		return strings.ToLower(r.URL.Scheme)
	}
	if r.Header.Get("X-Forwarded-Protocol") != "" {
		return strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Protocol")))
	}
	return "http"
}
