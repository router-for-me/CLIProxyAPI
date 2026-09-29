package ratio_setting

import "sync/atomic"

var exposeRatioEnabled atomic.Bool

func init() {
	exposeRatioEnabled.Store(false)
}

func SetExposeRatioEnabled(enabled bool) {
	exposeRatioEnabled.Store(enabled)
}

func IsExposeRatioEnabled() bool {
	return exposeRatioEnabled.Load()
}
