package selfupdate

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// window is a daily local-time range [start, end) in minutes after midnight. It may wrap past midnight.
type window struct {
	start, end int
	any        bool
}

func parseClock(s string) (int, error) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, fmt.Errorf("invalid time %q (want HH:MM)", s)
	}
	hh, errH := strconv.Atoi(h)
	mm, errM := strconv.Atoi(m)
	if errH != nil || errM != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 || len(m) != 2 {
		return 0, fmt.Errorf("invalid time %q (want HH:MM)", s)
	}
	return hh*60 + mm, nil
}

// parseWindow parses "HH:MM-HH:MM". Empty means any time. Equal start and end also mean any time.
func parseWindow(s string) (window, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return window{any: true}, nil
	}
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return window{}, fmt.Errorf("invalid window %q (want HH:MM-HH:MM)", s)
	}
	start, err := parseClock(a)
	if err != nil {
		return window{}, err
	}
	end, err := parseClock(b)
	if err != nil {
		return window{}, err
	}
	if start == end {
		return window{any: true}, nil
	}
	return window{start: start, end: end}, nil
}

// contains reports whether the local time t falls inside the window.
func (w window) contains(t time.Time) bool {
	if w.any {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if w.start < w.end {
		return m >= w.start && m < w.end
	}
	return m >= w.start || m < w.end // wraps midnight
}
