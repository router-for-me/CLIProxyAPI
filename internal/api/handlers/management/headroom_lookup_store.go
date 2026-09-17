package management

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// storeUsageWindowsReader wraps *store.PostgresStore so it satisfies the
// coreauth.UsageWindowsReader interface declared in
// sdk/cliproxy/auth/headroom_lookup_usage_windows.go. The adapter exists
// here (not in the store package) because the SDK must stay free of PG
// dependencies; the management handler is the bridge.
type storeUsageWindowsReader struct {
	store   *store.PostgresStore
	adapter *storeQuotaRepoAdapter
}

// newUsageWindowsHeadroomLookupFromStore builds the production
// coreauth.HeadroomLookup wired to the supplied *store.PostgresStore.
// The reader uses the same per-auth aggregation as the
// /v0/management/auths/:id/quota endpoint, so a flapping PG manifests
// identically in both surfaces (operators do not have to learn two
// failure modes).
//
// The TTL defaults to the package-level constant
// (defaultHeadroomTTL = 5s). Tests can substitute their own TTL by
// constructing UsageWindowsHeadroomLookup directly with a fake reader.
func newUsageWindowsHeadroomLookupFromStore(pg *store.PostgresStore) coreauth.HeadroomLookup {
	if pg == nil {
		return coreauth.StaticHeadroomLookup{}
	}
	reader := &storeUsageWindowsReader{
		store:   pg,
		adapter: NewStoreQuotaRepoAdapter(pg).(*storeQuotaRepoAdapter),
	}
	return coreauth.NewUsageWindowsHeadroomLookup(reader, 0)
}

// GetAuthQuota satisfies coreauth.UsageWindowsReader by translating the
// store-layer QuotaShare* types into the auth-package AuthQuotaSnapshot
// DTO. Errors and the partial flag propagate unchanged so the headroom
// lookup's "errors collapse to unlimited" rule applies uniformly.
func (r *storeUsageWindowsReader) GetAuthQuota(ctx context.Context, authID string) (coreauth.AuthQuotaSnapshot, bool, error) {
	snap, partial, err := r.adapter.GetAuthQuota(ctx, authID)
	if err != nil {
		return coreauth.AuthQuotaSnapshot{}, partial, err
	}
	out := coreauth.AuthQuotaSnapshot{
		AuthID:       snap.AuthID,
		Channel:      snap.Channel,
		PoolStrategy: snap.PoolStrategy,
	}
	for _, w := range snap.Windows {
		out.Windows = append(out.Windows, coreauth.WindowQuotaRead{
			Size:        w.Size,
			Used:        w.Used,
			Limit:       w.Limit,
			HeadroomPct: w.HeadroomPct,
			OverLimit:   w.OverLimit,
		})
	}
	return out, partial, nil
}
