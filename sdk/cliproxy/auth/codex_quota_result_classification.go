package auth

const positiveQuotaRejectionMetadataKey = "positive_quota_rejection"

// Keep subscription recovery recognition separate from the broader continuity
// quota classification. The annotation preserves only a classification lost
// while converting an upstream error; subscription reset does not consult it.
func isCodexQuotaRejectionResult(err *Error) bool {
	return err != nil && (quotaCodexSubscriptionError(err) || isPositiveQuotaRejectionError(err) || err.Metadata[positiveQuotaRejectionMetadataKey] == "true")
}
