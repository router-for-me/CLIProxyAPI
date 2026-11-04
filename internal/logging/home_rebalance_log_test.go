package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestHomeRebalanceLogPreservesPlanAndAddressesLocallyAndWhenForwarded(t *testing.T) {
	var local bytes.Buffer
	logger := log.New()
	logger.SetOutput(&local)
	logger.SetFormatter(&LogFormatter{})
	stub := &stubHomeAppLogClient{heartbeatOK: true}
	forwarder := &HomeAppLogForwarder{
		formatter: &LogFormatter{},
		queue:     make(chan homeAppLogPayload, 1),
	}
	forwarder.enabled.Store(true)
	forwarder.bind(stub)
	logger.AddHook(forwarder)

	logger.Infof("executing Home rebalance plan: plan=%q from=%q to=%q", "plan-1", "home-1:8327", "home-2:8327")
	select {
	case payload := <-forwarder.queue:
		forwarder.forward(payload)
	default:
		t.Fatal("rebalance log was not queued for Home")
	}
	if stub.pushedCount() != 1 {
		t.Fatal("rebalance log was not forwarded to Home")
	}
	var forwarded homeAppLogPayload
	if errJSON := json.Unmarshal(stub.pushedAt(0), &forwarded); errJSON != nil {
		t.Fatal(errJSON)
	}
	for destination, line := range map[string]string{"local": local.String(), "Home": forwarded.Line} {
		for key, value := range map[string]string{"plan": "plan-1", "from": "home-1:8327", "to": "home-2:8327"} {
			if !strings.Contains(line, fmt.Sprintf("%s=%q", key, value)) {
				t.Fatalf("%s log missing %s: %q", destination, key, line)
			}
		}
	}
}
