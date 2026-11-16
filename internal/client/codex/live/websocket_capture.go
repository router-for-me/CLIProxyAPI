package live

import "io"

type observedWebsocketWriter struct {
	destination io.Writer
	observer    io.Writer
}

func (w observedWebsocketWriter) Write(data []byte) (int, error) {
	n, err := w.destination.Write(data)
	if n > 0 {
		_, _ = w.observer.Write(data[:n])
	}
	return n, err
}
