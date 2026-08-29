// Chat state machine. Reducer-only design so the same logic is testable
// without React and the component stays a thin wrapper around it.
//
// State shape:
//   { messages: [...], inFlight: id|null, error: object|null, lastUsage: id|null }
// Message shape:
//   { id, role, content, streaming?, usage? }
//
// Actions:
//   SEND { id, userText, model, protocol, params }
//   TOKEN { id, token }
//   DONE { id, usage? }
//   ERROR { id, error }
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

export function reducer(state, action) {
  switch (action.type) {
    case 'SEND': {
      const userMsg = { id: genId(), role: 'user', content: action.userText, model: action.model, protocol: action.protocol, params: action.params };
      const asstMsg = { id: genId(), role: 'assistant', content: '', streaming: true };
      return {
        ...state,
        messages: [...state.messages, userMsg, asstMsg],
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
          ? { ...m, streaming: false, usage: action.usage }
          : m,
      );
      return { ...state, messages, inFlight: null, lastUsage: action.id };
    }
    case 'ERROR': {
      if (state.inFlight !== action.id) return state;
      const messages = state.messages.map((m, i) =>
        i === state.messages.length - 1 ? { ...m, streaming: false } : m,
      );
      return { ...state, messages, inFlight: null, error: action.error };
    }
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

// openStream encapsulates the fetch + SSE pipeline so the chat pipeline is
// easy to reason about. Calls onChunk for each \n\n-delimited event block,
// onDone once the body is fully consumed, or onError on failure.
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
  onDone();
}
