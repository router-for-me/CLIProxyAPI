package controller

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	errPluginProtocolObservationLimitExceeded   = errors.New("plugin protocol observation limit exceeded")
	errInvalidPluginProtocolObservationIdentity = errors.New("invalid plugin protocol observation identity")
)

type pluginProtocolObservationLimits struct {
	global    int
	perPlugin int
	perUser   int
	perToken  int
}

var defaultPluginProtocolObservationLimits = pluginProtocolObservationLimits{
	global:    128,
	perPlugin: 32,
	perUser:   4,
	perToken:  2,
}

var pluginProtocolObservationAdmissions = newPluginProtocolObservationLimiter(
	defaultPluginProtocolObservationLimits,
)

type pluginProtocolObservationLimitError struct {
	scope string
	limit int
}

func (e *pluginProtocolObservationLimitError) Error() string {
	return fmt.Sprintf("%s: %s capacity is %d", errPluginProtocolObservationLimitExceeded, e.scope, e.limit)
}

func (e *pluginProtocolObservationLimitError) Unwrap() error {
	return errPluginProtocolObservationLimitExceeded
}

type pluginProtocolObservationLimiter struct {
	mu sync.Mutex

	limits pluginProtocolObservationLimits
	global int
	plugin map[string]int
	user   map[int]int
	token  map[int]int
}

func newPluginProtocolObservationLimiter(limits pluginProtocolObservationLimits) *pluginProtocolObservationLimiter {
	return &pluginProtocolObservationLimiter{
		limits: limits,
		plugin: make(map[string]int),
		user:   make(map[int]int),
		token:  make(map[int]int),
	}
}

func (l *pluginProtocolObservationLimiter) acquire(
	pluginKey string,
	userID int,
	tokenID int,
) (func(), error) {
	pluginKey = strings.TrimSpace(pluginKey)
	switch {
	case pluginKey == "":
		return nil, fmt.Errorf("%w: plugin key is required", errInvalidPluginProtocolObservationIdentity)
	case userID <= 0:
		return nil, fmt.Errorf("%w: user id must be positive", errInvalidPluginProtocolObservationIdentity)
	case tokenID <= 0:
		return nil, fmt.Errorf("%w: token id must be positive", errInvalidPluginProtocolObservationIdentity)
	}

	l.mu.Lock()
	if l.global >= l.limits.global {
		l.mu.Unlock()
		return nil, &pluginProtocolObservationLimitError{
			scope: "global",
			limit: l.limits.global,
		}
	}
	l.global++

	if l.plugin[pluginKey] >= l.limits.perPlugin {
		l.global--
		l.mu.Unlock()
		return nil, &pluginProtocolObservationLimitError{
			scope: "plugin",
			limit: l.limits.perPlugin,
		}
	}
	l.plugin[pluginKey]++

	if l.user[userID] >= l.limits.perUser {
		l.global--
		l.plugin[pluginKey]--
		if l.plugin[pluginKey] == 0 {
			delete(l.plugin, pluginKey)
		}
		l.mu.Unlock()
		return nil, &pluginProtocolObservationLimitError{
			scope: "user",
			limit: l.limits.perUser,
		}
	}
	l.user[userID]++

	if l.token[tokenID] >= l.limits.perToken {
		l.global--
		l.plugin[pluginKey]--
		if l.plugin[pluginKey] == 0 {
			delete(l.plugin, pluginKey)
		}
		l.user[userID]--
		if l.user[userID] == 0 {
			delete(l.user, userID)
		}
		l.mu.Unlock()
		return nil, &pluginProtocolObservationLimitError{
			scope: "token",
			limit: l.limits.perToken,
		}
	}
	l.token[tokenID]++
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()

			l.global--

			l.plugin[pluginKey]--
			if l.plugin[pluginKey] == 0 {
				delete(l.plugin, pluginKey)
			}

			l.user[userID]--
			if l.user[userID] == 0 {
				delete(l.user, userID)
			}

			l.token[tokenID]--
			if l.token[tokenID] == 0 {
				delete(l.token, tokenID)
			}
		})
	}, nil
}
