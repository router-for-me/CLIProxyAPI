package claude

// ReadMetadataBool reads a boolean under the same lock used by metadata writers.
func ReadMetadataBool(metadata *map[string]any, key string) bool {
	if metadata == nil {
		return false
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()
	value, _ := (*metadata)[key].(bool)
	return value
}
