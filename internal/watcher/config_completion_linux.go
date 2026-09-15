//go:build linux

package watcher

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

type completionTarget struct {
	dir  string
	base string
	wd   int32
}

type configCompletionWatcher struct {
	targets  []completionTarget
	fd       int
	done     chan struct{}
	cancel   context.CancelFunc
	closeMu  sync.Mutex
	started  bool
	closed   bool
	closeErr error
}

func newConfigCompletionWatcher(configPaths ...string) (configCompletion, error) {
	targets := make([]completionTarget, 0, len(configPaths))
	seen := make(map[completionTarget]struct{}, len(configPaths))
	for _, configPath := range configPaths {
		path, err := filepath.Abs(configPath)
		if err != nil {
			return nil, fmt.Errorf("resolve config path: %w", err)
		}
		target := completionTarget{dir: filepath.Dir(path), base: filepath.Base(path)}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}
	return &configCompletionWatcher{
		targets: targets,
		fd:      -1,
		done:    make(chan struct{}),
	}, nil
}

func (w *configCompletionWatcher) Start(ctx context.Context, complete func(), unavailable func(error), admitted func()) error {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return fmt.Errorf("create config completion watcher: %w", err)
	}
	for i := range w.targets {
		wd, errWatch := unix.InotifyAddWatch(fd, w.targets[i].dir, unix.IN_CLOSE_WRITE|unix.IN_MOVED_TO)
		if errWatch != nil {
			_ = unix.Close(fd)
			return fmt.Errorf("watch config completion in %s: %w", w.targets[i].dir, errWatch)
		}
		w.targets[i].wd = int32(wd)
	}
	runCtx, cancel := context.WithCancel(ctx)

	w.closeMu.Lock()
	if w.started || w.closed {
		w.closeMu.Unlock()
		cancel()
		_ = unix.Close(fd)
		return os.ErrClosed
	}
	w.fd = fd
	w.cancel = cancel
	w.started = true
	w.closeMu.Unlock()

	if admitted != nil {
		admitted()
	}
	go w.readEvents(runCtx, fd, complete, unavailable)
	return nil
}

func (w *configCompletionWatcher) Close() error {
	w.closeMu.Lock()
	if !w.closed {
		w.closed = true
		if w.cancel != nil {
			w.cancel()
		}
	}
	started := w.started
	done := w.done
	w.closeMu.Unlock()

	if started {
		<-done
	}

	w.closeMu.Lock()
	err := w.closeErr
	w.closeMu.Unlock()
	return err
}

func (w *configCompletionWatcher) readEvents(ctx context.Context, fd int, complete func(), unavailable func(error)) {
	expectedStop := false
	defer func() {
		errClose := unix.Close(fd)
		w.closeMu.Lock()
		w.fd = -1
		w.closed = true
		if errClose != nil && !errors.Is(errClose, unix.EBADF) {
			w.closeErr = errClose
		}
		w.closeMu.Unlock()
		close(w.done)
		if !expectedStop {
			unavailable(configCompletionError("stopped", errors.New("event stream ended")))
		}
	}()

	buf := make([]byte, 4096)
	pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		select {
		case <-ctx.Done():
			expectedStop = true
			return
		default:
		}

		pollFDs[0].Revents = 0
		ready, errPoll := unix.Poll(pollFDs, 100)
		if errors.Is(errPoll, unix.EINTR) {
			continue
		}
		if errPoll != nil {
			unavailable(configCompletionError("poll", errPoll))
			return
		}
		if pollFDs[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			unavailable(configCompletionError("poll", fmt.Errorf("revents %#x", pollFDs[0].Revents)))
			return
		}
		if ready == 0 || pollFDs[0].Revents&unix.POLLIN == 0 {
			continue
		}

		n, errRead := unix.Read(fd, buf)
		if errors.Is(errRead, unix.EINTR) || errors.Is(errRead, unix.EAGAIN) {
			continue
		}
		if errRead != nil {
			unavailable(configCompletionError("read", errRead))
			return
		}
		if errDecode := w.handleEvents(buf[:n], complete); errDecode != nil {
			unavailable(errDecode)
			return
		}
	}
}

func (w *configCompletionWatcher) handleEvents(buf []byte, complete func()) error {
	for offset := 0; offset+unix.SizeofInotifyEvent <= len(buf); {
		raw := buf[offset:]
		mask := binary.NativeEndian.Uint32(raw[4:8])
		wd := int32(binary.NativeEndian.Uint32(raw[0:4]))
		nameLen := int(binary.NativeEndian.Uint32(raw[12:16]))
		eventLen := unix.SizeofInotifyEvent + nameLen
		if eventLen > len(buf)-offset {
			return configCompletionError("decode", errors.New("truncated event"))
		}
		if mask&unix.IN_Q_OVERFLOW != 0 {
			return configCompletionError("watch", errors.New("event queue overflow"))
		}
		if mask&unix.IN_IGNORED != 0 {
			return configCompletionError("watch", errors.New("watch was removed"))
		}
		if nameLen > 0 {
			name := raw[unix.SizeofInotifyEvent:eventLen]
			for len(name) > 0 && name[len(name)-1] == 0 {
				name = name[:len(name)-1]
			}
			if mask&(unix.IN_CLOSE_WRITE|unix.IN_MOVED_TO) != 0 && w.matchesTarget(wd, string(name)) {
				complete()
			}
		}
		offset += eventLen
	}
	return nil
}

func (w *configCompletionWatcher) matchesTarget(wd int32, name string) bool {
	for _, target := range w.targets {
		if target.wd == wd && target.base == name {
			return true
		}
	}
	return false
}
