package otelplugin

import "go.opentelemetry.io/otel/codes"

// Shorthand aliases to keep plugin.go free of the (otel-codes vs http-codes)
// confusion. The OTel trace package uses codes.Code for span status; HTTP
// status lives on attributes. Centralising the import here also makes the
// mocking surface in tests trivially small.
var (
	codeOk    = codes.Ok
	codeError = codes.Error
)
