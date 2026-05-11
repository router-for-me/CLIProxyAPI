// Package qoder provides OAuth2 authentication functionality for the Qoder provider.
package qoder

// Qoder login configuration
const (
	CallbackPort = 51122
	AuthBase     = "https://qoder.com"
	CenterBase   = "https://center.qoder.sh"
	ChatBase     = "https://api3.qoder.sh"
	OpenAPIBase  = "https://openapi.qoder.sh"
	IDEVersion   = "0.14.2"
	CosyVersion  = "1.0.0"
	RedirectURI  = "qoder://aicoding.aicoding-agent/login-success"
)

// SelectAccountsPath is the browser login page path.
const SelectAccountsPath = "/device/selectAccounts"

// ServerPublicKeyPEM is the RSA public key for COSY authentication.
const ServerPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

// Custom base64 encoding alphabet used by Qoder body encoding.
const (
	CustomPad      = "$"
	StdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	CustomAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
)

// Chat endpoint path
const (
	ChatPath       = "/algo/api/v2/service/pro/sse/agent_chat_generation"
	ChatQueryExtra = "FetchKeys=llm_model_result&AgentId=agent_common"
	ModelListPath  = "/algo/api/v2/model/list"
	UserPlanPath   = "/algo/api/v2/user/plan"
	UserStatusPath = "/api/v3/user/status"
)
