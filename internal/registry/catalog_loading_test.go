package registry

import (
	"sync"
	"testing"
)

func TestCatalogLoadingOwnerRelease(t *testing.T) {
	SetCatalogLoading(false)
	first := BeginCatalogLoading()
	defer first()
	second := BeginCatalogLoading()
	defer second()
	first()
	first()
	if !CatalogLoading() {
		t.Fatal("repeated first-owner release cleared the second owner")
	}
	second()
	if CatalogLoading() {
		t.Fatal("loading remained after all owners released")
	}
}

func TestCatalogLoadingOverrideAndOwnerAreIndependent(t *testing.T) {
	SetCatalogLoading(true)
	defer SetCatalogLoading(false)
	release := BeginCatalogLoading()
	defer release()
	SetCatalogLoading(false)
	if !CatalogLoading() {
		t.Fatal("override cleared an active owner")
	}
	SetCatalogLoading(true)
	release()
	if !CatalogLoading() {
		t.Fatal("owner release cleared the explicit override")
	}
	SetCatalogLoading(false)
	if CatalogLoading() {
		t.Fatal("loading remained after both override and owner finished")
	}
}

func TestCatalogLoadingConcurrentReleaseIsIdempotent(t *testing.T) {
	SetCatalogLoading(false)
	first := BeginCatalogLoading()
	defer first()
	second := BeginCatalogLoading()
	defer second()
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first()
		}()
	}
	wg.Wait()
	if !CatalogLoading() {
		t.Fatal("concurrent repeated release cleared another owner")
	}
	second()
	if CatalogLoading() {
		t.Fatal("loading remained after all owners released")
	}
}
