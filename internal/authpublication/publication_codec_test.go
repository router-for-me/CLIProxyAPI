package authpublication

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
)

func TestPublicationJournalLegacyFormatSurvivesUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account.json")
	data := []byte{0, 0xff, '\n', '"', '\\'}
	// A journal stores opaque bytes, including bytes that cannot be JSON text.
	legacy := `{"name":"account.json","data":"AP8KIlw=","committed":true}`
	if err := writeBytes(markerPath(path), []byte(legacy)); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("legacy publication read = %v, %v", got, err)
	}
	paths, err := Paths(filepath.Dir(path))
	if err != nil || len(paths) != 1 || paths[0] != path {
		t.Fatalf("legacy publication discovery = %v, %v", paths, err)
	}
	pending, err := Prepare(path, []byte("replacement"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path); !errors.Is(err, ErrPending) {
		t.Fatalf("uncommitted replacement became readable: %v", err)
	}
	if err := pending.Abort(); err != nil {
		t.Fatal(err)
	}
	if err := Repair(path); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) || HasPending(path) {
		t.Fatalf("legacy publication repair = %v, %v", got, err)
	}
}

func TestPublicationJournalRetainsBase64WireFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account.json")
	data := []byte(`{"access_token":"replacement"}`)
	pending, err := Prepare(path, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Commit(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(markerPath(path))
	if err != nil {
		t.Fatal(err)
	}
	journal := ojson.ParseBytes(raw)
	if !ojson.ValidBytes(raw) || journal.Get("name").String() != "account.json" || journal.Get("committed").Type != ojson.True {
		t.Fatalf("invalid committed journal: %s", raw)
	}
	if encoded := journal.Get("data"); encoded.Type != ojson.String || encoded.String() != base64.StdEncoding.EncodeToString(data) {
		t.Fatalf("journal data lost its base64 representation: %s", raw)
	}
}

func TestPublicationJournalRejectsMalformedRecords(t *testing.T) {
	for _, raw := range []string{
		`{"name":"account.json","data":"e30=","committed":true} trailing`,
		`{"name":"account.json","data":"e30=","committed":"true"}`,
		`{"name":"account.json","data":"not base64","committed":true}`,
		`{"name":"account.json","data":123,"committed":true}`,
		`{"name":false,"data":"e30=","committed":true}`,
	} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "account.json")
			if err := writeBytes(markerPath(path), []byte(raw)); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadFile(path); err == nil {
				t.Fatal("malformed journal exposed a credential")
			}
			if _, err := Paths(filepath.Dir(path)); err == nil {
				t.Fatal("malformed journal was accepted during discovery")
			}
		})
	}
}
