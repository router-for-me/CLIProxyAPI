package executor

import "time"

type codexCache struct {
	ID     string
	Expire time.Time
}

var codexCacheMap = map[string]codexCache{}
