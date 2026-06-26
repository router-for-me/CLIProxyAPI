// Package builtin exposes the built-in translator registrations for SDK users.
package builtin

import (
<<<<<<< HEAD
	sdktranslator "github.com/kooshapari/CLIProxyAPI/v7/sdk/translator"

	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator"
=======
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
>>>>>>> upstream/main
)

// Registry exposes the default registry populated with all built-in translators.
func Registry() *sdktranslator.Registry {
	return sdktranslator.Default()
}

// Pipeline returns a pipeline that already contains the built-in translators.
func Pipeline() *sdktranslator.Pipeline {
	return sdktranslator.NewPipeline(sdktranslator.Default())
}
