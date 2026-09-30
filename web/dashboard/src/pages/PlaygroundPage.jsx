import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getProtocol } from '../playground/protocols.js';
import { usePlaygroundKey } from '../playground/usePlaygroundKey.js';
import { usePlaygroundChat, openStream, mergeUsage, historyPrefixForRetry } from '../playground/usePlaygroundChat.js';
import ModelPicker from '../playground/ModelPicker.jsx';
import MessageBubble from '../playground/MessageBubble.jsx';
import UsageFooter from '../playground/UsageFooter.jsx';
import InspectorDrawer from '../playground/InspectorDrawer.jsx';
import CooldownBanner from '../playground/CooldownBanner.jsx';
import { usePlaygroundInspector } from '../playground/usePlaygroundInspector.js';
import { useCooldownWatch } from '../playground/useCooldownWatch.js';
import { detectProtocol } from '../playground/detectProtocol.js';
import { toJsonExport, toMarkdownExport } from '../playground/exportConversation.js';
import { getModelPricing } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState } from '../components/Primitives.jsx';

function authHeaderFor(protocolId, key) {
  switch (protocolId) {
    case 'gemini':
      return { 'x-goog-api-key': key };
    case 'claude':
      return { 'x-api-key': key, 'anthropic-version': '2023-06-01' };
    case 'codex':
      return { 'Authorization': `Bearer ${key}` };
    default:
      return { Authorization: `Bearer ${key}` };
  }
}

// Map a playground protocol id (used by the request layer) to the
// provider key the cooldown snapshot expects ("openai" vs. "openai-compat").
function providerKeyForProtocol(protocolId) {
  switch (protocolId) {
    case 'openai-compat':
      return 'openai';
    case 'gemini':
    case 'claude':
    case 'codex':
      return protocolId;
    default:
      return protocolId;
  }
}

function ProtocolSwitcher({ value, onChange }) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="playground-select"
      aria-label="Protocol"
    >
      <option value="openai-compat">OpenAI-compat</option>
      <option value="gemini">Gemini</option>
      <option value="claude">Claude</option>
      <option value="codex">Codex / Responses</option>
    </select>
  );
}

function ParamPanel({ params, onChange }) {
  const update = (k, v) => onChange({ ...params, [k]: v });
  return (
    <div className="playground-params">
      <label className="playground-field">
        <span className="playground-field__label">System prompt</span>
        <textarea
          className="playground-field__input"
          rows={3}
          value={params.system}
          onChange={(e) => update('system', e.target.value)}
        />
      </label>
      <label className="playground-field">
        <span className="playground-field__label">Temperature ({params.temperature})</span>
        <input
          type="range"
          min="0"
          max="2"
          step="0.1"
          value={params.temperature}
          onChange={(e) => update('temperature', Number(e.target.value))}
          className="playground-field__range"
        />
      </label>
      <label className="playground-field">
        <span className="playground-field__label">Max tokens</span>
        <input
          type="number"
          min="1"
          max="32768"
          value={params.max_tokens}
          onChange={(e) => update('max_tokens', Number(e.target.value))}
          className="playground-field__input"
        />
      </label>
      <label className="playground-field">
        <span className="playground-field__label">Top-p</span>
        <input
          type="number"
          min="0"
          max="1"
          step="0.05"
          value={params.top_p}
          onChange={(e) => update('top_p', Number(e.target.value))}
          className="playground-field__input"
        />
      </label>
      <label className="playground-check">
        <input type="checkbox" checked={params.stream} onChange={(e) => update('stream', e.target.checked)} />
        <span>Stream</span>
      </label>
    </div>
  );
}

function ExportButton({ messages }) {
  const disabled = messages.length === 0;
  const dl = (name, content, mime) => {
    const blob = new Blob([content], { type: mime });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    a.click();
    URL.revokeObjectURL(url);
  };
  return (
    <div className="playground-export">
      <button type="button" disabled={disabled} onClick={() => dl('conversation.json', toJsonExport(messages), 'application/json')}>
        JSON
      </button>
      <button type="button" disabled={disabled} onClick={() => dl('conversation.md', toMarkdownExport(messages), 'text/markdown')}>
        Markdown
      </button>
    </div>
  );
}

export default function PlaygroundPage() {
  const { key, error: keyError, loading: keyLoading, refresh: refreshKey } = usePlaygroundKey();
  const [model, setModel] = useState('');
  const [protocolId, setProtocolId] = useState('openai-compat');
  const [params, setParams] = useState({ system: '', temperature: 0.7, max_tokens: 1024, top_p: 1, stream: true });
  const { state, dispatch } = usePlaygroundChat();
  const abortRef = useRef(null);
  const [draft, setDraft] = useState('');
  const [railOpen, setRailOpen] = useState(false);
  const userPickedProtocol = useRef(false);

  // Inspector state — drawer visibility, current request, tab + pretty toggle.
  const inspector = usePlaygroundInspector();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [activeTab, setActiveTab] = useState('outgoing');
  const [pretty, setPretty] = useState(true);
  const [currentReqId, setCurrentReqId] = useState(null);

  // Cooldown snapshot for the selected protocol + model (live endpoint).
  const cooldown = useCooldownWatch(providerKeyForProtocol(protocolId), model);

  // Pricing for the selected model (503/no-PG degrades to null).
  const pricing = useAsync(
    () => (model ? getModelPricing(model).catch(() => null) : Promise.resolve(null)),
    [model],
  );

  // Auto-detect protocol when the model changes, unless the user picked one.
  useEffect(() => {
    if (!model || userPickedProtocol.current) return;
    setProtocolId(detectProtocol({ model }));
  }, [model]);

  // Auto-scroll: keep the transcript pinned to the newest token unless the
  // user has scrolled up, in which case we offer a "jump to latest" pill.
  const scrollRef = useRef(null);
  const [atBottom, setAtBottom] = useState(true);
  useEffect(() => {
    if (!atBottom) return;
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [state.messages, atBottom]);

  const onMessagesScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 48);
  }, []);

  const jumpToLatest = useCallback(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
    setAtBottom(true);
  }, []);

  // Shared executor: dispatches SEND/RETRY, opens the stream, and records
  // outgoing/incoming snapshots (with real response headers) for the
  // inspector. `opts.assistantId` regenerates that message in place;
  // `opts.prefix` supplies the truncated history for a retry.
  const executeSend = useCallback(async (userText, opts = {}) => {
    if (!key || !model || state.inFlight) return;
    const adapter = getProtocol(protocolId);
    const prefix = opts.prefix ?? state.messages;
    const history = [];
    if (params.system) history.push({ role: 'system', content: params.system });
    for (const m of prefix) history.push({ role: m.role, content: m.content });
    history.push({ role: 'user', content: userText });
    const reqId = `r-${Date.now()}`;
    const body = adapter.buildRequest(model, history, { ...params });
    const url = adapter.endpoint(model, params);
    if (opts.assistantId) {
      dispatch({ type: 'RETRY', id: reqId, assistantId: opts.assistantId, userText, model, protocol: protocolId, params, outgoing: body });
    } else {
      dispatch({ type: 'SEND', id: reqId, model, protocol: protocolId, userText, params, outgoing: body });
    }
    setCurrentReqId(reqId);
    inspector.recordOutgoing(reqId, body);
    const ac = new AbortController();
    abortRef.current = ac;
    let assembledIncoming = '';
    let usage = null;
    await openStream({
      url,
      init: {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...authHeaderFor(protocolId, key),
        },
        body: JSON.stringify(body),
        signal: ac.signal,
      },
      onChunk: (event) => {
        // Non-SSE body (stream disabled): a single JSON document. Feed it to
        // the adapter's parseResponse so text + usage still surface.
        const trimmed = event.trim();
        if (trimmed.startsWith('{')) {
          try {
            const json = JSON.parse(trimmed);
            const resp = adapter.parseResponse ? adapter.parseResponse(json) : null;
            if (resp?.text) {
              assembledIncoming += resp.text;
              dispatch({ type: 'TOKEN', id: reqId, token: resp.text });
            }
            if (resp?.usage) usage = mergeUsage(usage, resp.usage);
          } catch {
            /* not JSON — ignore */
          }
          return;
        }
        for (const line of event.split('\n')) {
          if (!line.trim()) continue;
          const parsed = adapter.parseStreamChunk(line);
          if (parsed.token) {
            assembledIncoming += parsed.token;
            dispatch({ type: 'TOKEN', id: reqId, token: parsed.token });
          }
          if (parsed.usage) usage = mergeUsage(usage, parsed.usage);
        }
      },
      onDone: ({ headers } = {}) => {
        const incoming = { assembled_text: assembledIncoming, ...(usage ? { usage } : {}) };
        inspector.recordIncoming(reqId, incoming, headers || {});
        dispatch({ type: 'DONE', id: reqId, usage, incoming });
        abortRef.current = null;
      },
      onError: (e) => {
        const incoming = { error: e };
        inspector.recordIncoming(reqId, incoming, {});
        dispatch({ type: 'ERROR', id: reqId, error: e, incoming });
        abortRef.current = null;
      },
    });
  }, [key, model, protocolId, params, state.inFlight, state.messages, dispatch, inspector]);

  const doSend = useCallback(async () => {
    if (!draft.trim()) return;
    const text = draft;
    setDraft('');
    await executeSend(text);
  }, [draft, executeSend]);

  // Retry regenerates the assistant message it was clicked on, using the
  // user prompt that produced it and dropping everything after it.
  const retry = useCallback(async (assistantMessage) => {
    if (!assistantMessage || state.inFlight) return;
    if (!key || !model) return;
    const info = historyPrefixForRetry(state.messages, assistantMessage.id);
    if (!info) return;
    await executeSend(info.userText, { assistantId: assistantMessage.id, prefix: info.prefix });
  }, [key, model, state.inFlight, state.messages, executeSend]);

  const onAbort = useCallback(() => {
    if (abortRef.current) {
      abortRef.current.abort();
      abortRef.current = null;
    }
  }, []);

  const newChat = useCallback(() => {
    if (state.messages.length > 0 && typeof window !== 'undefined'
      && !window.confirm('Clear this conversation? This cannot be undone.')) {
      return;
    }
    dispatch({ type: 'CLEAR' });
    inspector.clear();
    setCurrentReqId(null);
    setDrawerOpen(false);
    setDraft('');
  }, [state.messages.length, dispatch, inspector]);

  const inspect = useCallback((message) => {
    const reqId = message?.reqId || inspector.findReqIdForMessage(message?.id, state.messages);
    if (reqId) setCurrentReqId(reqId);
    setActiveTab('outgoing');
    setDrawerOpen(true);
  }, [inspector, state.messages]);

  const copy = useCallback(async (message) => {
    try {
      await navigator.clipboard.writeText(message?.content || '');
    } catch {
      /* clipboard unavailable */
    }
  }, []);

  const lastUsage = useMemo(() => {
    for (let i = state.messages.length - 1; i >= 0; i--) {
      if (state.messages[i].usage) return state.messages[i].usage;
    }
    return null;
  }, [state.messages]);

  const sendDisabledReason = !key ? 'Playground key not ready'
    : !model ? 'Pick a model first'
      : !draft.trim() ? 'Type a message'
        : '';

  if (keyLoading) {
    return <div className="playground playground--centered"><Spinner label="Preparing playground key…" /></div>;
  }
  if (keyError) {
    return (
      <div className="playground playground--centered">
        <ErrorBanner error={keyError} onRetry={refreshKey} />
      </div>
    );
  }

  return (
    <div className="playground">
      <aside className={`playground__rail${railOpen ? ' playground__rail--open' : ''}`} aria-label="Playground settings">
        <div className="playground__rail-head">
          <span className="playground__rail-title">Settings</span>
          <button type="button" className="playground__rail-close" onClick={() => setRailOpen(false)} aria-label="Close settings">×</button>
        </div>
        <section className="playground-sidebar-section">
          <h4>Model</h4>
          <ModelPicker value={model} onChange={setModel} cooldownProviders={cooldown.providers} />
        </section>
        <section className="playground-sidebar-section">
          <h4>Protocol</h4>
          <ProtocolSwitcher
            value={protocolId}
            onChange={(v) => { userPickedProtocol.current = true; setProtocolId(v); }}
          />
        </section>
        <details className="playground-sidebar-section playground-sidebar-section--details" open>
          <summary>Parameters</summary>
          <ParamPanel params={params} onChange={setParams} />
        </details>
        <section className="playground-sidebar-section">
          <h4>Export</h4>
          <ExportButton messages={state.messages} />
        </section>
      </aside>
      {railOpen && <div className="playground__rail-backdrop" onClick={() => setRailOpen(false)} />}

      <div className="playground__chat">
        <header className="playground__chat-header">
          <button type="button" className="playground__rail-toggle" onClick={() => setRailOpen(true)} aria-label="Open settings">☰</button>
          <div className="playground__chat-title">
            <span className="playground__chat-model">{model || 'No model selected'}</span>
            <span className="playground__chat-provider">{providerKeyForProtocol(protocolId)}</span>
            {cooldown.state && <span className="badge badge--disabled">COOLDOWN</span>}
          </div>
          <div className="playground__chat-actions">
            <button type="button" onClick={newChat} disabled={state.messages.length === 0}>New chat</button>
            <button type="button" onClick={() => setDrawerOpen((o) => !o)}>
              {drawerOpen ? 'Hide inspector' : 'Inspect'}
            </button>
          </div>
        </header>

        <CooldownBanner state={cooldown.state} onRefresh={cooldown.refresh} />

        <div className="playground__messages" ref={scrollRef} onScroll={onMessagesScroll}>
          {state.messages.length === 0 && (
            <EmptyState
              title="Start a conversation"
              hint="Pick a model on the left, then send a message to try it."
            />
          )}
          {state.messages.map((m) => (
            <MessageBubble
              key={m.id}
              m={m}
              streamingFast={state.inFlight === m.reqId}
              onCopy={() => copy(m)}
              onInspect={() => inspect(m)}
              onRetry={m.role === 'assistant' ? () => retry(m) : undefined}
            />
          ))}
          {state.error && (
            <ErrorBanner error={state.error} onRetry={() => dispatch({ type: 'CLEAR_ERROR' })} />
          )}
        </div>
        {!atBottom && state.messages.length > 0 && (
          <button type="button" className="playground__jump" onClick={jumpToLatest}>
            Jump to latest ↓
          </button>
        )}

        <UsageFooter lastUsage={lastUsage} pricing={pricing.data} />

        <div className="playground__composer">
          <textarea
            className="playground__composer-input"
            rows={2}
            placeholder="Type a message…  (Enter to send, Shift+Enter for a new line)"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.nativeEvent?.isComposing) return;
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                doSend();
              } else if (e.key === 'Escape' && state.inFlight) {
                e.preventDefault();
                onAbort();
              }
            }}
          />
          {state.inFlight ? (
            <button onClick={onAbort} className="playground-stop-button">Stop</button>
          ) : (
            <button
              onClick={doSend}
              disabled={!draft.trim() || !key || !model}
              title={sendDisabledReason}
              className="primary"
            >
              Send
            </button>
          )}
        </div>
      </div>

      <InspectorDrawer
        open={drawerOpen}
        view={currentReqId ? inspector.viewFor(currentReqId) : null}
        activeTab={activeTab}
        onClose={() => setDrawerOpen(false)}
        onTabChange={setActiveTab}
        pretty={pretty}
        onPrettyToggle={() => setPretty((p) => !p)}
      />
    </div>
  );
}
