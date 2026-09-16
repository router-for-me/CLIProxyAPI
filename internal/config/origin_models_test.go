package config

import "testing"

func TestParseConfigOriginModelsEndpointDefaultsAndOptIn(t *testing.T) {
	defaultCfg, err := ParseConfigBytes([]byte("api-keys: [native]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if defaultCfg.AllowOriginModelsEndpoint {
		t.Fatal("allow-origin-models-endpoint defaulted to true")
	}

	optInCfg, err := ParseConfigBytes([]byte("allow-origin-models-endpoint: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !optInCfg.AllowOriginModelsEndpoint {
		t.Fatal("allow-origin-models-endpoint: true was not parsed")
	}
}
