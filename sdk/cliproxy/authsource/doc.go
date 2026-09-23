// Package authsource reconciles an external credential catalog into a conductor
// manager.
//
// The file watcher under internal/watcher only sees the auth directory and
// config-synthesized API keys. Embedders that own credentials elsewhere (a
// database catalog whose ids use OwnedIDPrefix) previously had to copy that
// diff themselves: register new rows, copy operator fields onto the live auth
// without replacing conductor tokens, drop rows that left the catalog, and
// remove config-synthesized twins. NewWatcher is that constructor.
//
// Token metadata stays on the live auth unless AttrTokenRotatedAt changes.
// Config-synthesized auths that duplicate a catalog identity are removed when
// a twin can still serve; a shadow that is the only holder of base_url is kept.
package authsource
