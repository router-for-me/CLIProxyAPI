package discovery

import "testing"

func TestReviewNormalizeBrowseQuery(t *testing.T) {
	for _, input := range []string{"_responses._sub._ai-gateway._tcp", "_ai-gateway._tcp,_responses", "_RESPONSES._sub._ai-gateway._tcp.LOCAL."} {
		t.Run(input, func(t *testing.T) {
			service, domain, err := normalizeBrowseQuery(input, "local.")
			if err != nil || service != "_responses._sub._ai-gateway._tcp" || domain != "local." {
				t.Fatalf("normalized=(%q,%q,%v)", service, domain, err)
			}
		})
	}
	for _, input := range []string{"_responses._sub._ai-gateway._tcp,_images", "x._sub._ai-gateway._tcp", "_responses._sub._invalid._udp", "_ai--gateway._tcp", "_123._tcp"} {
		t.Run(input, func(t *testing.T) {
			if _, _, err := normalizeBrowseQuery(input, "local."); err == nil {
				t.Fatal("accepted invalid browse name")
			}
		})
	}
	if _, _, err := normalizeBrowseQuery("", "local..bad"); err == nil {
		t.Fatal("accepted malformed domain")
	}
}
