// Shared query-param builder for the auto-router analysis stats endpoints.
//
// router_id must be the router's PK id, NOT its requestable model_id:
// usage_events.router_id is populated with router.ID at request time
// (sdk/api/handlers/handlers_auto_router.go), and the management stats
// queries match on that column. Sending model_id matches zero events, which
// silently blanks every analysis tab.
export function buildStatsParams(router, apiKeyId, range) {
  return {
    router_id: router?.id || undefined,
    api_key_id: apiKeyId || undefined,
    from: range.from,
    to: range.to,
  };
}
