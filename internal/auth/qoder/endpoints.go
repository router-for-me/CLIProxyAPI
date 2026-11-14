package qoder

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const (
	qoderVPCDomainSuffix = "vpc.qoder.com.cn"
	qoderCLIClientID     = "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb"
)

var qoderVPCInstancePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// Endpoints contains every upstream origin used by the Qoder provider.
type Endpoints struct {
	OpenAPIBase string
	CenterBase  string
	InferBase   string
	WebBase     string
	VPCInstance string
}

// ResolveEndpoints returns the public global endpoints or the derived Qoder CN
// Enterprise VPC endpoints configured for this process.
func ResolveEndpoints(cfg *config.Config) (Endpoints, error) {
	raw := ""
	if cfg != nil {
		raw = cfg.Qoder.VPCEndpoint
	}
	instance, err := parseVPCInstance(raw)
	if err != nil {
		return Endpoints{}, err
	}
	if instance == "" {
		return Endpoints{
			OpenAPIBase: QoderOpenAPIBase,
			CenterBase:  QoderCenterBase,
			InferBase:   QoderChatBase,
			WebBase:     strings.TrimSuffix(QoderLoginURL, "/device/selectAccounts"),
		}, nil
	}

	return Endpoints{
		OpenAPIBase: fmt.Sprintf("https://%s-openapi.%s", instance, qoderVPCDomainSuffix),
		CenterBase:  fmt.Sprintf("https://%s-gateway.%s", instance, qoderVPCDomainSuffix),
		InferBase:   fmt.Sprintf("https://%s-gateway.%s", instance, qoderVPCDomainSuffix),
		WebBase:     fmt.Sprintf("https://%s.%s", instance, qoderVPCDomainSuffix),
		VPCInstance: instance,
	}, nil
}

func parseVPCInstance(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", nil
	}
	if strings.Contains(value, "://") || strings.ContainsAny(value, "/?#:") {
		return "", fmt.Errorf("qoder.vpc-endpoint must be an instance name or VPC domain without scheme, port, path, or query")
	}

	suffix := "." + qoderVPCDomainSuffix
	if strings.HasSuffix(value, suffix) {
		value = strings.TrimSuffix(value, suffix)
		if strings.HasSuffix(value, "-openapi") {
			value = strings.TrimSuffix(value, "-openapi")
		} else if strings.HasSuffix(value, "-gateway") {
			value = strings.TrimSuffix(value, "-gateway")
		}
	} else if strings.Contains(value, ".") {
		return "", fmt.Errorf("qoder.vpc-endpoint %q is not a %s domain", raw, qoderVPCDomainSuffix)
	}

	if !qoderVPCInstancePattern.MatchString(value) {
		return "", fmt.Errorf("qoder.vpc-endpoint %q contains an invalid VPC instance name", raw)
	}
	return value, nil
}

func (e Endpoints) LoginURL() string {
	return e.WebBase + "/device/selectAccounts"
}

func (e Endpoints) OAuthTokenURL() string {
	return e.OpenAPIBase + "/api/v1/deviceToken/poll"
}

func (e Endpoints) RefreshTokenURL() string {
	return e.CenterBase + "/algo/api/v3/user/refresh_token"
}

func (e Endpoints) UserInfoURL() string {
	return e.OpenAPIBase + "/api/v1/userinfo"
}

func (e Endpoints) ModelListURL() string {
	if e.VPCInstance == "" {
		return e.InferBase + "/algo/api/v2/model/list"
	}
	return e.InferBase + "/algo/api/v2/model/list?Encode=1"
}

func (e Endpoints) JobTokenExchangeURL() string {
	return e.OpenAPIBase + QoderJobTokenExchangePath
}

func (e Endpoints) JobTokenRefreshURL() string {
	return e.OpenAPIBase + QoderJobTokenRefreshPath
}

func (e Endpoints) ChatURL() string {
	return e.InferBase + "/algo" + QoderSigPath + "?FetchKeys=llm_model_result&AgentId=agent_common"
}

func (e Endpoints) EncodedChatURL() string {
	return e.ChatURL() + "&Encode=1"
}

func (e Endpoints) UsageURL() string {
	return e.OpenAPIBase + "/api/v2/quota/usage"
}

func (e Endpoints) DeviceClientID() string {
	if e.VPCInstance == "" {
		return ""
	}
	return qoderCLIClientID
}
