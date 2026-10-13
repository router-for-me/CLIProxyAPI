package registry

import (
	"context"
	"reflect"
	"testing"
)

func TestDevinCatalogRefreshNotifiesOnlyOnChange(t *testing.T) {
	original := GetDevinModelsJSON()
	refreshCallbackMu.Lock()
	previousCallback, previousPending := refreshCallback, pendingRefreshChanges
	pendingRefreshChanges = nil
	var notifications [][]string
	refreshCallback = func(providers []string) { notifications = append(notifications, providers) }
	refreshCallbackMu.Unlock()
	defer func() {
		refreshCallbackMu.Lock()
		refreshCallback, pendingRefreshChanges = previousCallback, previousPending
		refreshCallbackMu.Unlock()
		if _, errLoad := loadDevinModelsFromBytes(original, "restore"); errLoad != nil {
			t.Error(errLoad)
		}
	}()

	data := []byte(`{"devin":[{"id":"devin/catalog-notification-regression","display_name":"Catalog notification regression"}]}`)
	u := &catalogUpdater{
		fetch:   func(context.Context, string) ([]byte, error) { return data, nil },
		publish: devinCatalogUpdater.publish,
	}
	u.refresh(context.Background(), "test", 0)
	if !reflect.DeepEqual(notifications, [][]string{{"devin"}}) {
		t.Fatalf("notifications = %v", notifications)
	}
	u.refresh(context.Background(), "test", 0)
	data = []byte(`invalid`)
	u.refresh(context.Background(), "test", 0)
	if len(notifications) != 1 {
		t.Fatalf("unchanged/invalid catalog triggered notifications: %v", notifications)
	}
}
