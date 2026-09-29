package convdiag

import (
	"context"
	"reflect"

	"github.com/QuantumNous/new-api/relaykit/types"
)

type collectorKey struct{}

type Collector struct {
	diagnostics []types.ConversionDiagnostic
}

func WithCollector(ctx context.Context) (context.Context, *Collector) {
	if isNilContext(ctx) {
		ctx = context.Background()
	}
	if collector, _ := ctx.Value(collectorKey{}).(*Collector); collector != nil {
		return ctx, collector
	}
	collector := &Collector{}
	return context.WithValue(ctx, collectorKey{}, collector), collector
}

func Add(ctx context.Context, diagnostics ...types.ConversionDiagnostic) {
	if isNilContext(ctx) || len(diagnostics) == 0 {
		return
	}
	collector, _ := ctx.Value(collectorKey{}).(*Collector)
	if collector == nil {
		return
	}
	collector.diagnostics = append(collector.diagnostics, diagnostics...)
}

func isNilContext(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	value := reflect.ValueOf(ctx)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (c *Collector) Diagnostics() []types.ConversionDiagnostic {
	if c == nil || len(c.diagnostics) == 0 {
		return nil
	}
	return append([]types.ConversionDiagnostic(nil), c.diagnostics...)
}
