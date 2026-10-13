package registry

import (
	"fmt"
	"net/url"
	"path/filepath"
)

// CatalogSources overrides the source of each model catalog independently.
// Empty values retain the process default (embedded or official remote catalogs).
type CatalogSources struct {
	Catalog      string `yaml:"catalog" json:"catalog,omitempty"`
	CodexCatalog string `yaml:"codex-catalog" json:"codex-catalog,omitempty"`
	DevinCatalog string `yaml:"devin-catalog" json:"devin-catalog,omitempty"`
}

// Validate rejects relative paths and unsupported URL schemes.
func (m CatalogSources) Validate() error {
	for name, source := range map[string]string{"catalog": m.Catalog, "codex-catalog": m.CodexCatalog, "devin-catalog": m.DevinCatalog} {
		if source == "" || filepath.IsAbs(source) {
			continue
		}
		parsed, errParse := url.Parse(source)
		if errParse != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
			return fmt.Errorf("models.%s must be an http(s) URL or an absolute local path", name)
		}
	}
	return nil
}
