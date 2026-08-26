//go:build !linux

package watcher

import "context"

type configCompletionWatcher struct{}

func newConfigCompletionWatcher(string) (*configCompletionWatcher, error) {
	return nil, nil
}

func (*configCompletionWatcher) Start(context.Context, func(), func(error), func()) error { return nil }
func (*configCompletionWatcher) Close() error                                             { return nil }
