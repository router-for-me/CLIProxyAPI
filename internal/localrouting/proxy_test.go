package localrouting

import "testing"

func TestFindRouteWildcard(t *testing.T) {
	routes := []RouteInfo{{Host: "myapp.localhost", TargetHost: "127.0.0.1", TargetPort: 4100}}
	if _, ok := findRoute("myapp.localhost", routes); !ok {
		t.Fatal("expected exact route")
	}
	route, ok := findRoute("tenant1.myapp.localhost", routes)
	if !ok {
		t.Fatal("expected wildcard route")
	}
	if route.TargetPort != 4100 {
		t.Fatalf("target_port = %d, want 4100", route.TargetPort)
	}
}

func TestNormalizeRequestHost(t *testing.T) {
	if got := normalizeRequestHost("MyApp.Localhost:1355"); got != "myapp.localhost" {
		t.Fatalf("normalizeRequestHost = %q", got)
	}
}
