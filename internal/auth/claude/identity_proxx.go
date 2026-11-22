package claude

// ReadMetadataBool reads a bool-valued metadata entry under the metadata lock,
// so it cannot observe a map being concurrently written by another path.
func ReadMetadataBool(metadata *map[string]any, key string) bool {
	if metadata == nil {
		return false
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		return false
	}
	value, _ := (*metadata)[key].(bool)
	return value
}
