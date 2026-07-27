package home

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCredentialInFlightWireContractFixture(t *testing.T) {
	raw, errRead := os.ReadFile(filepath.Join("testdata", "credential_in_flight_contract.json"))
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	fixture, errDecode := decodeInFlightContractFixture(raw)
	if errDecode != nil {
		t.Fatalf("decodeInFlightContractFixture() error = %v", errDecode)
	}
	if fixture.Part.Kind != InFlightFramePart || fixture.Part.PartIndex == nil || *fixture.Part.PartIndex != 0 || fixture.Part.PartCount == nil || *fixture.Part.PartCount != 1 {
		t.Fatalf("part = %#v", fixture.Part)
	}
	if fixture.Part.Aggregates[0].Status != InFlightAccounted || fixture.Part.Aggregates[1].Status != InFlightUnaccounted {
		t.Fatalf("statuses = %#v", fixture.Part.Aggregates)
	}
	if fixture.Overflow.Kind != InFlightFrameOverflow || fixture.Overflow.AggregateGroupCount != 100001 {
		t.Fatalf("overflow = %#v", fixture.Overflow)
	}
	assertInFlightContractFields(t)
	assertRequiredInFlightJSONKeys(t, raw, []string{"config", "part", "overflow"})
	assertInFlightFixtureKeys(t, fixture)
}

func TestCredentialInFlightWireContractRejectsInvalidJSON(t *testing.T) {
	raw, errRead := os.ReadFile(filepath.Join("testdata", "credential_in_flight_contract.json"))
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{name: "unknown frame owner field", raw: bytes.Replace(raw, []byte(`"kind": "part"`), []byte(`"kind": "part", "node_id": "node-a"`), 1)},
		{name: "unknown aggregate owner field", raw: bytes.Replace(raw, []byte(`"credential_id": "cred-a"`), []byte(`"credential_id": "cred-a", "fingerprint": "owner"`), 1)},
		{name: "unknown detail secret field", raw: bytes.Replace(raw, []byte(`"request_id": "req-1"`), []byte(`"request_id": "req-1", "secret": "secret"`), 1)},
		{name: "unknown overflow secret field", raw: bytes.Replace(raw, []byte(`"aggregate_group_count": 100001`), []byte(`"aggregate_group_count": 100001, "api_key": "secret"`), 1)},
		{name: "trailing JSON", raw: append(append([]byte{}, raw...), []byte(` {"part": {}}`)...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, errDecode := decodeInFlightContractFixture(test.raw); errDecode == nil {
				t.Fatal("decodeInFlightContractFixture() error = nil")
			}
		})
	}
}

type inFlightContractFixture struct {
	Part         InFlightSnapshotFrame
	Overflow     InFlightSnapshotFrame
	PartJSON     json.RawMessage
	OverflowJSON json.RawMessage
}

func decodeInFlightContractFixture(raw []byte) (inFlightContractFixture, error) {
	var fixture inFlightContractFixture
	var document struct {
		Config   json.RawMessage       `json:"config"`
		Part     InFlightSnapshotFrame `json:"part"`
		Overflow InFlightSnapshotFrame `json:"overflow"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&document); errDecode != nil {
		return fixture, errDecode
	}
	if errDecode := decoder.Decode(&struct{}{}); errDecode == nil {
		return fixture, errors.New("unexpected trailing JSON")
	} else if errDecode != io.EOF {
		return fixture, errDecode
	}
	documentRaw := struct {
		Part     json.RawMessage `json:"part"`
		Overflow json.RawMessage `json:"overflow"`
	}{}
	if errDecode := json.Unmarshal(raw, &documentRaw); errDecode != nil {
		return fixture, errDecode
	}
	fixture.Part = document.Part
	fixture.Overflow = document.Overflow
	fixture.PartJSON = documentRaw.Part
	fixture.OverflowJSON = documentRaw.Overflow
	return fixture, nil
}

func assertInFlightContractFields(t *testing.T) {
	t.Helper()
	assertOrderedInFlightJSONFields(t, reflect.TypeOf(InFlightSnapshotFrame{}), []inFlightJSONField{
		{name: "Kind", tag: "kind"},
		{name: "Revision", tag: "revision"},
		{name: "ObservedAt", tag: "observed_at"},
		{name: "BarrierRevision", tag: "barrier_revision"},
		{name: "PartIndex", tag: "part_index,omitempty"},
		{name: "PartCount", tag: "part_count,omitempty"},
		{name: "DetailsTruncated", tag: "details_truncated,omitempty"},
		{name: "Aggregates", tag: "aggregates,omitempty"},
		{name: "Details", tag: "details,omitempty"},
		{name: "AggregateGroupCount", tag: "aggregate_group_count,omitempty"},
	})
	assertOrderedInFlightJSONFields(t, reflect.TypeOf(InFlightAggregate{}), []inFlightJSONField{
		{name: "CredentialID", tag: "credential_id"},
		{name: "Model", tag: "model"},
		{name: "Status", tag: "status"},
		{name: "Count", tag: "count"},
	})
	assertOrderedInFlightJSONFields(t, reflect.TypeOf(InFlightRequestDetail{}), []inFlightJSONField{
		{name: "RequestID", tag: "request_id"},
		{name: "CredentialID", tag: "credential_id"},
		{name: "Model", tag: "model"},
		{name: "RequestKind", tag: "request_kind"},
		{name: "StartedAt", tag: "started_at"},
	})
}

func assertInFlightFixtureKeys(t *testing.T, fixture inFlightContractFixture) {
	t.Helper()
	assertRequiredInFlightJSONKeys(t, fixture.PartJSON, []string{"kind", "revision", "observed_at", "barrier_revision", "part_index", "part_count", "details_truncated", "aggregates", "details"})
	assertRequiredInFlightJSONKeys(t, fixture.OverflowJSON, []string{"kind", "revision", "observed_at", "barrier_revision", "aggregate_group_count"})

	var part struct {
		Aggregates []json.RawMessage `json:"aggregates"`
		Details    []json.RawMessage `json:"details"`
	}
	if errDecode := json.Unmarshal(fixture.PartJSON, &part); errDecode != nil {
		t.Fatalf("json.Unmarshal() error = %v", errDecode)
	}
	for index, aggregate := range part.Aggregates {
		assertRequiredInFlightJSONKeys(t, aggregate, []string{"credential_id", "model", "status", "count"})
		if len(aggregate) == 0 {
			t.Fatalf("aggregate %d is empty", index)
		}
	}
	for index, detail := range part.Details {
		assertRequiredInFlightJSONKeys(t, detail, []string{"request_id", "credential_id", "model", "request_kind", "started_at"})
		if len(detail) == 0 {
			t.Fatalf("detail %d is empty", index)
		}
	}
}

type inFlightJSONField struct {
	name string
	tag  string
}

func assertOrderedInFlightJSONFields(t *testing.T, structType reflect.Type, want []inFlightJSONField) {
	t.Helper()
	if structType.NumField() != len(want) {
		t.Fatalf("%s field count = %d, want %d", structType.Name(), structType.NumField(), len(want))
	}
	for index, expected := range want {
		field := structType.Field(index)
		if field.Name != expected.name || field.Tag.Get("json") != expected.tag {
			t.Fatalf("%s field %d = (%q, %q), want (%q, %q)", structType.Name(), index, field.Name, field.Tag.Get("json"), expected.name, expected.tag)
		}
	}
}

func assertRequiredInFlightJSONKeys(t *testing.T, raw json.RawMessage, required []string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if errDecode := json.Unmarshal(raw, &fields); errDecode != nil {
		t.Fatalf("json.Unmarshal() error = %v", errDecode)
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			t.Fatalf("required JSON key %q is missing", key)
		}
	}
}
