package modelrouting

import (
	"bytes"
	"errors"
	"testing"
)

func TestPreparePublicationOwnsOnlyRouting(t *testing.T) {
	host := []byte("# host settings\nserver: {port: 8317}\napi-keys: [existing-client-key]\n")
	projection := validConfig()
	projection.Generation = 1
	var err error
	projection.ProjectionDigest, err = ProjectionDigest(&projection)
	if err != nil {
		t.Fatal(err)
	}
	body, err := RoutingYAML(&projection)
	if err != nil {
		t.Fatal(err)
	}
	data, _, identity, err := PreparePublication(host, body, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, host) || identity.Generation != 1 || identity.ConfigDigest != ConfigDigest(body) {
		t.Fatalf("publication did not preserve host settings or attest the resource: %#v", identity)
	}
	if _, _, _, err = PreparePublication(data, body, &identity, nil); !errors.Is(err, ErrCASConflict) {
		t.Fatalf("duplicate bootstrap: %v", err)
	}
	if _, _, _, err = PreparePublication(host, append([]byte("api-keys: [replacement]\n"), body...), nil, nil); !errors.Is(err, ErrInvalidPublication) {
		t.Fatalf("mixed payload: %v", err)
	}
}
