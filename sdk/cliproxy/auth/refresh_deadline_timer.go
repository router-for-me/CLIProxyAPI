package auth

import "time"

type refreshDeadlineTimer interface {
	Chan() <-chan time.Time
	Reset(time.Duration)
	Stop()
	Close()
}

type goRefreshDeadlineTimer struct {
	timer *time.Timer
}

func newGoRefreshDeadlineTimer(d time.Duration) refreshDeadlineTimer {
	return &goRefreshDeadlineTimer{timer: time.NewTimer(d)}
}

func (t *goRefreshDeadlineTimer) Chan() <-chan time.Time {
	return t.timer.C
}

func (t *goRefreshDeadlineTimer) Reset(d time.Duration) {
	if !t.timer.Stop() {
		select {
		case <-t.timer.C:
		default:
		}
	}
	t.timer.Reset(d)
}

func (t *goRefreshDeadlineTimer) Stop() {
	if !t.timer.Stop() {
		select {
		case <-t.timer.C:
		default:
		}
	}
}

func (t *goRefreshDeadlineTimer) Close() {
	t.Stop()
}
