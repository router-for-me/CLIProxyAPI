package executor

import (
	"context"
	"net/http"
)

// neuralwattResponseSink captures Neuralwatt's provider-specific response
// metadata (cost, cache savings, energy, served service tier) into the usage
// record. Implemented fully in a later step.
type neuralwattResponseSink struct{}

func (neuralwattResponseSink) CaptureResponse(context.Context, http.Header, []byte) {}
func (neuralwattResponseSink) CaptureStreamHeaders(context.Context, http.Header)    {}
func (neuralwattResponseSink) CaptureStreamChunk(context.Context, []byte)           {}
