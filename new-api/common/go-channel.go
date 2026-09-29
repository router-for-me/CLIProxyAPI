package common

import (
	"time"
)

func SafeSendBool(ch chan bool, value bool) (closed bool) {
	defer func() {
		// Recover from panic if one occured. A panic would mean the channel was closed.
		if recover() != nil {
			closed = true
		}
	}()

	// This will panic if the channel is closed.
	ch <- value

	// If the code reaches here, then the channel was not closed.
	return false
}

func SafeSendString(ch chan string, value string) (closed bool) {
	defer func() {
		// Recover from panic if one occured. A panic would mean the channel was closed.
		if recover() != nil {
			closed = true
		}
	}()

	// This will panic if the channel is closed.
	ch <- value

	// If the code reaches here, then the channel was not closed.
	return false
}

// SafeSendStringTimeout send, return true, else return false
func SafeSendStringTimeout(ch chan string, value string, timeout int) (closed bool) {
	defer func() {
		// Recover from panic if one occured. A panic would mean the channel was closed.
		if recover() != nil {
			closed = false
		}
	}()

	// This will panic if the channel is closed.
	select {
	case ch <- value:
		return true
	case <-time.After(time.Duration(timeout) * time.Second):
		return false
	}
}
