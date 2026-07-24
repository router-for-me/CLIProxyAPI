package pluginhost

type schemaVersionedPluginClient interface {
	setSchemaVersion(uint32)
}

func setPluginClientSchemaVersion(client pluginClient, schemaVersion uint32) {
	if client == nil {
		return
	}
	if versioned, ok := client.(schemaVersionedPluginClient); ok {
		versioned.setSchemaVersion(schemaVersion)
	}
}

func (c *guardedPluginClient) setSchemaVersion(schemaVersion uint32) {
	if c == nil {
		return
	}
	c.mu.Lock()
	inner := c.inner
	c.mu.Unlock()
	setPluginClientSchemaVersion(inner, schemaVersion)
}
