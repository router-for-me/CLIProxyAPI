// Chat state machine. Reducer-only design so the same logic is testable
// without React and the component stays a thin wrapper around it.
//
// State shape:
//   { messages: [...], inFlight: id|null, error: object|null, lastUsage: id|null }
// Message shape:
//   { id, role, content, streaming?, usage?, outgoing?, incoming?, reqId? }
//
// Actions:
//   SEND   { id, userText, model, protocol, params, outgoing }
//   TOKEN  { id, token }
//   DONE   { id, usage?, incoming? }
//   ERROR  { id, error, incoming? }
//   RETRY  { id, assistantId, userText, model, protocol, params, outgoing }
//   CLEAR
//   CLEAR_ERROR

import { useReducer } from 'react';

export const initialState = {
  messages: [],
  inFlight: null,
  error: null,
  lastUsage: null,
};

let nextId = 1;
const genId = () => `msg-${nextId++}`;

// findPrecedingUser returns the index of the user message that produced the
// assistant message at `assistantIndex`, or -1 when there is none.
function findPrecedingUser(messages, assistantIndex) {
  for (let i = assistantIndex - 1; i >= 0; i--) {
    if (messages[i].role === 'user') return i;
  }
  return -1;
}

// historyPrefixForRetry resolves the user prompt behind an assistant message
// plus the message prefix that precedes it. Retrying re-runs that prompt with
// a clean history (everything after it is dropped) so the conversation does
// not fork or double-append. Returns null when the message/prefix is missing.
export function historyPrefixForRetry(messages, assistantMessageId) {
  if (!Array.isArray(messages)) return null;
  const idx = messages.findIndex((m) => m && m.id === assistantMessageId);
  if (idx < 0) return null;
  const userIdx = findPrecedingUser(messages, idx);
  if (userIdx < 0) return null;
  return { userText: messages[userIdx].content, prefix: messages.slice(0, userIdx) };
}

// mergeUsage folds a partial usage object into an accumulator. Providers
// stream usage across several chunks (Claude sends input tokens on
// message_start, output tokens on message_delta), so later fields win while
// earlier ones are preserved. Unknown/empty fields never overwrite a value.
export function mergeUsage(acc, next) {
  if (!next) return acc;
  const out = { ...(acc || {}) };
  for (const key of ['prompt_tokens', 'completion_tokens']) {
    const value = next[key];
    if (Number.isFinite(value)) out[key] = value;
  }
  if (Number.isFinite(next.total_tokens)) {
    out.total_tokens = next.total_tokens;
  } else if (Number.isFinite(out.prompt_tokens) && Number.isFinite(out.completion_tokens)) {
    // Prefer a derived total over a stale one accumulated earlier.
    out.total_tokens = out.prompt_tokens + out.completion_tokens;
  }
  return out;
}

export function reducer(state, action) {
  switch (action.type) {
    case 'SEND': {
      const userMsg = { id: genId(), role: 'user', content: action.userText, model: action.model, protocol: action.protocol, params: action.params };
      const asstMsg = { id: genId(), role: 'assistant', content: '', streaming: true, outgoing: action.outgoing, reqId: action.id };
      return {
        ...state,
        messages: [...state.messages, userMsg, asstMsg],
        inFlight: action.id,
        error: null,
      };
    }
    // RETRY regenerates one assistant message in place: the message it
    // belonged to is replaced, and everything after it is discarded so the
    // streamed tokens land in the right slot without forking the transcript.
    case 'RETRY': {
      const idx = state.messages.findIndex((m) => m && m.id === action.assistantId);
      if (idx < 0) return state;
      const userIdx = findPrecedingUser(state.messages, idx);
      if (userIdx < 0) return state;
      const prefix = state.messages.slice(0, userIdx);
      const userMsg = { ...state.messages[userIdx], id: genId() };
      const asstMsg = { id: genId(), role: 'assistant', content: '', streaming: true, outgoing: action.outgoing, reqId: action.id };
      return {
        ...state,
        messages: [...prefix, userMsg, asstMsg],
        inFlight: action.id,
        error: null,
      };
    }
    case 'TOKEN': {
      if (state.inFlight !== action.id) return state;
      const messages = state.messages.map((m, i) =>
        i === state.messages.length - 1 ? { ...m, content: m.content + action.token } : m,
      );
      return { ...state, messages };
    }
    case 'DONE': {
      if (state.inFlight !== action.id) return state;
      const messages = state.messages.map((m, i) =>
        i === state.messages.length - 1
          ? { ...m, streaming: false, usage: action.usage, incoming: action.incoming }
          : m,
      );
      return { ...state, messages, inFlight: null, lastUsage: action.id };
    }
    case 'ERROR': {
      if (state.inFlight !== action.id) return state;
      const messages = state.messages.map((m, i) =>
        i === state.messages.length - 1
          ? { ...m, streaming: false, incoming: action.incoming }
          : m,
      );
      return { ...state, messages, inFlight: null, error: action.error };
    }
    case 'CLEAR':
      return { ...initialState, messages: [] };
    case 'CLEAR_ERROR':
      return { ...state, error: null };
    default:
      return state;
  }
}

export function usePlaygroundChat() {
  const [state, dispatch] = useReducer(reducer, initialState);
  return { state, dispatch };
}

function headersToObject(headers) {
  if (!headers || typeof headers.entries !== 'function') return {};
  const out = {};
  try {
    for (const [k, v] of headers.entries()) out[k] = v;
  } catch {
    /* malformed Headers — degrade to an empty map */
  }
  return out;
}

// openStream encapsulates the fetch + SSE pipeline so the chat pipeline is
// easy to reason about. Calls onChunk for each \n\n-delimited event block
// (flushing any trailing partial block first), then onDone({ headers }) once
// the body is fully consumed, or onError on failure. Response headers are
// surfaced so the inspector can show the real wire metadata.
export async function openStream({ url, init, onChunk, onDone, onError, signal }) {
  let res;
  try {
    res = await fetch(url, { ...init, signal });
  } catch (e) {
    onError({ message: e.message || 'Network error', type: 'network' });
    return;
  }
  if (!res.ok) {
    let text = '';
    try { text = await res.text(); } catch { /* ignore */ }
    onError({ message: `HTTP ${res.status}`, type: 'upstream', status: res.status, body: text });
    return;
  }
  const headers = headersToObject(res.headers);
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let idx;
    while ((idx = buffer.indexOf('\n\n')) >= 0) {
      const event = buffer.slice(0, idx);
      buffer = buffer.slice(idx + 2);
      onChunk(event);
    }
  }
  buffer += decoder.decode();
  if (buffer.trim()) onChunk(buffer);
  onDone({ headers });
}
