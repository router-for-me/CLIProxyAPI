package router

import "time"

const resultAttachWindow = 10 * time.Minute

// decisionLog is a fixed-size ring of recent decisions.
type decisionLog struct {
	items []Decision
	next  int
	full  bool
	seq   uint64
}

func newDecisionLog(size int) decisionLog {
	if size < 1 {
		size = 1
	}
	return decisionLog{items: make([]Decision, size)}
}

func (l *decisionLog) add(d Decision) uint64 {
	l.seq++
	d.ID = l.seq
	l.items[l.next] = d
	l.next = (l.next + 1) % len(l.items)
	if l.next == 0 {
		l.full = true
	}
	return d.ID
}

// recent returns decisions newest first.
func (l *decisionLog) recent() []Decision {
	n := l.next
	if l.full {
		n = len(l.items)
	}
	out := make([]Decision, 0, n)
	for i := 0; i < n; i++ {
		idx := (l.next - 1 - i + len(l.items)) % len(l.items)
		out = append(out, l.items[idx])
	}
	return out
}

func (l *decisionLog) resize(size int) {
	if size < 1 || size == len(l.items) {
		return
	}
	recent := l.recent()
	seq := l.seq
	*l = newDecisionLog(size)
	l.seq = seq
	for i := min(len(recent), size) - 1; i >= 0; i-- {
		l.items[l.next] = recent[i]
		l.next = (l.next + 1) % size
		if l.next == 0 {
			l.full = true
		}
	}
}

// attach records an upstream result on the newest matching decision that has
// none yet. Picks and usage records carry no shared ID, so matching is by
// provider and session within a short window.
func (l *decisionLog) attach(provider, sessionKey string, res Result, now time.Time) {
	n := l.next
	if l.full {
		n = len(l.items)
	}
	for i := 0; i < n; i++ {
		idx := (l.next - 1 - i + len(l.items)) % len(l.items)
		d := &l.items[idx]
		if now.Sub(d.At) > resultAttachWindow {
			return
		}
		if d.Provider != provider || d.sessionFull != sessionKey || d.Result != nil {
			continue
		}
		r := res
		d.Result = &r
		return
	}
}
