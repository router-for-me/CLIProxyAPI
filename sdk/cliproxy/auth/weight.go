package auth

import (
	"fmt"
	"strconv"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialweight"
)

// ValidateAuthWeight validates every explicit credential weight source.
func ValidateAuthWeight(auth *Auth) error {
	if auth == nil {
		return nil
	}
	if rawWeight, ok := auth.Attributes[AttributeWeight]; ok {
		if _, errParse := credentialweight.ParseString(rawWeight); errParse != nil {
			return fmt.Errorf("invalid attributes weight: %w", errParse)
		}
	}
	if rawWeight, ok := auth.Metadata[AttributeWeight]; ok {
		if _, errParse := credentialweight.ParseValue(rawWeight); errParse != nil {
			return fmt.Errorf("invalid metadata weight: %w", errParse)
		}
	}
	if rawModelWeights, ok := auth.Attributes[AttributeModelWeights]; ok {
		if _, errParse := ParseModelWeights(rawModelWeights); errParse != nil {
			return fmt.Errorf("invalid attributes model_weights: %w", errParse)
		}
	}
	if rawModelWeights, ok := auth.Metadata[AttributeModelWeights]; ok && rawModelWeights != nil {
		if _, errParse := ParseModelWeights(rawModelWeights); errParse != nil {
			return fmt.Errorf("invalid metadata model_weights: %w", errParse)
		}
	}
	return nil
}

// ApplyAuthWeightMetadata validates the auth and applies the source metadata weight
// and optional per-model weight table (model_weights) as routing attributes.
func ApplyAuthWeightMetadata(auth *Auth, metadata map[string]any) error {
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return errWeight
	}
	if auth == nil || metadata == nil {
		return nil
	}
	if rawWeight, ok := metadata[AttributeWeight]; ok {
		weight, errParse := credentialweight.ParseValue(rawWeight)
		if errParse != nil {
			return fmt.Errorf("invalid metadata weight: %w", errParse)
		}
		if auth.Attributes == nil {
			auth.Attributes = make(map[string]string)
		}
		auth.Attributes[AttributeWeight] = strconv.FormatInt(weight, 10)
	}
	if rawModelWeights, ok := metadata[AttributeModelWeights]; ok && rawModelWeights != nil {
		modelWeights, errParse := ParseModelWeights(rawModelWeights)
		if errParse != nil {
			return fmt.Errorf("invalid metadata model_weights: %w", errParse)
		}
		if auth.Attributes == nil {
			auth.Attributes = make(map[string]string)
		}
		if encoded := EncodeModelWeights(modelWeights); encoded != "" {
			auth.Attributes[AttributeModelWeights] = encoded
		} else {
			delete(auth.Attributes, AttributeModelWeights)
		}
	}
	return nil
}
