package auth

import (
	"reflect"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

func TestQuotaRecoveryBindingTimeZones(t *testing.T) {
	now := time.Date(2037, time.March, 4, 5, 6, 7, 123456789, time.UTC)
	for _, tc := range []struct {
		name string
		zone *time.Location
	}{
		{"utc", time.UTC},
		{"local_utc", time.FixedZone("Local", 0)},
		{"offset", time.FixedZone("Nepal", 5*3600+45*60)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deadline := now.Add(time.Hour)
			input := []quotaRecoveryBinding{{Selection: "selection-sentinel", Execution: "actual-sentinel", Requested: "original-sentinel", Until: deadline.In(tc.zone)}}
			want := []quotaRecoveryBinding{{Selection: "selection-sentinel", Execution: "actual-sentinel", Requested: "original-sentinel", Until: deadline}}
			got := mergeQuotaRecoveryBindings(now, input)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("binding deadline is not canonical UTC: got=%#v want=%#v", got, want)
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var decoded []quotaRecoveryBinding
			if err = json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			restored := mergeQuotaRecoveryBindings(now, decoded)
			if !reflect.DeepEqual(restored, want) {
				t.Fatal("JSON restore changed exact binding tuple or deadline")
			}
			before := &Auth{Quota: QuotaState{recoveryBindings: got}}
			after := &Auth{Quota: QuotaState{recoveryBindings: restored}}
			if !authQuotaRecoveryBindingsEqual(before, after, now) {
				t.Fatal("equivalent stored binding rejected")
			}
			after.Quota.recoveryBindings[0].Until = deadline.Add(time.Nanosecond)
			if authQuotaRecoveryBindingsEqual(before, after, now) {
				t.Fatal("changed deadline accepted")
			}
		})
	}
}
