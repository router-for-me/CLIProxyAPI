package common

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const maxConversionDiagnostics = 32

type conversionDiagnosticKey struct {
	code     string
	path     string
	severity types.ConversionDiagnosticSeverity
	from     types.RelayFormat
	to       types.RelayFormat
}

// RecordConversionDiagnostics retains conversion losses for the consume log
// and emits one request-correlated warning per distinct diagnostic. The cap
// prevents malformed streams from growing request state without bound.
func (info *RelayInfo) RecordConversionDiagnostics(ctx context.Context, diagnostics []types.ConversionDiagnostic) {
	if info == nil || len(diagnostics) == 0 {
		return
	}
	if ginCtx, ok := ctx.(*gin.Context); ok && ginCtx == nil {
		ctx = nil
	}
	if info.conversionDiagnosticKeys == nil {
		info.conversionDiagnosticKeys = make(map[conversionDiagnosticKey]struct{})
	}
	for _, diagnostic := range diagnostics {
		key := conversionDiagnosticKey{
			code:     diagnostic.Code,
			path:     diagnostic.Path,
			severity: diagnostic.Severity,
			from:     diagnostic.From,
			to:       diagnostic.To,
		}
		if _, exists := info.conversionDiagnosticKeys[key]; exists {
			continue
		}
		if len(info.conversionDiagnostics) >= maxConversionDiagnostics {
			if !info.conversionDiagnosticsTruncated {
				info.conversionDiagnosticsTruncated = true
				logger.LogWarn(ctx, fmt.Sprintf("conversion diagnostics truncated after %d distinct entries", maxConversionDiagnostics))
			}
			continue
		}
		info.conversionDiagnosticKeys[key] = struct{}{}
		info.conversionDiagnostics = append(info.conversionDiagnostics, diagnostic)
		logger.LogWarn(ctx, fmt.Sprintf(
			"conversion diagnostic: code=%q severity=%q from=%q to=%q path=%q message=%q",
			diagnostic.Code, diagnostic.Severity, diagnostic.From, diagnostic.To, diagnostic.Path, diagnostic.Message,
		))
	}
}

func (info *RelayInfo) ConversionDiagnostics() []types.ConversionDiagnostic {
	if info == nil || len(info.conversionDiagnostics) == 0 {
		return nil
	}
	return append([]types.ConversionDiagnostic(nil), info.conversionDiagnostics...)
}

func (info *RelayInfo) ConversionDiagnosticsTruncated() bool {
	return info != nil && info.conversionDiagnosticsTruncated
}
