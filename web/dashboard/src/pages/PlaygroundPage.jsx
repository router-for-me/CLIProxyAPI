import React, { useCallback, useEffect, useRef, useState } from 'react';
import { getProtocol } from '../playground/protocols.js';
import { usePlaygroundKey } from '../playground/usePlaygroundKey.js';
import { usePlaygroundChat, openStream } from '../playground/usePlaygroundChat.js';
import ModelPicker from '../playground/ModelPicker.jsx';
import { detectProtocol } from '../playground/detectProtocol.js';
import { toJsonExport, toMarkdownExport } from '../playground/exportConversation.js';

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

function MessageBubble({ m }) {
  const isUser = m.role === 'user';
  return (
    <div className={`rounded p-3 ${isUser ? 'bg-muted' : 'bg-card border'}`}>
      <div className="text-xs text-muted-foreground mb-1">
        {m.role}
        {m.model ? ` · ${m.model}` : ''}
        {m.protocol ? ` · ${m.protocol}` : ''}
      </div>
      <div className="whitespace-pre-wrap text-sm">
        {m.content || (m.streaming ? '▍' : '')}
      </div>
      {m.usage && (
        <div className="text-xs text-muted-foreground mt-1">
          prompt {m.usage.prompt_tokens ?? 0} · completion {m.usage.completion_tokens ?? 0}
        </div>
      )}
    </div>
  );
}

function ProtocolSwitcher({ value, onChange }) {
  return (
    <select value={value} onChange={(e) => onChange(e.target.value)} className="w-full border rounded px-2 py-1 bg-background">
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
    <div className="space-y-2 text-sm">
      <label className="block">
        <span className="text-xs text-muted-foreground">System prompt</span>
        <textarea
          className="w-full border rounded px-2 py-1 bg-background"
          rows={3}
          value={params.system}
          onChange={(e) => update('system', e.target.value)}
        />
      </label>
      <label className="block">
        <span className="text-xs text-muted-foreground">Temperature ({params.temperature})</span>
        <input type="range" min="0" max="2" step="0.1" value={params.temperature} onChange={(e) => update('temperature', Number(e.target.value))} className="w-full" />
      </label>
      <label className="block">
        <span className="text-xs text-muted-foreground">Max tokens</span>
        <input type="number" min="1" max="32768" value={params.max_tokens} onChange={(e) => update('max_tokens', Number(e.target.value))} className="w-full border rounded px-2 py-1 bg-background" />
      </label>
      <label className="block">
        <span className="text-xs text-muted-foreground">Top-p</span>
        <input type="number" min="0" max="1" step="0.05" value={params.top_p} onChange={(e) => update('top_p', Number(e.target.value))} className="w-full border rounded px-2 py-1 bg-background" />
      </label>
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={params.stream} onChange={(e) => update('stream', e.target.checked)} />
        <span>Stream</span>
      </label>
    </div>
  );
}

function ExportButton({ messages }) {
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
    <div className="flex gap-2">
      <button onClick={() => dl('conversation.json', toJsonExport(messages), 'application/json')} className="text-xs px-2 py-1 border rounded">JSON</button>
      <button onClick={() => dl('conversation.md', toMarkdownExport(messages), 'text/markdown')} className="text-xs px-2 py-1 border rounded">Markdown</button>
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
  const userPickedProtocol = useRef(false);

  // Auto-detect protocol when the model changes, unless the user
  // explicitly picked one.
  useEffect(() => {
    if (!model || userPickedProtocol.current) return;
    setProtocolId(detectProtocol({ model }));
  }, [model]);

  const doSend = useCallback(async () => {
    if (!key || !model || state.inFlight || !draft.trim()) return;
    const adapter = getProtocol(protocolId);
    const history = [];
    if (params.system) history.push({ role: 'system', content: params.system });
    for (const m of state.messages) history.push({ role: m.role, content: m.content });
    history.push({ role: 'user', content: draft });
    const reqId = `r-${Date.now()}`;
    const body = adapter.buildRequest(model, history, { ...params });
    const url = adapter.endpoint(model, params);
    dispatch({ type: 'SEND', id: reqId, model, protocol: protocolId, userText: draft, params });
    setDraft('');
    const ac = new AbortController();
    abortRef.current = ac;
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
        for (const line of event.split('\n')) {
          if (!line.trim()) continue;
          const { token } = adapter.parseStreamChunk(line);
          if (token) dispatch({ type: 'TOKEN', id: reqId, token });
        }
      },
      onDone: () => {
        dispatch({ type: 'DONE', id: reqId, usage: null });
        abortRef.current = null;
      },
      onError: (e) => {
        dispatch({ type: 'ERROR', id: reqId, error: e });
        abortRef.current = null;
      },
    });
  }, [key, model, protocolId, params, state.inFlight, state.messages, draft, dispatch]);

  const onAbort = useCallback(() => {
    if (abortRef.current) {
      abortRef.current.abort();
      abortRef.current = null;
    }
  }, []);

  if (keyLoading) return <div className="p-6 text-sm text-muted-foreground">Preparing playground key…</div>;
  if (keyError) {
    return (
      <div className="p-6">
        <div className="rounded border border-red-500 bg-red-50 dark:bg-red-950 p-3">
          <div className="font-semibold">Playground key bootstrap failed</div>
          <div className="text-sm">{keyError.message}</div>
          <button onClick={refreshKey} className="mt-2 px-3 py-1 border rounded">Retry</button>
        </div>
      </div>
    );
  }

  return (
    <div className="flex h-full">
      <div className="w-80 border-r p-4 space-y-4 overflow-y-auto">
        <ModelPicker value={model} onChange={setModel} />
        <ProtocolSwitcher
          value={protocolId}
          onChange={(v) => { userPickedProtocol.current = true; setProtocolId(v); }}
        />
        <ParamPanel params={params} onChange={setParams} />
        <ExportButton messages={state.messages} />
      </div>
      <div className="flex-1 flex flex-col">
        <div className="flex-1 overflow-y-auto p-4 space-y-3">
          {state.messages.length === 0 && (
            <div className="text-sm text-muted-foreground">Pick a model and send a message to start.</div>
          )}
          {state.messages.map((m) => <MessageBubble key={m.id} m={m} />)}
          {state.error && (
            <div className="rounded border border-red-500 bg-red-50 dark:bg-red-950 p-3 text-sm">
              <div className="font-semibold">Error</div>
              <div>{state.error.message}</div>
              {state.error.body && (
                <pre className="mt-2 text-xs whitespace-pre-wrap break-all">{state.error.body}</pre>
              )}
              <button onClick={() => dispatch({ type: 'CLEAR_ERROR' })} className="mt-1 text-xs underline">Dismiss</button>
            </div>
          )}
        </div>
        <div className="border-t p-3 flex gap-2">
          <textarea
            className="flex-1 border rounded px-2 py-1 bg-background resize-none"
            rows={2}
            placeholder="Type a message…"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) doSend(); }}
          />
          {state.inFlight ? (
            <button onClick={onAbort} className="px-4 py-2 rounded border">Stop</button>
          ) : (
            <button
              onClick={doSend}
              disabled={!draft.trim() || !key || !model}
              className="px-4 py-2 rounded bg-primary text-primary-foreground disabled:opacity-50"
            >
              Send
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
