// Package signature exports Claude message signature sanitizers for embedding
// hosts that previously duplicated internal/signature.
//
// Deletion criterion for host copies: import this package and delete the local
// sanitize implementation once the release that contains it is pinned.
package signature
