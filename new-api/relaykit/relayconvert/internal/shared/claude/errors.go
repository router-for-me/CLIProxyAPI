package claude

import "errors"

// ErrMissingMaxTokens is returned when an OpenAI-format request carries no
// usable max_tokens and no Options.Claude.DefaultMaxTokens hook is
// configured. The Claude Messages API rejects requests without max_tokens
// (400 "max_tokens: Field required"), so conversion fails loudly instead of
// emitting a request the upstream is guaranteed to refuse.
var ErrMissingMaxTokens = errors.New("claude messages request requires max_tokens: set max_tokens on the request or configure Options.Claude.DefaultMaxTokens")
