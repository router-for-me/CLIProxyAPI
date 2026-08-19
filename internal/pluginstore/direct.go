package pluginstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

func SelectArtifact(plan InstallPlan, goos string, goarch string) (Artifact, error) {
	plan = NormalizeInstallPlan(plan)
	goos = normalizeGOOS(goos)
	goarch = normalizeGOARCH(goarch)
	if plan.Type != InstallTypeDirect {
		return Artifact{}, fmt.Errorf("install type %q is not direct", plan.Type)
	}
	for _, artifact := range plan.Artifacts {
		if artifact.GOOS == goos && artifact.GOARCH == goarch {
			return artifact, nil
		}
	}
	return Artifact{}, fmt.Errorf("artifact not found for %s/%s", goos, goarch)
}

func (c Client) DownloadArtifact(ctx context.Context, artifact Artifact) ([]byte, error) {
	artifact = NormalizeInstallPlan(InstallPlan{Type: InstallTypeDirect, Artifacts: []Artifact{artifact}}).Artifacts[0]
	if errValidate := ValidateArtifact(artifact); errValidate != nil {
		return nil, errValidate
	}
	maxSize := int64(0)
	if artifact.Size > 0 {
		maxSize = artifact.Size
	}
	data, errDownload := c.get(ctx, artifact.URL, "application/octet-stream", RequestKindArtifact, maxSize)
	if errDownload != nil {
		return nil, errDownload
	}
	if maxSize > 0 && int64(len(data)) > maxSize {
		return nil, fmt.Errorf("artifact exceeds declared size")
	}
	return data, nil
}

func VerifyArtifactChecksum(artifact Artifact, data []byte) error {
	expected := strings.ToLower(strings.TrimSpace(artifact.SHA256))
	if expected == "" {
		return fmt.Errorf("artifact checksum missing")
	}
	actualBytes := sha256.Sum256(data)
	actual := hex.EncodeToString(actualBytes[:])
	if actual != expected {
		return fmt.Errorf("artifact checksum mismatch")
	}
	return nil
}
