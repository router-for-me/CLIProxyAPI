package localrouting

import "testing"

func TestFindFreePort(t *testing.T) {
	port, errFind := FindFreePort(4900, 4910)
	if errFind != nil {
		t.Fatalf("find free port: %v", errFind)
	}
	if port < 4900 || port > 4910 {
		t.Fatalf("port = %d, out of range", port)
	}
}
