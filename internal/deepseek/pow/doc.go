// Package pow implements DeepSeek's proof-of-work challenge solver.
//
// The web client must solve an SHA3-512 PoW challenge (difficulty + prefix)
// before chat completion requests are accepted. The solution is sent in the
// x-ds-pow-response header. This package ports the solver from ds2api.
package pow
