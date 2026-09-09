package auth

import (
	"net/http"
	"testing"
)

// imageOnlyModelRejectionBody mirrors the body returned by
// sdk/api/handlers.validateImageOnlyModel. A proxy chained in front of this one
// receives it verbatim, so it must not look like a credential-level model
// support failure.
const imageOnlyModelRejectionBody = `{"error":{"message":"model gpt-image-2 is only supported on /v1/images/generations and /v1/images/edits","type":"invalid_request_error","code":"unsupported_value","param":"model"}}`

// TestImageOnlyModelRejectionDoesNotPenalizeCredential pins the interaction
// between the image-only rejection body and the credential cooldown path.
//
// isModelSupportErrorMessage matches the literal "model_not_supported" anywhere
// in an upstream message, and isRequestInvalidError consults it before
// clienterror.IsRequestFault. So a rejection carrying that code would suspend the
// (credential, model) pair for 12 hours even though the request itself is at
// fault and no other credential could satisfy it.
func TestImageOnlyModelRejectionDoesNotPenalizeCredential(t *testing.T) {
	err := &Error{HTTPStatus: http.StatusBadRequest, Message: imageOnlyModelRejectionBody}

	if isModelSupportResultError(err) {
		t.Fatalf("image-only rejection must not be treated as a credential model-support failure: %s", err.Message)
	}
	if !shouldSkipCredentialCooldown(err) {
		t.Fatalf("image-only rejection must skip credential cooldown: %s", err.Message)
	}

	// Guard the root cause directly so a future edit to the emitted code is caught
	// here rather than silently suspending credentials in production.
	penalized := &Error{HTTPStatus: http.StatusBadRequest, Message: `{"error":{"code":"model_not_supported"}}`}
	if !isModelSupportResultError(penalized) {
		t.Fatal("expected model_not_supported to still drive the credential model-support path")
	}
	if shouldSkipCredentialCooldown(penalized) {
		t.Fatal("expected model_not_supported to still enter the cooldown path")
	}
}
