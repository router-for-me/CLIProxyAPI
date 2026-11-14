package registry

import (
	"sort"
	"time"
)

// RegisteredRouteSnapshot is an internal management-plane fact. ClientID is
// never serialized; the inventory boundary hashes it before publication.
type RegisteredRouteSnapshot struct {
	ClientID         string
	RouteChannel     string
	RuntimeModelID   string
	Model            *ModelInfo
	QuotaBlocked     bool
	QuotaResetsAt    *time.Time
	SuspensionReason string
	LastUpdated      time.Time
}

// RegisteredRouteSnapshots returns every credential/model registration for the
// management inventory builder. Callers must hash ClientID before serialization.
func (r *ModelRegistry) RegisteredRouteSnapshots() []RegisteredRouteSnapshot {
	return r.RegisteredRouteSnapshotsAt(r.now())
}

// RegisteredRouteSnapshotsAt evaluates cooldowns at the same instant as the caller.
func (r *ModelRegistry) RegisteredRouteSnapshotsAt(now time.Time) []RegisteredRouteSnapshot {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	result := make([]RegisteredRouteSnapshot, 0)
	for clientID, modelIDs := range r.clientModels {
		provider := r.clientProviders[clientID]
		for _, modelID := range modelIDs {
			registration := r.models[modelID]
			if registration == nil {
				continue
			}
			info := registration.Info
			if byClient := r.clientModelInfos[clientID]; byClient != nil && byClient[modelID] != nil {
				info = byClient[modelID]
			} else if registration.InfoByProvider[provider] != nil {
				info = registration.InfoByProvider[provider]
			}
			if info == nil {
				continue
			}
			reason, _, _ := clientSuspensionActive(registration, clientID, now)
			snapshot := RegisteredRouteSnapshot{
				ClientID: clientID, RouteChannel: provider, RuntimeModelID: modelID,
				Model: cloneModelInfo(info), SuspensionReason: reason,
				LastUpdated: registration.LastUpdated.UTC(),
			}
			if markedAt := registration.QuotaExceededClients[clientID]; markedAt != nil {
				resetsAt := markedAt.Add(modelQuotaExceededWindow).UTC()
				if now.Before(resetsAt) {
					snapshot.QuotaBlocked = true
					snapshot.QuotaResetsAt = &resetsAt
				}
			}
			result = append(result, snapshot)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].RuntimeModelID != result[j].RuntimeModelID {
			return result[i].RuntimeModelID < result[j].RuntimeModelID
		}
		if result[i].RouteChannel != result[j].RouteChannel {
			return result[i].RouteChannel < result[j].RouteChannel
		}
		return result[i].ClientID < result[j].ClientID
	})
	return result
}
