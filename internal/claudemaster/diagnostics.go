package claudemaster

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

func startProxyDiagnostics(proxy *Proxy, output io.Writer) func() {
	if output == nil {
		return func() {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		encoder := json.NewEncoder(output)
		emit := func() { _ = encoder.Encode(proxy.Snapshot()) }
		for {
			select {
			case <-ticker.C:
				emit()
			case <-stop:
				emit()
				return
			}
		}
	}()
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}
