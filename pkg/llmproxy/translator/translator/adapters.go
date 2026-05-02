// Package translator provides response format adapters that wrap legacy translator functions
// with the correct type signatures for SDK integration.
package translator

import (
	"context"

	"github.com/kooshapari/CLIProxyAPI/v7/sdk/translator"
)

// ResponseStreamAdapter wraps a legacy translator function that returns []string
// and converts it to the SDK ResponseStreamTransform type ([][]byte).
type ResponseStreamAdapter func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte

// ResponseNonStreamAdapter wraps a legacy translator function that returns string
// and converts it to the SDK ResponseNonStreamTransform type ([]byte).
type ResponseNonStreamAdapter func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte

// ResponseTokenCountAdapter wraps a legacy token count translator that returns string
// and converts it to the SDK ResponseTokenCountTransform type ([]byte).
type ResponseTokenCountAdapter func(ctx context.Context, count int64) []byte

// ToResponseStreamTransform converts a legacy []string-returning function to the SDK type.
func ToResponseStreamTransform(fn func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string) translator.ResponseStreamTransform {
	return func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
		result := fn(ctx, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
		converted := make([][]byte, len(result))
		for i, s := range result {
			converted[i] = []byte(s)
		}
		return converted
	}
}

// ToResponseStreamTransformFromBytes converts a legacy [][]byte-returning function directly.
func ToResponseStreamTransformFromBytes(fn func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte) translator.ResponseStreamTransform {
	return fn
}

// ToResponseNonStreamTransform converts a legacy string-returning function to the SDK type.
func ToResponseNonStreamTransform(fn func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) string) translator.ResponseNonStreamTransform {
	return func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
		return []byte(fn(ctx, model, originalRequestRawJSON, requestRawJSON, rawJSON, param))
	}
}

// ToResponseNonStreamTransformFromBytes converts a legacy []byte-returning function to the SDK type.
func ToResponseNonStreamTransformFromBytes(fn func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte) translator.ResponseNonStreamTransform {
	return fn
}

// ToResponseTokenCountTransform converts a legacy string-returning function to the SDK type.
func ToResponseTokenCountTransform(fn func(ctx context.Context, count int64) string) translator.ResponseTokenCountTransform {
	return func(ctx context.Context, count int64) []byte {
		return []byte(fn(ctx, count))
	}
}

// ToResponseTokenCountTransformFromBytes converts a legacy []byte-returning function directly.
func ToResponseTokenCountTransformFromBytes(fn func(ctx context.Context, count int64) []byte) translator.ResponseTokenCountTransform {
	return fn
}
