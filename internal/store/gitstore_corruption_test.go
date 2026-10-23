package store

import (
	"fmt"
	"io/fs"
	"testing"
)

func TestMissingPackErrorIsNarrowlyClassified(t *testing.T) {
	for _, tt := range []struct {
		path string
		want bool
	}{
		{"objects/pack/pack-fixture.pack", true},
		{"objects/pack/pack-fixture.idx", false},
		{"config", false},
		{"auths/key.json", false},
	} {
		err := fmt.Errorf("packhandle: pack size: %w", &fs.PathError{Op: "statat", Path: tt.path, Err: fs.ErrNotExist})
		if got := isRepositoryCorruptionError(err); got != tt.want {
			t.Errorf("%s: corruption=%v, want %v", tt.path, got, tt.want)
		}
	}
}
