// Package kiro provides OAuth2 authentication and token management for the Kiro AI provider.
package kiro

import (
	"regexp"
	"strings"
)

const (
	// DefaultAwsRegion is the default AWS region for Kiro endpoints.
	DefaultAwsRegion = "us-east-1"

	// KiroAuthService is the endpoint for Kiro social login and token refresh.
	KiroAuthService = "https://prod.us-east-1.auth.desktop.kiro.dev"

	// DefaultStartURL is the default start URL for AWS Builder ID.
	DefaultStartURL = "https://view.awsapps.com/start"

	// ClientName and ClientType for OIDC client registration.
	DefaultClientName = "kiro-oauth-client"
	DefaultClientType = "public"

	// RedirectURI used for Kiro Social Login.
	SocialRedirectURI = "kiro://kiro.kiroAgent/authenticate-success"

	// DefaultBuilderIDProfileArn is the shared fallback profile ARN for AWS Builder ID.
	DefaultBuilderIDProfileArn = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"

	// DefaultSocialProfileArn is the shared fallback profile ARN for Kiro social auth (Google/GitHub).
	DefaultSocialProfileArn = "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK"
)

// DefaultProfileArnForMethod returns the fallback profile ARN for a given auth method.
func DefaultProfileArnForMethod(authMethod string) string {
	switch strings.ToLower(strings.TrimSpace(authMethod)) {
	case "google", "github", "social":
		return DefaultSocialProfileArn
	default:
		return DefaultBuilderIDProfileArn
	}
}


var (
	// DefaultScopes for CodeWhisperer/Kiro OIDC requests.
	DefaultScopes = []string{
		"codewhisperer:completions",
		"codewhisperer:analysis",
		"codewhisperer:conversations",
	}

	// DefaultGrantTypes for OIDC registration.
	DefaultGrantTypes = []string{
		"urn:ietf:params:oauth:grant-type:device_code",
		"refresh_token",
	}

	// AwsRegionPattern validates AWS region strings to prevent injection.
	AwsRegionPattern = regexp.MustCompile(`^[a-z]{2}-[a-z]+-\d{1,2}$`)
)

// AssertValidAwsRegion validates that region matches AWS region format.
func AssertValidAwsRegion(region string) string {
	if region == "" {
		return DefaultAwsRegion
	}
	if !AwsRegionPattern.MatchString(region) {
		return DefaultAwsRegion
	}
	return region
}
