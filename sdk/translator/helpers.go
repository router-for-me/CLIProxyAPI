package translator

import "context"

// TranslateRequestByFormatName converts a request payload between schemas by their string identifiers.
func TranslateRequestByFormatName(from, to Format, model string, rawJSON []byte, stream bool) []byte {
	return TranslateRequest(from, to, model, rawJSON, stream)
}

// HasResponseTransformerByFormatName reports whether a response translator exists between two schemas.
func HasResponseTransformerByFormatName(from, to Format) bool {
	return HasResponseTransformer(from, to)
}

// TranslateStreamByFormatName converts streaming responses between schemas by their string identifiers.
func TranslateStreamByFormatName(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	return TranslateStream(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

// TranslateNonStreamByFormatName converts non-streaming responses between schemas by their string identifiers.
func TranslateNonStreamByFormatName(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) string {
	return TranslateNonStream(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

// TranslateTokenCountByFormatName converts token counts between schemas by their string identifiers.
func TranslateTokenCountByFormatName(ctx context.Context, from, to Format, count int64, rawJSON []byte) string {
	return TranslateTokenCount(ctx, from, to, count, rawJSON)
}
