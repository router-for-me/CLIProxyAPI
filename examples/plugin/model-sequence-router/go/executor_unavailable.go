package main

const (
	// unavailableStatus is the retryable status a client receives when the
	// provider of its next sequence position is unavailable.
	unavailableStatus = 529
	// unavailableCode names the unavailable-provider condition in the error envelope.
	unavailableCode = "sequence_position_unavailable"
	// unavailableMessage explains the condition to the client.
	unavailableMessage = "model-sequence-router: the next sequence position's provider is unavailable; retry to reuse the same position"
)

// executorIdentifier names the executor provider in the identifier reply.
type executorIdentifier struct {
	Identifier string `json:"identifier"`
}

// unavailableEnvelope renders the retryable error every work entry point
// returns. The router self-targets only to signal that the next sequence position
// is unavailable, so the executor holds no state and reads no cursor.
func unavailableEnvelope() []byte {
	return errorEnvelope(envelopeError{
		Code:       unavailableCode,
		Message:    unavailableMessage,
		HTTPStatus: unavailableStatus,
	})
}

// executorIdentifierResult names this plugin as the executor provider. The host
// requires an identifier before it accepts a self-targeted route, so this method
// answers normally while every execution entry point reports unavailability.
func executorIdentifierResult() ([]byte, error) {
	return okEnvelope(executorIdentifier{Identifier: pluginIdentifier})
}
