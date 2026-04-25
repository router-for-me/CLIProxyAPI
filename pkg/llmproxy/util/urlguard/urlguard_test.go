package urlguard

import "testing"

func TestValidateOutboundURL_Allowed(t *testing.T) {
	cases := []string{
		"https://oidc.us-east-1.amazonaws.com/token",
		"https://codewhisperer.us-east-1.amazonaws.com",
		"https://cloudcode-pa.googleapis.com/v1/models",
		"https://oauth2.googleapis.com/token",
	}
	for _, u := range cases {
		got, err := ValidateOutboundURL(u)
		if err != nil {
			t.Errorf("ValidateOutboundURL(%q) error: %v", u, err)
			continue
		}
		if got != u {
			t.Errorf("ValidateOutboundURL(%q) = %q; want unchanged", u, got)
		}
	}
}

func TestValidateOutboundURL_Rejected(t *testing.T) {
	cases := []string{
		"",
		"not a url",
		"file:///etc/passwd",
		"https://evil.example.com/",
		"http://169.254.169.254/latest/meta-data/",
		"https://user:pass@oidc.us-east-1.amazonaws.com/",
		"https://evilamazonaws.com/",
	}
	for _, u := range cases {
		if _, err := ValidateOutboundURL(u); err == nil {
			t.Errorf("ValidateOutboundURL(%q) unexpectedly allowed", u)
		}
	}
}
