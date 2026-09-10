package home

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	log "github.com/sirupsen/logrus"
)

// WaitForConfig tolerates transient transport failures during the caller's
// bootstrap deadline. It never substitutes cached config or retries a command
// with side effects. Authentication and configuration errors fail immediately.
func (c *Client) WaitForConfig(ctx context.Context) ([]byte, error) {
	ticker := time.NewTicker(homeReconnectInterval)
	defer ticker.Stop()
	return waitForConfig(ctx, c.GetConfig, ticker.C)
}

func waitForConfig(ctx context.Context, fetch func(context.Context) ([]byte, error), ticks <-chan time.Time) ([]byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := fetch(ctx)
		if err == nil {
			return raw, nil
		}
		var networkError net.Error
		if !errors.As(err, &networkError) && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, err
		}
		// Do not log raw transport errors: they can contain credential URLs.
		log.Warn("Home config transport unavailable; retrying within bootstrap deadline")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticks:
		}
	}
}
