// Owns the inspector drawer's payload cache, keyed by reqId.
//
// The pure half (createInspectorStore) is exported so it can be tested
// without React. The hook just wires it to useState so the component
// re-renders when the cache mutates.
//
// Storage is session-local: it grows with the conversation, but the
// conversation is in-memory anyway, so this matches the spec's YAGNI
// decision not to persist.

import { useEffect, useState, useCallback } from 'react';

export function createInspectorStore() {
  let state = { outgoing: {}, incoming: {}, headers: {}, messageToReq: {}, latestReqId: null };
  const listeners = new Set();
  const notify = () => listeners.forEach((l) => l(state));
  const set = (next) => { state = next; notify(); };

  return {
    recordOutgoing(reqId, body) {
      set({
        ...state,
        outgoing: { ...state.outgoing, [reqId]: body },
        latestReqId: reqId,
      });
    },
    recordIncoming(reqId, body, headers) {
      set({
        ...state,
        incoming: { ...state.incoming, [reqId]: body },
        headers: { ...state.headers, [reqId]: headers || {} },
      });
    },
    bindMessage(messageId, reqId) {
      set({ ...state, messageToReq: { ...state.messageToReq, [messageId]: reqId } });
    },
    findReqIdForMessage(messageId, messages) {
      // Prefer explicit binding set by the component after dispatch.
      const bound = state.messageToReq[messageId];
      if (bound) return bound;
      // Fallback: if the message exists in the conversation and we have a
      // most-recent outgoing reqId, attribute the in-flight response to it.
      if (state.latestReqId && Array.isArray(messages) && messages.some((m) => m && m.id === messageId)) {
        return state.latestReqId;
      }
      return null;
    },
    viewFor(reqId) {
      return {
        outgoing: state.outgoing[reqId] ?? null,
        incoming: state.incoming[reqId] ?? null,
        headers: state.headers[reqId] ?? null,
      };
    },
    snapshot() { return state; },
    clear() { set({ outgoing: {}, incoming: {}, headers: {}, messageToReq: {}, latestReqId: null }); },
    subscribe(fn) { listeners.add(fn); return () => listeners.delete(fn); },
  };
}

export function usePlaygroundInspector() {
  const [store] = useState(() => createInspectorStore());
  const [, force] = useState(0);
  const rerender = useCallback(() => force((n) => n + 1), []);

  // Force a re-render on every store mutation. Subscription is local;
  // we don't expose the listener API externally.
  useEffect(() => store.subscribe(rerender), [store, rerender]);

  return store;
}
