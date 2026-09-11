package config

import "testing"

func TestRoutingCooldownWait(t *testing.T) {
	const yamlConfig = `routing:
  cooldown-wait:
    max-wait-ms: 5000
    max-attempts: 2
    reclassify-403: true
`

	cfg, errParse := ParseConfigBytes([]byte(yamlConfig))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	got := cfg.Routing.CooldownWait
	if got.MaxWaitMS != 5000 {
		t.Errorf("MaxWaitMS = %d, want 5000", got.MaxWaitMS)
	}
	if got.MaxAttempts != 2 {
		t.Errorf("MaxAttempts = %d, want 2", got.MaxAttempts)
	}
	if !got.Reclassify403 {
		t.Errorf("Reclassify403 = false, want true")
	}

	// Zero-value defaults round-trip.
	const emptyConfig = `routing:
  strategy: "round-robin"
`
	emptyCfg, errParse := ParseConfigBytes([]byte(emptyConfig))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if want := (CooldownWaitConfig{}); emptyCfg.Routing.CooldownWait != want {
		t.Errorf("CooldownWait = %+v, want zero value %+v", emptyCfg.Routing.CooldownWait, want)
	}
}
