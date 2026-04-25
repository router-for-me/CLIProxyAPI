// Package translatorcommon provides shared translator utilities.
package translatorcommon

import "fmt"

// FormatEndpoint formats a URL endpoint.
func FormatEndpoint(base, path string) string {
	return fmt.Sprintf("%s/%s", base, path)
}
