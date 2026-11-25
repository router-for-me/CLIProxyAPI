package pluginhost

import (
	"go/build"
	"testing"
)

func TestSupportFilesForWindowsWithoutCGO(t *testing.T) {
	buildContext := build.Default
	buildContext.GOOS = "windows"
	buildContext.CgoEnabled = false

	matchSupported, errMatchSupported := buildContext.MatchFile(".", "support_cgo.go")
	if errMatchSupported != nil {
		t.Fatalf("match support_cgo.go: %v", errMatchSupported)
	}
	matchUnsupported, errMatchUnsupported := buildContext.MatchFile(".", "support_nocgo.go")
	if errMatchUnsupported != nil {
		t.Fatalf("match support_nocgo.go: %v", errMatchUnsupported)
	}

	if !matchSupported {
		t.Fatal("support_cgo.go is excluded from Windows builds without CGO")
	}
	if matchUnsupported {
		t.Fatal("support_nocgo.go is included in Windows builds without CGO")
	}
}
