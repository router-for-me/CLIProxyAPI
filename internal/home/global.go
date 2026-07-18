package home

import "sync/atomic"

var currentClient atomic.Value // *Client

// SetCurrent sets the active home client used by runtime integrations.
func SetCurrent(client *Client) {
	currentClient.Store(client)
}

// Current returns the active home client instance, if any.
func Current() *Client {
	if v := currentClient.Load(); v != nil {
		if client, ok := v.(*Client); ok {
			return client
		}
	}
	return nil
}

// ClearCurrent removes the active home client.
func ClearCurrent() {
	currentClient.Store((*Client)(nil))
}
