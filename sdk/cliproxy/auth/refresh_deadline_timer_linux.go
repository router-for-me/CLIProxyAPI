//go:build linux

package auth

import (
	"encoding/binary"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

type linuxBootTimeTimer struct {
	timerFD int
	closeFD int
	c       chan time.Time
	done    chan struct{}
	once    sync.Once
}

func newRefreshDeadlineTimer(d time.Duration) refreshDeadlineTimer {
	timerFD, err := unix.TimerfdCreate(unix.CLOCK_BOOTTIME, unix.TFD_CLOEXEC|unix.TFD_NONBLOCK)
	if err != nil {
		return newGoRefreshDeadlineTimer(d)
	}
	closeFD, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		_ = unix.Close(timerFD)
		return newGoRefreshDeadlineTimer(d)
	}

	t := &linuxBootTimeTimer{
		timerFD: timerFD,
		closeFD: closeFD,
		c:       make(chan time.Time, 1),
		done:    make(chan struct{}),
	}
	t.Reset(d)
	go t.run()
	return t
}

func (t *linuxBootTimeTimer) Chan() <-chan time.Time {
	return t.c
}

func (t *linuxBootTimeTimer) Reset(d time.Duration) {
	if d <= 0 {
		d = time.Nanosecond
	}
	spec := &unix.ItimerSpec{Value: unix.NsecToTimespec(d.Nanoseconds())}
	_ = unix.TimerfdSettime(t.timerFD, 0, spec, nil)
}

func (t *linuxBootTimeTimer) Stop() {
	_ = unix.TimerfdSettime(t.timerFD, 0, &unix.ItimerSpec{}, nil)
}

func (t *linuxBootTimeTimer) Close() {
	t.once.Do(func() {
		var wake [8]byte
		binary.NativeEndian.PutUint64(wake[:], 1)
		_, _ = unix.Write(t.closeFD, wake[:])
		<-t.done
	})
}

func (t *linuxBootTimeTimer) run() {
	defer close(t.done)
	defer func() {
		if err := unix.Close(t.timerFD); err != nil {
			log.Errorf("failed to close timerfd: %v", err)
		}
	}()
	defer func() {
		if err := unix.Close(t.closeFD); err != nil {
			log.Errorf("failed to close eventfd: %v", err)
		}
	}()

	pollFDs := []unix.PollFd{
		{Fd: int32(t.timerFD), Events: unix.POLLIN},
		{Fd: int32(t.closeFD), Events: unix.POLLIN},
	}
	var value [8]byte
	for {
		_, err := unix.Poll(pollFDs, -1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return
		}
		if pollFDs[1].Revents != 0 {
			return
		}
		if pollFDs[0].Revents&unix.POLLIN == 0 {
			if pollFDs[0].Revents != 0 {
				return
			}
			continue
		}
		if _, err = unix.Read(t.timerFD, value[:]); err != nil && err != unix.EAGAIN {
			return
		}
		select {
		case t.c <- time.Now():
		default:
		}
	}
}
