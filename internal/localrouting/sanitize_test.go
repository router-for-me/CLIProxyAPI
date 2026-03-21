package localrouting

import "testing"

func TestBuildHostAndURL(t *testing.T) {
	host := BuildHost("My_App", ".LOCALHOST")
	if host != "my-app.localhost" {
		t.Fatalf("host = %q, want my-app.localhost", host)
	}
	url := BuildURL(false, host, 1355)
	if url != "http://my-app.localhost:1355" {
		t.Fatalf("url = %q", url)
	}
}
