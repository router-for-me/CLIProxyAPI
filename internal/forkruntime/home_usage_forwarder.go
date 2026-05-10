package forkruntime

import (
	"context"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
)

// HomeUsageSink is the home client surface required for usage forwarding.
type HomeUsageSink interface {
	HeartbeatOK() bool
	LPushUsage(context.Context, []byte) error
}

// StartHomeUsageForwarder starts forwarding queued usage payloads to the home client.
func StartHomeUsageForwarder(ctx context.Context, sink HomeUsageSink) {
	if sink == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go runHomeUsageForwarder(ctx, sink)
}

func runHomeUsageForwarder(ctx context.Context, sink HomeUsageSink) {
	sleep := func(d time.Duration) bool {
		if d <= 0 {
			return true
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !sink.HeartbeatOK() {
			if !sleep(time.Second) {
				return
			}
			continue
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		items := redisqueue.PopOldest(64)
		if len(items) == 0 {
			if !sleep(500 * time.Millisecond) {
				return
			}
			continue
		}

		for i := range items {
			select {
			case <-ctx.Done():
				reenqueue(items[i:])
				return
			default:
			}

			if err := sink.LPushUsage(ctx, items[i]); err != nil {
				reenqueue(items[i:])
				if !sleep(time.Second) {
					return
				}
				break
			}
		}
	}
}

func reenqueue(items [][]byte) {
	for _, item := range items {
		redisqueue.Enqueue(item)
	}
}
