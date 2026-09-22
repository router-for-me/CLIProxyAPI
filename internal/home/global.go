package home

import "sync/atomic"

var currentClient atomic.Pointer[Client]

// SetCurrent sets the active home client used by runtime integrations.
func SetCurrent(client *Client) {
	currentClient.Store(client)
}

// Current returns the active home client instance, if any.
func Current() *Client {
	return currentClient.Load()
}

// ClearCurrent removes the active home client.
func ClearCurrent() {
	currentClient.Store(nil)
}

// ClearCurrentIf removes the active client only when it is client.
func ClearCurrentIf(client *Client) {
	if client != nil {
		currentClient.CompareAndSwap(client, nil)
	}
}
