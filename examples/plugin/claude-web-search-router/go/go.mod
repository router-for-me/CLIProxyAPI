module github.com/router-for-me/CLIProxyAPI/v8/examples/plugin/claude-web-search-router/go

go 1.26.10

require (
	github.com/MoeForge/moego v0.1.0-alpha.5
	github.com/router-for-me/CLIProxyAPI/v8 v8.0.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/sirupsen/logrus v1.9.3 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/router-for-me/CLIProxyAPI/v8 => ../../../..

replace github.com/MoeForge/moego => github.com/router-for-me/moego v0.1.3
