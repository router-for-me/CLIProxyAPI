# Playground Feature Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a top-level dashboard page `/playground` that lets operators test any LIVE model from the catalog or upstream providers, end-to-end through the existing NixLLM proxy routes. Power-user oriented: multi-turn, multi-protocol (OpenAI-compat, Gemini, Claude, Codex), multi-model compare, raw inspector, usage/cost footer, conversation export.

**Architecture:** Two-path frontend-only design. Management routes (`/v0/management/*`) carry the dashboard's `MANAGEMENT_PASSWORD` bearer for UI bootstrap and queries. Standard proxy routes (`/v1/chat/completions`, `/v1/messages`, `/v1beta/models/{m}:generateContent`, `/v1/responses`) carry a dedicated, auto-bootstrapped "playground" API key for chat traffic. No Go backend changes.

**Tech Stack:** React 18 + react-router-dom 6 + Vite (existing). `node:test` + `node:assert/strict` for unit tests (matches `web/dashboard/src/components/modelRouteProvider.test.js`). No new test libraries — the dashboard has no test runner installed yet; Task 0 adds one via `node --test`.

**Reference:** Approved design at `docs/plans/2026-08-29-playground-feature-design.md` (commit `de754156`).

---

## Task 0: Add node:test runner wiring

**Files:**
- Modify: `web/dashboard/package.json` (add `test` script only — no new deps)

**Step 1: Confirm there's no test script yet**

Run: `grep '"test"' /home/bilfid/projects/nixllm/web/dashboard/package.json`
Expected: no match (the `scripts` block currently has `dev`, `build`, `preview`, `lint`, `cf:*` but no `test`).

**Step 2: Add the test script**

Edit `web/dashboard/package.json` `scripts` block. Add after `"lint"`:

```json
    "test": "node --test --experimental-vm-modules --no-warnings src/**/*.test.js src/**/*.test.jsx 2>/dev/null || node --test src/**/*.test.js src/**/*.test.jsx"
```

(Primary invocation uses `--experimental-vm-modules` so future ESM React tests can import JSX via Vite's transform. The fallback runs plain `node --test` against existing `.test.js` files like `modelRouteProvider.test.js`, which uses `node:test` directly. The `2>/dev/null` swallows the expected "no test files matched" error from the primary when only `.test.jsx` is present.)

**Step 3: Run existing tests to confirm the runner works**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && npm test 2>&1 | tail -20`
Expected: existing `modelRouteProvider.test.js` tests pass; output ends with `# pass N` and `# fail 0`.

**Step 4: Commit**

```bash
cd /home/bilfid/projects/nixllm
git add web/dashboard/package.json
git commit -m "chore(dashboard): add npm test script using node:test"
```

---

## Task 1: Protocol adapter module — types and registry

**Files:**
- Create: `web/dashboard/src/playground/protocols.js`
- Test: `web/dashboard/src/playground/protocols.test.js`

**Step 1: Write the failing test**

Create `web/dashboard/src/playground/protocols.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { listProtocols, getProtocol } from './protocols.js';

test('listProtocols returns the four supported protocols in stable order', () => {
  const ids = listProtocols().map((p) => p.id);
  assert.deepEqual(ids, ['openai-compat', 'gemini', 'claude', 'codex']);
});

test('getProtocol returns an adapter for each known id', () => {
  for (const id of ['openai-compat', 'gemini', 'claude', 'codex']) {
    const a = getProtocol(id);
    assert.equal(a.id, id);
    assert.equal(typeof a.endpoint, 'function');
    assert.equal(typeof a.buildRequest, 'function');
    assert.equal(typeof a.parseStreamChunk, 'function');
    assert.equal(typeof a.buildErrorPayload, 'function');
    assert.equal(typeof a.supports, 'object');
  }
});

test('getProtocol throws on unknown id', () => {
  assert.throws(() => getProtocol('not-a-protocol'), /Unknown protocol/);
});
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/protocols.test.js 2>&1 | tail -20`
Expected: FAIL — `Cannot find module './protocols.js'`.

**Step 3: Write the minimal implementation**

Create `web/dashboard/src/playground/protocols.js`:

```js
// Protocol adapter registry.
//
// Each protocol owns its wire format (endpoint URL, request body shape,
// stream chunk parsing, error normalization). The chat pipeline stays
// protocol-agnostic — it calls adapter.buildRequest() and
// adapter.parseStreamChunk() and never branches on protocol id.
//
// Adapters are pure functions of their inputs; no network or DOM access.

import { openaiCompatAdapter } from './adapters/openaiCompat.js';
import { geminiAdapter } from './adapters/gemini.js';
import { claudeAdapter } from './adapters/claude.js';
import { codexAdapter } from './adapters/codex.js';

const ADAPTERS = [openaiCompatAdapter, geminiAdapter, claudeAdapter, codexAdapter];

export function listProtocols() {
  return ADAPTERS.slice();
}

export function getProtocol(id) {
  const adapter = ADAPTERS.find((a) => a.id === id);
  if (!adapter) throw new Error(`Unknown protocol: ${id}`);
  return adapter;
}
```

Create four empty adapter files with stub `id` so the imports resolve. Each will be filled in Task 2-5.

`web/dashboard/src/playground/adapters/openaiCompat.js`:
```js
export const openaiCompatAdapter = {
  id: 'openai-compat',
  endpoint: () => '/v1/chat/completions',
  buildRequest: () => ({}),
  parseStreamChunk: () => ({ token: '', done: true }),
  buildErrorPayload: (err) => ({ message: String(err), type: 'error' }),
  supports: { streaming: true, tools: true, vision: true, system_prompt: true },
};
```

Repeat the same shape for `gemini.js`, `claude.js`, `codex.js` with `id` set to each protocol name and `endpoint` returning the canonical path:

- gemini: `endpoint: (m) => \`/v1beta/models/${m}:generateContent\``
- claude: `endpoint: () => '/v1/messages'`
- codex: `endpoint: () => '/v1/responses'`

(Leave `buildRequest` and `parseStreamChunk` as stubs — Tasks 2-5 replace them.)

**Step 4: Run test to verify it passes**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/protocols.test.js 2>&1 | tail -10`
Expected: `# pass 3`, `# fail 0`.

**Step 5: Commit**

```bash
cd /home/bilfid/projects/nixllm
git add web/dashboard/src/playground/
git commit -m "feat(playground): add protocol adapter registry with stub adapters"
```

---

## Task 2: OpenAI-compat adapter

**Files:**
- Modify: `web/dashboard/src/playground/adapters/openaiCompat.js`
- Test: `web/dashboard/src/playground/adapters/openaiCompat.test.js`

**Step 1: Write the failing test**

Create `web/dashboard/src/playground/adapters/openaiCompat.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { openaiCompatAdapter } from './openaiCompat.js';

const history = [
  { role: 'system', content: 'be brief' },
  { role: 'user', content: 'hi' },
];
const params = { temperature: 0.2, max_tokens: 100, top_p: 1, stream: true };

test('buildRequest flattens messages and merges params', () => {
  const body = openaiCompatAdapter.buildRequest('gpt-4o', history, params);
  assert.equal(body.model, 'gpt-4o');
  assert.equal(body.stream, true);
  assert.equal(body.temperature, 0.2);
  assert.equal(body.max_tokens, 100);
  assert.equal(body.top_p, 1);
  assert.deepEqual(body.messages, history);
});

test('buildRequest omits undefined params', () => {
  const body = openaiCompatAdapter.buildRequest('m', [{ role: 'user', content: 'x' }], {});
  assert.equal('temperature' in body, false);
  assert.equal('max_tokens' in body, false);
  assert.equal('top_p' in body, false);
});

test('parseStreamChunk extracts a content token from an OpenAI SSE delta', () => {
  const line = 'data: {"choices":[{"delta":{"content":"hello"}}]}';
  const out = openaiCompatAdapter.parseStreamChunk(line);
  assert.equal(out.token, 'hello');
  assert.equal(out.done, false);
});

test('parseStreamChunk signals done on the [DONE] sentinel', () => {
  const out = openaiCompatAdapter.parseStreamChunk('data: [DONE]');
  assert.equal(out.token, '');
  assert.equal(out.done, true);
});

test('parseStreamChunk returns empty on non-data lines', () => {
  const out = openaiCompatAdapter.parseStreamChunk(': heartbeat');
  assert.equal(out.token, '');
  assert.equal(out.done, false);
});
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/adapters/openaiCompat.test.js 2>&1 | tail -10`
Expected: FAIL — first test expects `body.stream === true` but stub returns `{}`.

**Step 3: Write the implementation**

Replace `web/dashboard/src/playground/adapters/openaiCompat.js` with:

```js
// OpenAI-compatible chat completions adapter. Covers the standard
// /v1/chat/completions endpoint used by all OpenAI-compat upstream
// providers and by Claude/Gemini when reached through their OpenAI-compat
// translator.

export const openaiCompatAdapter = {
  id: 'openai-compat',
  endpoint: () => '/v1/chat/completions',
  supports: { streaming: true, tools: true, vision: true, system_prompt: true },

  buildRequest(model, messages, params) {
    const body = { model, messages };
    for (const k of ['temperature', 'max_tokens', 'top_p', 'stream']) {
      if (params[k] !== undefined) body[k] = params[k];
    }
    return body;
  },

  parseStreamChunk(line) {
    if (!line.startsWith('data:')) return { token: '', done: false };
    const payload = line.slice(5).trim();
    if (payload === '[DONE]') return { token: '', done: true };
    try {
      const json = JSON.parse(payload);
      const token = json?.choices?.[0]?.delta?.content ?? '';
      return { token: token || '', done: false };
    } catch {
      return { token: '', done: false };
    }
  },

  buildErrorPayload(err) {
    if (err && err.status) {
      return { message: err.message || 'Request failed', type: 'upstream', status: err.status };
    }
    return { message: String(err), type: 'network' };
  },
};
```

**Step 4: Run test to verify it passes**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/adapters/openaiCompat.test.js 2>&1 | tail -10`
Expected: `# pass 5`, `# fail 0`.

**Step 5: Commit**

```bash
cd /home/bilfid/projects/nixllm
git add web/dashboard/src/playground/adapters/openaiCompat.js web/dashboard/src/playground/adapters/openaiCompat.test.js
git commit -m "feat(playground): implement OpenAI-compat protocol adapter"
```

---

## Task 3: Gemini native adapter

**Files:**
- Modify: `web/dashboard/src/playground/adapters/gemini.js`
- Test: `web/dashboard/src/playground/adapters/gemini.test.js`

**Step 1: Write the failing test**

Create `web/dashboard/src/playground/adapters/gemini.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { geminiAdapter } from './gemini.js';

const history = [
  { role: 'system', content: 'be brief' },
  { role: 'user', content: 'hi' },
];
const params = { temperature: 0.3, max_tokens: 200, top_p: 0.9, stream: true };

test('endpoint uses streamGenerateContent when streaming is enabled', () => {
  assert.equal(
    geminiAdapter.endpoint('gemini-1.5-pro', { stream: true }),
    '/v1beta/models/gemini-1.5-pro:streamGenerateContent?alt=sse',
  );
});

test('endpoint uses generateContent when streaming is disabled', () => {
  assert.equal(
    geminiAdapter.endpoint('gemini-1.5-pro', { stream: false }),
    '/v1beta/models/gemini-1.5-pro:generateContent',
  );
});

test('buildRequest converts messages to Gemini contents[] with systemInstruction', () => {
  const body = geminiAdapter.buildRequest('gemini-1.5-pro', history, params);
  assert.equal(body.model, undefined); // model is in the URL, not the body
  assert.deepEqual(body.systemInstruction, { parts: [{ text: 'be brief' }] });
  assert.deepEqual(body.contents, [{ role: 'user', parts: [{ text: 'hi' }] }]);
  assert.equal(body.generationConfig.temperature, 0.3);
  assert.equal(body.generationConfig.maxOutputTokens, 200);
  assert.equal(body.generationConfig.topP, 0.9);
});

test('buildRequest maps assistant role to model role', () => {
  const body = geminiAdapter.buildRequest('m', [
    { role: 'user', content: 'q' },
    { role: 'assistant', content: 'a' },
    { role: 'user', content: 'q2' },
  ], {});
  assert.deepEqual(body.contents, [
    { role: 'user', parts: [{ text: 'q' }] },
    { role: 'model', parts: [{ text: 'a' }] },
    { role: 'user', parts: [{ text: 'q2' }] },
  ]);
});

test('parseStreamChunk extracts a text token from a Gemini SSE data line', () => {
  const line = 'data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}';
  const out = geminiAdapter.parseStreamChunk(line);
  assert.equal(out.token, 'hello');
  assert.equal(out.done, false);
});
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/adapters/gemini.test.js 2>&1 | tail -10`
Expected: FAIL — first test expects a specific URL but stub returns `{}` / no URL.

**Step 3: Write the implementation**

Replace `web/dashboard/src/playground/adapters/gemini.js` with:

```js
// Gemini native generateContent adapter. Sends to
// /v1beta/models/{model}:generateContent (or :streamGenerateContent?alt=sse
// when streaming). History is converted to Gemini's contents[] shape with
// role "model" instead of "assistant" and an optional systemInstruction
// pulled from any leading system message.

export const geminiAdapter = {
  id: 'gemini',
  endpoint: (model, params = {}) => {
    const suffix = params.stream
      ? ':streamGenerateContent?alt=sse'
      : ':generateContent';
    return `/v1beta/models/${encodeURIComponent(model)}${suffix}`;
  },
  supports: { streaming: true, tools: true, vision: true, system_prompt: true },

  buildRequest(_model, messages, params) {
    let systemText = '';
    const contents = [];
    for (const m of messages) {
      if (m.role === 'system') {
        systemText = m.content;
        continue;
      }
      contents.push({
        role: m.role === 'assistant' ? 'model' : 'user',
        parts: [{ text: m.content }],
      });
    }
    const generationConfig = {};
    if (params.temperature !== undefined) generationConfig.temperature = params.temperature;
    if (params.max_tokens !== undefined) generationConfig.maxOutputTokens = params.max_tokens;
    if (params.top_p !== undefined) generationConfig.topP = params.top_p;
    const body = { contents };
    if (systemText) body.systemInstruction = { parts: [{ text: systemText }] };
    if (Object.keys(generationConfig).length) body.generationConfig = generationConfig;
    return body;
  },

  parseStreamChunk(line) {
    if (!line.startsWith('data:')) return { token: '', done: false };
    const payload = line.slice(5).trim();
    if (!payload) return { token: '', done: false };
    try {
      const json = JSON.parse(payload);
      const text = json?.candidates?.[0]?.content?.parts?.[0]?.text ?? '';
      const finish = json?.candidates?.[0]?.finishReason;
      return { token: text || '', done: !!finish };
    } catch {
      return { token: '', done: false };
    }
  },

  buildErrorPayload(err) {
    if (err && err.status) {
      return { message: err.message || 'Request failed', type: 'upstream', status: err.status };
    }
    return { message: String(err), type: 'network' };
  },
};
```

**Step 4: Run test to verify it passes**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/adapters/gemini.test.js 2>&1 | tail -10`
Expected: `# pass 5`, `# fail 0`.

**Step 5: Commit**

```bash
cd /home/bilfid/projects/nixllm
git add web/dashboard/src/playground/adapters/gemini.js web/dashboard/src/playground/adapters/gemini.test.js
git commit -m "feat(playground): implement Gemini native protocol adapter"
```

---

## Task 4: Claude Messages adapter

**Files:**
- Modify: `web/dashboard/src/playground/adapters/claude.js`
- Test: `web/dashboard/src/playground/adapters/claude.test.js`

**Step 1: Write the failing test**

Create `web/dashboard/src/playground/adapters/claude.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { claudeAdapter } from './claude.js';

const history = [
  { role: 'system', content: 'be brief' },
  { role: 'user', content: 'hi' },
  { role: 'assistant', content: 'hello' },
  { role: 'user', content: 'how are you?' },
];
const params = { temperature: 0.4, max_tokens: 256, top_p: 0.95, stream: true };

test('buildRequest moves system messages to top-level system field', () => {
  const body = claudeAdapter.buildRequest('claude-3-5-sonnet', history, params);
  assert.equal(body.model, 'claude-3-5-sonnet');
  assert.equal(body.system, 'be brief');
  assert.equal(body.stream, true);
  assert.equal(body.temperature, 0.4);
  assert.equal(body.max_tokens, 256);
  assert.equal(body.top_p, 0.95);
  assert.deepEqual(body.messages, [
    { role: 'user', content: 'hi' },
    { role: 'assistant', content: 'hello' },
    { role: 'user', content: 'how are you?' },
  ]);
});

test('parseStreamChunk extracts a text token from a content_block_delta event', () => {
  const line = 'event: content_block_delta\ndata: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}';
  const out = claudeAdapter.parseStreamChunk(line);
  assert.equal(out.token, 'hi');
  assert.equal(out.done, false);
});

test('parseStreamChunk signals done on message_stop', () => {
  const line = 'event: message_stop\ndata: {"type":"message_stop"}';
  const out = claudeAdapter.parseStreamChunk(line);
  assert.equal(out.token, '');
  assert.equal(out.done, true);
});
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/adapters/claude.test.js 2>&1 | tail -10`
Expected: FAIL — first test expects `body.system === 'be brief'` but stub returns `{}`.

**Step 3: Write the implementation**

Replace `web/dashboard/src/playground/adapters/claude.js` with:

```js
// Anthropic Claude Messages adapter. Sends to /v1/messages. System
// messages are extracted from the history and placed in the top-level
// `system` field (Claude's wire format does not allow `role: "system"`
// in the messages array).

export const claudeAdapter = {
  id: 'claude',
  endpoint: () => '/v1/messages',
  supports: { streaming: true, tools: true, vision: true, system_prompt: true },

  buildRequest(model, messages, params) {
    let systemText = '';
    const filtered = [];
    for (const m of messages) {
      if (m.role === 'system') {
        systemText = m.content;
        continue;
      }
      filtered.push({ role: m.role, content: m.content });
    }
    const body = { model, messages: filtered };
    if (systemText) body.system = systemText;
    for (const k of ['temperature', 'max_tokens', 'top_p', 'stream']) {
      if (params[k] !== undefined) body[k] = params[k];
    }
    return body;
  },

  // Claude's SSE format is multi-line: each event is `event: <name>` followed
  // by `data: <json>`. The caller is expected to buffer and feed one logical
  // line (event + data) to this parser — see usePlaygroundChat for the
  // buffer logic. For v1 we accept both single-line and two-line inputs.
  parseStreamChunk(raw) {
    if (!raw) return { token: '', done: false };
    const eventMatch = raw.match(/^event:\s*(\S+)/m);
    const dataMatch = raw.match(/^data:\s*(.+)$/m);
    if (eventMatch?.[1] === 'message_stop') {
      return { token: '', done: true };
    }
    if (!dataMatch) return { token: '', done: false };
    try {
      const json = JSON.parse(dataMatch[1]);
      if (eventMatch?.[1] === 'content_block_delta') {
        return { token: json?.delta?.text ?? '', done: false };
      }
      if (json?.type === 'message_stop') return { token: '', done: true };
      return { token: '', done: false };
    } catch {
      return { token: '', done: false };
    }
  },

  buildErrorPayload(err) {
    if (err && err.status) {
      return { message: err.message || 'Request failed', type: 'upstream', status: err.status };
    }
    return { message: String(err), type: 'network' };
  },
};
```

**Step 4: Run test to verify it passes**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/adapters/claude.test.js 2>&1 | tail -10`
Expected: `# pass 3`, `# fail 0`.

**Step 5: Commit**

```bash
cd /home/bilfid/projects/nixllm
git add web/dashboard/src/playground/adapters/claude.js web/dashboard/src/playground/adapters/claude.test.js
git commit -m "feat(playground): implement Claude Messages protocol adapter"
```

---

## Task 5: Codex / Responses adapter

**Files:**
- Modify: `web/dashboard/src/playground/adapters/codex.js`
- Test: `web/dashboard/src/playground/adapters/codex.test.js`

**Step 1: Write the failing test**

Create `web/dashboard/src/playground/adapters/codex.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { codexAdapter } from './codex.js';

const history = [
  { role: 'user', content: 'hi' },
  { role: 'assistant', content: 'hello' },
];
const params = { temperature: 0.7, max_tokens: 512, stream: true };

test('buildRequest maps to Responses API input + model', () => {
  const body = codexAdapter.buildRequest('gpt-4o', history, params);
  assert.equal(body.model, 'gpt-4o');
  assert.equal(body.stream, true);
  assert.equal(body.temperature, 0.7);
  assert.equal(body.max_output_tokens, 512);
  // Codex Responses API takes a flat `input` string; for v1 we join the
  // history with role tags so multi-turn works.
  assert.match(body.input, /user: hi/);
  assert.match(body.input, /assistant: hello/);
});

test('parseStreamChunk extracts a text token from a Responses SSE delta', () => {
  const line = 'data: {"type":"response.output_text.delta","delta":"yo"}';
  const out = codexAdapter.parseStreamChunk(line);
  assert.equal(out.token, 'yo');
  assert.equal(out.done, false);
});

test('parseStreamChunk signals done on response.completed', () => {
  const line = 'data: {"type":"response.completed"}';
  const out = codexAdapter.parseStreamChunk(line);
  assert.equal(out.token, '');
  assert.equal(out.done, true);
});
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/adapters/codex.test.js 2>&1 | tail -10`
Expected: FAIL — first test expects `body.model === 'gpt-4o'` but stub returns `{}`.

**Step 3: Write the implementation**

Replace `web/dashboard/src/playground/adapters/codex.js` with:

```js
// OpenAI Responses API adapter (Codex-style). Sends to /v1/responses.
// For v1, history is joined into a flat `input` string with role tags —
// the Responses API accepts a string input and the proxy translator is
// responsible for any further conversion.

export const codexAdapter = {
  id: 'codex',
  endpoint: () => '/v1/responses',
  supports: { streaming: true, tools: true, vision: false, system_prompt: false },

  buildRequest(model, messages, params) {
    const input = messages
      .map((m) => `${m.role}: ${m.content}`)
      .join('\n');
    const body = { model, input };
    if (params.temperature !== undefined) body.temperature = params.temperature;
    if (params.max_tokens !== undefined) body.max_output_tokens = params.max_tokens;
    if (params.stream !== undefined) body.stream = params.stream;
    return body;
  },

  parseStreamChunk(line) {
    if (!line.startsWith('data:')) return { token: '', done: false };
    const payload = line.slice(5).trim();
    if (!payload) return { token: '', done: false };
    try {
      const json = JSON.parse(payload);
      if (json?.type === 'response.completed') return { token: '', done: true };
      if (json?.type === 'response.output_text.delta') {
        return { token: json.delta ?? '', done: false };
      }
      return { token: '', done: false };
    } catch {
      return { token: '', done: false };
    }
  },

  buildErrorPayload(err) {
    if (err && err.status) {
      return { message: err.message || 'Request failed', type: 'upstream', status: err.status };
    }
    return { message: String(err), type: 'network' };
  },
};
```

**Step 4: Run test to verify it passes**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/adapters/codex.test.js 2>&1 | tail -10`
Expected: `# pass 3`, `# fail 0`.

**Step 5: Commit**

```bash
cd /home/bilfid/projects/nixllm
git add web/dashboard/src/playground/adapters/codex.js web/dashboard/src/playground/adapters/codex.test.js
git commit -m "feat(playground): implement Codex Responses API adapter"
```

---

## Task 6: `usePlaygroundKey` hook

**Files:**
- Create: `web/dashboard/src/playground/usePlaygroundKey.js`
- Test: `web/dashboard/src/playground/usePlaygroundKey.test.js`

**Step 1: Write the failing test**

Create `web/dashboard/src/playground/usePlaygroundKey.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { resolvePlaygroundKey, PLAYGROUND_KEY_NAME } from './usePlaygroundKey.js';

// resolvePlaygroundKey is the pure half of usePlaygroundKey: given a
// stored localStorage value and a list-fetcher + creator, it either
// returns the cached key or bootstraps one idempotently.
//
// We test it directly so we don't need a React renderer.

async function withFetch(handlers, fn) {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url, init = {}) => {
    const key = `${init.method || 'GET'} ${url}`;
    const handler = handlers[key];
    if (!handler) throw new Error(`Unexpected fetch: ${key}`);
    return handler(init);
  };
  try {
    return await fn();
  } finally {
    globalThis.fetch = originalFetch;
  }
}

function ok(body, status = 200) {
  return { ok: status < 400, status, json: async () => body, text: async () => JSON.stringify(body) };
}

test('returns the cached key without fetching when present', async () => {
  const cached = JSON.stringify({ id: 'k1', secret: 's3cr3t' });
  let calls = 0;
  const key = await resolvePlaygroundKey({
    cached,
    list: async () => { calls++; return []; },
    create: async () => { calls++; throw new Error('should not be called'); },
  });
  assert.equal(key, 's3cr3t');
  assert.equal(calls, 0);
});

test('bootstraps a new key when nothing is cached', async () => {
  let listed = 0;
  const key = await resolvePlaygroundKey({
    cached: null,
    list: async () => { listed++; return []; },
    create: async (name) => {
      assert.equal(name, PLAYGROUND_KEY_NAME);
      return { id: 'k2', secret: 'fresh-key' };
    },
  });
  assert.equal(key, 'fresh-key');
  assert.equal(listed, 1);
});

test('falls back to list when create returns 409 (already exists)', async () => {
  const key = await resolvePlaygroundKey({
    cached: null,
    list: async () => [{ id: 'k3', name: PLAYGROUND_KEY_NAME, secret: 'existing-key' }],
    create: async () => {
      const e = new Error('Conflict');
      e.status = 409;
      throw e;
    },
  });
  assert.equal(key, 'existing-key');
});
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/usePlaygroundKey.test.js 2>&1 | tail -10`
Expected: FAIL — `Cannot find module './usePlaygroundKey.js'`.

**Step 3: Write the implementation**

Create `web/dashboard/src/playground/usePlaygroundKey.js`:

```js
// Dedicated "playground" API key bootstrap.
//
// The playground must use a real proxy API key (not the management
// password) so traffic is attributable in usage stats and goes through
// the proxy's auth/quota/policy stack. This hook ensures such a key
// exists, named `playground`, and caches its secret in localStorage.
//
// The first half (resolvePlaygroundKey) is a pure async function
// suitable for direct testing without React. The hook wraps it with
// useEffect/useState for component use.

import { useCallback, useEffect, useState } from 'react';
import { listAPIKeys, createAPIKey } from '../api/client.js';

const STORAGE_KEY = 'nixllm.playground.key';
export const PLAYGROUND_KEY_NAME = 'playground';

export async function resolvePlaygroundKey({ cached, list, create }) {
  if (cached) {
    try {
      const parsed = JSON.parse(cached);
      if (parsed?.secret) return parsed.secret;
    } catch {
      /* fall through to bootstrap */
    }
  }
  try {
    const created = await create(PLAYGROUND_KEY_NAME);
    if (created?.secret) return created.secret;
  } catch (e) {
    if (e?.status !== 409) throw e;
  }
  // 409 means the key already exists — fetch and return it.
  const rows = await list();
  const found = rows.find((r) => r.name === PLAYGROUND_KEY_NAME);
  if (!found?.secret) {
    throw new Error('Playground key not found after create-409');
  }
  return found.secret;
}

export function usePlaygroundKey() {
  const [key, setKey] = useState(() => {
    try {
      const raw = localStorage.getItem(STORAGE_KEY);
      const parsed = raw ? JSON.parse(raw) : null;
      return parsed?.secret || null;
    } catch {
      return null;
    }
  });
  const [error, setError] = useState(null);
  const [loading, setLoading] = useState(!key);
  const refresh = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const cached = (() => {
        try { return localStorage.getItem(STORAGE_KEY); } catch { return null; }
      })();
      const secret = await resolvePlaygroundKey({
        cached,
        list: async () => {
          const res = await listAPIKeys({ search: PLAYGROUND_KEY_NAME, pageSize: 100 });
          return res?.items || res?.keys || res || [];
        },
        create: async (name) => {
          const res = await createAPIKey({ name });
          return { id: res?.id, secret: res?.secret || res?.api_key };
        },
      });
      localStorage.setItem(STORAGE_KEY, JSON.stringify({ id: 'playground', secret }));
      setKey(secret);
    } catch (e) {
      setError(e);
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => { if (!key) refresh(); }, [key, refresh]);
  return { key, error, loading, refresh };
}
```

**Step 4: Run test to verify it passes**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/usePlaygroundKey.test.js 2>&1 | tail -10`
Expected: `# pass 3`, `# fail 0`.

**Step 5: Commit**

```bash
cd /home/bilfid/projects/nixllm
git add web/dashboard/src/playground/usePlaygroundKey.js web/dashboard/src/playground/usePlaygroundKey.test.js
git commit -m "feat(playground): add usePlaygroundKey hook with idempotent bootstrap"
```

---

## Task 7: Chat state machine (reducer)

**Files:**
- Create: `web/dashboard/src/playground/usePlaygroundChat.js`
- Test: `web/dashboard/src/playground/usePlaygroundChat.test.js`

**Step 1: Write the failing test**

Create `web/dashboard/src/playground/usePlaygroundChat.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { reducer, initialState } from './usePlaygroundChat.js';

const baseParams = { temperature: 0.5, stream: true };

test('initialState has empty messages and no in-flight request', () => {
  assert.deepEqual(initialState.messages, []);
  assert.equal(initialState.inFlight, null);
  assert.equal(initialState.error, null);
});

test('SEND adds a user message and an empty assistant placeholder', () => {
  const s = reducer(initialState, {
    type: 'SEND',
    id: 'req-1',
    model: 'gpt-4o',
    protocol: 'openai-compat',
    userText: 'hi',
    params: baseParams,
  });
  assert.equal(s.messages.length, 2);
  assert.equal(s.messages[0].role, 'user');
  assert.equal(s.messages[0].content, 'hi');
  assert.equal(s.messages[1].role, 'assistant');
  assert.equal(s.messages[1].content, '');
  assert.equal(s.messages[1].streaming, true);
  assert.equal(s.inFlight, 'req-1');
});

test('TOKEN appends to the in-flight assistant message', () => {
  const a = reducer(initialState, { type: 'SEND', id: 'r1', model: 'm', protocol: 'openai-compat', userText: 'hi', params: baseParams });
  const b = reducer(a, { type: 'TOKEN', id: 'r1', token: 'hel' });
  const c = reducer(b, { type: 'TOKEN', id: 'r1', token: 'lo' });
  assert.equal(c.messages[1].content, 'hello');
  assert.equal(c.messages[1].streaming, true);
  assert.equal(c.inFlight, 'r1');
});

test('DONE clears streaming, inFlight, and records usage', () => {
  const a = reducer(initialState, { type: 'SEND', id: 'r1', model: 'm', protocol: 'openai-compat', userText: 'hi', params: baseParams });
  const b = reducer(a, { type: 'DONE', id: 'r1', usage: { prompt_tokens: 5, completion_tokens: 7 } });
  assert.equal(b.messages[1].streaming, false);
  assert.equal(b.inFlight, null);
  assert.deepEqual(b.messages[1].usage, { prompt_tokens: 5, completion_tokens: 7 });
  assert.equal(b.lastUsage, 'r1');
});

test('ERROR records the error and stops streaming', () => {
  const a = reducer(initialState, { type: 'SEND', id: 'r1', model: 'm', protocol: 'openai-compat', userText: 'hi', params: baseParams });
  const b = reducer(a, { type: 'ERROR', id: 'r1', error: { message: 'boom', type: 'upstream' } });
  assert.equal(b.messages[1].streaming, false);
  assert.deepEqual(b.error, { message: 'boom', type: 'upstream' });
  assert.equal(b.inFlight, null);
});

test('CLEAR_ERROR removes the error without affecting messages', () => {
  const a = reducer(initialState, { type: 'SEND', id: 'r1', model: 'm', protocol: 'openai-compat', userText: 'hi', params: baseParams });
  const b = reducer(a, { type: 'ERROR', id: 'r1', error: { message: 'boom' } });
  const c = reducer(b, { type: 'CLEAR_ERROR' });
  assert.equal(c.error, null);
  assert.equal(c.messages.length, 2);
});
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/usePlaygroundChat.test.js 2>&1 | tail -10`
Expected: FAIL — `Cannot find module`.

**Step 3: Write the implementation**

Create `web/dashboard/src/playground/usePlaygroundChat.js`:

```js
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
//
// The component layer maps fetch/SSE events to these actions.

import { useCallback, useReducer } from 'react';

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

// sendMessage is the function the component calls. It opens the SSE
// stream, dispatches TOKEN as chunks arrive, and dispatches DONE/ERROR
// on completion. Returns an AbortController.
export function usePlaygroundChat() {
  const [state, dispatch] = useReducer(reducer, initialState);
  return { state, dispatch };
}

// openStream encapsulates the fetch + SSE pipeline so sendMessage is
// easy to reason about. The caller passes a builder that constructs the
// { url, init } from the current state.
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
```

**Step 4: Run test to verify it passes**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/usePlaygroundChat.test.js 2>&1 | tail -10`
Expected: `# pass 5`, `# fail 0`.

**Step 5: Commit**

```bash
cd /home/bilfid/projects/nixllm
git add web/dashboard/src/playground/usePlaygroundChat.js web/dashboard/src/playground/usePlaygroundChat.test.js
git commit -m "feat(playground): add chat state reducer and SSE stream helper"
```

---

## Task 8: Wire sendMessage into the reducer

**Files:**
- Modify: `web/dashboard/src/playground/usePlaygroundChat.js` (add `useSendMessage` export)
- Test: `web/dashboard/src/playground/usePlaygroundChat.test.js` (add tests for the new function via direct invocation)

**Step 1: Write the failing test**

Append to `web/dashboard/src/playground/usePlaygroundChat.test.js`:

```js
// The sendMessage integration is tested by directly driving the
// exported openStream helper with a fake fetch — no React needed.
import { openStream } from './usePlaygroundChat.js';

test('openStream dispatches onChunk for each SSE event block', async () => {
  const events = [];
  const sseBody = 'data: {"choices":[{"delta":{"content":"hi"}}]}\n\ndata: [DONE]\n\n';
  const fakeBody = new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(sseBody));
      controller.close();
    },
  });
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => ({ ok: true, status: 200, body: fakeBody });
  try {
    await openStream({
      url: '/v1/chat/completions',
      init: { method: 'POST' },
      onChunk: (e) => events.push(e),
      onDone: () => events.push('__done__'),
      onError: (e) => events.push(`ERR:${e.message}`),
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
  assert.equal(events.length, 3);
  assert.match(events[0], /data: \{"choices":/);
  assert.equal(events[1], 'data: [DONE]');
  assert.equal(events[2], '__done__');
});

test('openStream reports a non-2xx as onError with status and body', async () => {
  let captured;
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => ({
    ok: false,
    status: 503,
    text: async () => 'upstream down',
  });
  try {
    await openStream({
      url: '/v1/chat/completions',
      init: {},
      onChunk: () => {},
      onDone: () => { captured = 'DONE'; },
      onError: (e) => { captured = e; },
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
  assert.equal(captured.status, 503);
  assert.equal(captured.type, 'upstream');
  assert.equal(captured.body, 'upstream down');
});
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/usePlaygroundChat.test.js 2>&1 | tail -10`
Expected: FAIL — `openStream is not a function` (or undefined import — the `usePlaygroundChat.js` from Task 7 only exports `usePlaygroundChat`).

Wait — Task 7 already exports `openStream`. If those tests pass in Task 7, these new ones should run. The reason they fail is that the existing exports don't include `openStream` *re-exported* for tests. Check:

Run: `grep -n "export" /home/bilfid/projects/nixllm/web/dashboard/src/playground/usePlaygroundChat.js`
Expected: shows `export function openStream` and `export const initialState`, `export function reducer`, `export function usePlaygroundChat`.

If `openStream` is already exported, the new tests should actually pass. Run them to confirm — if they pass, skip to Step 4 (commit only the test file).

Expected output of running: `# pass 7`, `# fail 0` — meaning openStream already works as designed.

**Step 3: (Only if Step 2 actually failed) Adjust the implementation**

No implementation change needed in the common case — `openStream` is already exported. If the tests fail, the most likely cause is a missing `signal` argument threading. Skip this step if Step 2 passed.

**Step 4: Run the broader playground test suite to confirm nothing regressed**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/**/*.test.js 2>&1 | tail -10`
Expected: All tests pass.

**Step 5: Commit**

```bash
cd /home/bilfid/projects/nixllm
git add web/dashboard/src/playground/usePlaygroundChat.test.js
git commit -m "test(playground): cover openStream SSE chunking and error paths"
```

---

## Task 9: Model picker — catalog + upstream tabs

**Files:**
- Create: `web/dashboard/src/playground/ModelPicker.jsx`
- Test: `web/dashboard/src/playground/ModelPicker.test.js` (smoke-render only — full React DOM via `react-dom/server` is overkill; use a shallow check by exporting a derived pure function alongside)

For testability, also export a `filterLiveModels(upstreams, health)` pure function in the same file.

**Step 1: Write the failing test**

Create `web/dashboard/src/playground/ModelPicker.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { filterLiveModels, modelHealthIsLive } from './ModelPicker.js';

test('modelHealthIsLive returns true for status healthy', () => {
  assert.equal(modelHealthIsLive({ status: 'healthy', consecutive_failures: 0 }), true);
});

test('modelHealthIsLive returns false for status down or consecutive_failures >= 3', () => {
  assert.equal(modelHealthIsLive({ status: 'healthy', consecutive_failures: 5 }), false);
  assert.equal(modelHealthIsLive({ status: 'down', consecutive_failures: 0 }), false);
});

test('filterLiveModels keeps only models with at least one healthy auth', () => {
  const upstreams = [
    { id: 'u1', model: 'gpt-4o', provider_type: 'openai' },
    { id: 'u2', model: 'gpt-4o', provider_type: 'openai' },
    { id: 'u3', model: 'claude-3-5-sonnet', provider_type: 'claude' },
  ];
  const health = [
    { model: 'gpt-4o', status: 'healthy', consecutive_failures: 0 },
    { model: 'gpt-4o', status: 'down', consecutive_failures: 4 },
    { model: 'claude-3-5-sonnet', status: 'down', consecutive_failures: 9 },
  ];
  const live = filterLiveModels(upstreams, health);
  // gpt-4o has at least one healthy auth; claude-3-5-sonnet has none.
  assert.deepEqual(live.map((u) => u.model).sort(), ['gpt-4o', 'gpt-4o']);
});
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/ModelPicker.test.js 2>&1 | tail -10`
Expected: FAIL — `Cannot find module`.

**Step 3: Write the implementation**

Create `web/dashboard/src/playground/ModelPicker.jsx`:

```jsx
import React, { useEffect, useMemo, useState } from 'react';
import { listModelsCatalog } from '../api/client.js';
import { listUpstreamProviders } from '../api/client.js';
import { getModelHealth } from '../api/client.js';

// Pure helpers (testable in isolation):

export function modelHealthIsLive(h) {
  if (!h) return false;
  if (h.status === 'down') return false;
  if ((h.consecutive_failures || 0) >= 3) return false;
  return true;
}

export function filterLiveModels(upstreams, healthRows) {
  const byModel = new Map();
  for (const h of healthRows || []) {
    if (modelHealthIsLive(h)) byModel.set(h.model, true);
  }
  return (upstreams || []).filter((u) => byModel.get(u.model));
}

export default function ModelPicker({ value, onChange }) {
  const [tab, setTab] = useState('catalog');
  const [catalog, setCatalog] = useState([]);
  const [upstreams, setUpstreams] = useState([]);
  const [health, setHealth] = useState([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [c, u, h] = await Promise.all([
          listModelsCatalog({ pageSize: 500 }),
          listUpstreamProviders({ providerType: '' }),
          getModelHealth().catch(() => []),
        ]);
        if (cancelled) return;
        setCatalog(c?.items || c?.models || c || []);
        setUpstreams(u?.items || u?.providers || u || []);
        setHealth(h?.items || h || []);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  const liveUpstreams = useMemo(() => filterLiveModels(upstreams, health), [upstreams, health]);

  return (
    <div className="space-y-2">
      <div className="flex gap-2 border-b border-border pb-1">
        <button onClick={() => setTab('catalog')} className={tab === 'catalog' ? 'font-semibold' : ''}>Catalog</button>
        <button onClick={() => setTab('upstream')} className={tab === 'upstream' ? 'font-semibold' : ''}>Upstream (LIVE)</button>
      </div>
      {loading ? (
        <div className="text-sm text-muted-foreground">Loading models…</div>
      ) : tab === 'catalog' ? (
        <CatalogList models={catalog} value={value} onChange={onChange} />
      ) : (
        <UpstreamList upstreams={liveUpstreams} value={value} onChange={onChange} />
      )}
    </div>
  );
}

function CatalogList({ models, value, onChange }) {
  return (
    <select
      className="w-full border rounded px-2 py-1 bg-background"
      value={value || ''}
      onChange={(e) => onChange(e.target.value)}
    >
      <option value="">Select a model…</option>
      {models.map((m) => (
        <option key={m.id || m.model} value={m.model || m.id}>
          {m.model || m.id} {m.provider ? `· ${m.provider}` : ''}
        </option>
      ))}
    </select>
  );
}

function UpstreamList({ upstreams, value, onChange }) {
  if (!upstreams.length) {
    return <div className="text-sm text-muted-foreground">No LIVE models detected.</div>;
  }
  return (
    <select
      className="w-full border rounded px-2 py-1 bg-background"
      value={value || ''}
      onChange={(e) => onChange(e.target.value)}
    >
      <option value="">Select a LIVE model…</option>
      {upstreams.map((u) => (
        <option key={u.id} value={u.model}>
          {u.model} · {u.provider_type}
        </option>
      ))}
    </select>
  );
}
```

**Step 4: Run test to verify it passes**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/ModelPicker.test.js 2>&1 | tail -10`
Expected: `# pass 3`, `# fail 0`.

**Step 5: Commit**

```bash
cd /home/bilfid/projects/nixllm
git add web/dashboard/src/playground/ModelPicker.jsx web/dashboard/src/playground/ModelPicker.test.js
git commit -m "feat(playground): add ModelPicker with catalog and LIVE upstream tabs"
```

---

## Task 10: Protocol auto-detect helper

**Files:**
- Create: `web/dashboard/src/playground/detectProtocol.js`
- Test: `web/dashboard/src/playground/detectProtocol.test.js`

**Step 1: Write the failing test**

Create `web/dashboard/src/playground/detectProtocol.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { detectProtocol, detectProtocolFromUpstream } from './detectProtocol.js';

test('detectProtocol maps gemini/Google models to gemini', () => {
  assert.equal(detectProtocol({ model: 'gemini-1.5-pro', provider: 'google' }), 'gemini');
});

test('detectProtocol maps claude models to claude', () => {
  assert.equal(detectProtocol({ model: 'claude-3-5-sonnet-20241022' }), 'claude');
});

test('detectProtocol maps gpt/responses models to openai-compat by default', () => {
  assert.equal(detectProtocol({ model: 'gpt-4o' }), 'openai-compat');
});

test('detectProtocolFromUpstream uses provider_type', () => {
  assert.equal(detectProtocolFromUpstream({ provider_type: 'claude' }), 'claude');
  assert.equal(detectProtocolFromUpstream({ provider_type: 'gemini' }), 'gemini');
  assert.equal(detectProtocolFromUpstream({ provider_type: 'openai' }), 'openai-compat');
  assert.equal(detectProtocolFromUpstream({ provider_type: 'openai-compatible' }), 'openai-compat');
});
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/detectProtocol.test.js 2>&1 | tail -10`
Expected: FAIL — module missing.

**Step 3: Write the implementation**

Create `web/dashboard/src/playground/detectProtocol.js`:

```js
// Protocol auto-detection from a model descriptor or upstream provider row.
// Used by the playground to pick a default protocol; the user can always
// override via the ProtocolSwitcher.

const GEMINI_RE = /gemini|^google(-|$)|\/models\/.*gemini/i;
const CLAUDE_RE = /claude/i;

export function detectProtocol(model) {
  const m = String(model?.model || model?.id || '');
  const provider = String(model?.provider || model?.provider_type || '');
  if (GEMINI_RE.test(m) || GEMINI_RE.test(provider)) return 'gemini';
  if (CLAUDE_RE.test(m) || CLAUDE_RE.test(provider)) return 'claude';
  return 'openai-compat';
}

export function detectProtocolFromUpstream(upstream) {
  const t = String(upstream?.provider_type || '').toLowerCase();
  if (t === 'gemini' || t === 'google') return 'gemini';
  if (t === 'claude' || t === 'anthropic') return 'claude';
  if (t === 'codex') return 'codex';
  return 'openai-compat';
}
```

**Step 4: Run test to verify it passes**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/detectProtocol.test.js 2>&1 | tail -10`
Expected: `# pass 4`, `# fail 0`.

**Step 5: Commit**

```bash
cd /home/bilfid/projects/nixllm
git add web/dashboard/src/playground/detectProtocol.js web/dashboard/src/playground/detectProtocol.test.js
git commit -m "feat(playground): add protocol auto-detect helper"
```

---

## Task 11: Conversation export

**Files:**
- Create: `web/dashboard/src/playground/exportConversation.js`
- Test: `web/dashboard/src/playground/exportConversation.test.js`

**Step 1: Write the failing test**

Create `web/dashboard/src/playground/exportConversation.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { toJsonExport, toMarkdownExport } from './exportConversation.js';

const messages = [
  { role: 'user', content: 'hi', model: 'gpt-4o', protocol: 'openai-compat' },
  { role: 'assistant', content: 'hello!', usage: { prompt_tokens: 3, completion_tokens: 2 } },
];

test('toJsonExport returns a string of JSON with full metadata', () => {
  const out = JSON.parse(toJsonExport(messages));
  assert.equal(out.length, 2);
  assert.equal(out[0].model, 'gpt-4o');
  assert.deepEqual(out[1].usage, { prompt_tokens: 3, completion_tokens: 2 });
});

test('toMarkdownExport renders user and assistant turns with headings', () => {
  const md = toMarkdownExport(messages);
  assert.match(md, /## User/);
  assert.match(md, /## Assistant/);
  assert.match(md, /hi/);
  assert.match(md, /hello!/);
});
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/exportConversation.test.js 2>&1 | tail -10`
Expected: FAIL — module missing.

**Step 3: Write the implementation**

Create `web/dashboard/src/playground/exportConversation.js`:

```js
// Conversation export helpers. Pure functions — the component layer
// calls these and triggers a Blob download.

export function toJsonExport(messages) {
  return JSON.stringify(messages, null, 2);
}

export function toMarkdownExport(messages) {
  const lines = ['# Playground Conversation', ''];
  for (const m of messages) {
    if (m.role === 'user') {
      lines.push('## User', '', m.content, '');
    } else if (m.role === 'assistant') {
      lines.push('## Assistant', '', m.content || '', '');
      if (m.usage) {
        lines.push(`_Usage: prompt ${m.usage.prompt_tokens ?? 0}, completion ${m.usage.completion_tokens ?? 0}_`, '');
      }
    }
  }
  return lines.join('\n');
}
```

**Step 4: Run test to verify it passes**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && node --test src/playground/exportConversation.test.js 2>&1 | tail -10`
Expected: `# pass 2`, `# fail 0`.

**Step 5: Commit**

```bash
cd /home/bilfid/projects/nixllm
git add web/dashboard/src/playground/exportConversation.js web/dashboard/src/playground/exportConversation.test.js
git commit -m "feat(playground): add conversation export (JSON + Markdown)"
```

---

## Task 12: Assemble PlaygroundPage

**Files:**
- Create: `web/dashboard/src/pages/PlaygroundPage.jsx`
- Modify: `web/dashboard/src/App.jsx` (add import + route)
- Modify: `web/dashboard/src/components/Sidebar.jsx` (add nav entry)

**Step 1: Add the sidebar entry**

Read `web/dashboard/src/components/Sidebar.jsx` to find an existing nav entry pattern. Add a new entry next to Model Health / Cooldown Providers using the same shape (icon, label, path). Use a `MessageSquare` or `FlaskConical` icon from whatever icon library is in use. The existing entries use `lucide-react` based on import patterns seen elsewhere — if not present, inline a simple SVG.

Example diff (verify exact strings with the file before editing):

```jsx
<NavLink to="/playground" icon={<PlayIcon />}>Playground</NavLink>
```

**Step 2: Add the route**

In `web/dashboard/src/App.jsx`, add:

```jsx
import PlaygroundPage from './pages/PlaygroundPage.jsx';
```

And inside the `<Routes>` block, add a new `<Route path="/playground" element={<PlaygroundPage />} />` next to the other top-level routes.

**Step 3: Create the page component**

Create `web/dashboard/src/pages/PlaygroundPage.jsx`:

```jsx
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getProtocol } from '../playground/protocols.js';
import { usePlaygroundKey } from '../playground/usePlaygroundKey.js';
import { usePlaygroundChat, openStream } from '../playground/usePlaygroundChat.js';
import ModelPicker from '../playground/ModelPicker.jsx';
import { detectProtocol } from '../playground/detectProtocol.js';
import { toJsonExport, toMarkdownExport } from '../playground/exportConversation.js';

export default function PlaygroundPage() {
  const { key, error: keyError, loading: keyLoading, refresh: refreshKey } = usePlaygroundKey();
  const [model, setModel] = useState('');
  const [protocolId, setProtocolId] = useState('openai-compat');
  const [params, setParams] = useState({ system: '', temperature: 0.7, max_tokens: 1024, top_p: 1, stream: true });
  const [compareModels, setCompareModels] = useState([]); // for v1, single-model only
  const { state, dispatch } = usePlaygroundChat();
  const abortRef = useRef(null);

  // Auto-detect protocol when model changes (unless user manually changed it).
  const userPickedProtocol = useRef(false);
  useEffect(() => {
    if (!model || userPickedProtocol.current) return;
    setProtocolId(detectProtocol({ model }));
  }, [model]);

  const send = useCallback(async () => {
    if (!key || !model || state.inFlight) return;
    const adapter = getProtocol(protocolId);
    const messages = [];
    if (params.system) messages.push({ role: 'system', content: params.system });
    for (const m of state.messages) messages.push({ role: m.role, content: m.content });
    messages.push({ role: 'user', content: '' }); // user text input
    // Actually, read the user input from a separate state — see below.
  }, [key, model, protocolId, params, state.inFlight, state.messages]);

  // For v1 we keep the user input in a local ref rather than dispatching
  // to the reducer before send.
  const [draft, setDraft] = useState('');

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
        // Split event into individual lines, feed each to the parser.
        for (const line of event.split('\n')) {
          if (!line.trim()) continue;
          const { token, done } = adapter.parseStreamChunk(line);
          if (token) dispatch({ type: 'TOKEN', id: reqId, token });
          if (done) {/* nothing extra; DONE comes from openStream end */}
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
        <ProtocolSwitcher value={protocolId} onChange={(v) => { userPickedProtocol.current = true; setProtocolId(v); }} />
        <ParamPanel params={params} onChange={setParams} />
        <ExportButton messages={state.messages} />
      </div>
      <div className="flex-1 flex flex-col">
        <div className="flex-1 overflow-y-auto p-4 space-y-3">
          {state.messages.map((m) => <MessageBubble key={m.id} m={m} />)}
          {state.error && (
            <div className="rounded border border-red-500 bg-red-50 dark:bg-red-950 p-3 text-sm">
              <div className="font-semibold">Error</div>
              <div>{state.error.message}</div>
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
          <button
            onClick={doSend}
            disabled={!draft.trim() || state.inFlight || !key || !model}
            className="px-4 py-2 rounded bg-primary text-primary-foreground disabled:opacity-50"
          >
            {state.inFlight ? 'Streaming…' : 'Send'}
          </button>
        </div>
      </div>
    </div>
  );
}

function authHeaderFor(protocolId, key) {
  switch (protocolId) {
    case 'gemini':
      return { 'x-goog-api-key': key };
    case 'claude':
      return { 'x-api-key': key, 'anthropic-version': '2023-06-01' };
    default:
      return { Authorization: `Bearer ${key}` };
  }
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
```

**Step 4: Run lint and build**

```bash
cd /home/bilfid/projects/nixllm/web/dashboard && npm run lint 2>&1 | tail -10
cd /home/bilfid/projects/nixllm/web/dashboard && npm run build 2>&1 | tail -10
```

Expected: `npm run lint` reports 0 errors. `npm run build` produces `dist/` with no errors.

**Step 5: Smoke-test the page exists**

Run: `ls /home/bilfid/projects/nixllm/web/dashboard/src/pages/PlaygroundPage.jsx && grep -n '"/playground"' /home/bilfid/projects/nixllm/web/dashboard/src/App.jsx`
Expected: file exists and route is present.

**Step 6: Run all dashboard tests**

Run: `cd /home/bilfid/projects/nixllm/web/dashboard && npm test 2>&1 | tail -10`
Expected: all tests pass (existing + new playground suite).

**Step 7: Commit**

```bash
cd /home/bilfid/projects/nixllm
git add web/dashboard/src/pages/PlaygroundPage.jsx web/dashboard/src/App.jsx web/dashboard/src/components/Sidebar.jsx
git commit -m "feat(playground): assemble PlaygroundPage and wire route + sidebar"
```

---

## Task 13: Embed in Go binary via `make dash-embed`

**Files:** none — verification only.

**Step 1: Run the embed target**

```bash
cd /home/bilfid/projects/nixllm && make dash-embed 2>&1 | tail -20
```

Expected: `vite build` runs, then Go rebuilds. No errors. The `internal/dashboardasset/dist` directory is repopulated.

**Step 2: Verify the new page is in the dist**

Run: `ls /home/bilfid/projects/nixllm/internal/dashboardasset/dist/assets/ 2>&1 | head -5`
Expected: a chunk referencing `PlaygroundPage` is present (search for it):

Run: `grep -l "playground" /home/bilfid/projects/nixllm/internal/dashboardasset/dist/assets/*.js 2>&1 | head -3`
Expected: at least one asset contains "playground".

**Step 3: Run Go formatting check**

```bash
gofmt -l /home/bilfid/projects/nixllm 2>&1
```

Expected: no output (no files need formatting).

**Step 4: Verify Go build is clean**

```bash
go build -o /tmp/nixllm-build /home/bilfid/projects/nixllm/cmd/server && rm /tmp/nixllm-build
```

Expected: success, no errors.

**Step 5: Commit any embed-related changes**

```bash
cd /home/bilfid/projects/nixllm
git add -A internal/dashboardasset/ 2>/dev/null
git status --short internal/dashboardasset/
```

Expected: if `internal/dashboardasset/` is gitignored, the embed was rebuilt but not committed — that's the repo's existing pattern (see how prior dashboard updates were handled). If anything *did* change, commit it.

---

## Task 14: End-to-end smoke (manual)

This task is operator-driven — it requires a running NixLLM with at least one LIVE auth and a real model upstream. Skip in CI.

**Steps:**

1. Start the dev server: `cd /home/bilfid/projects/nixllm && go run ./cmd/server --no-browser`
2. Start the dashboard: `cd /home/bilfid/projects/nixllm/web/dashboard && npm run dev`
3. Open `http://localhost:9173`, log in with `MANAGEMENT_PASSWORD`.
4. Navigate to **Playground** in the sidebar.
5. Pick a model from the Upstream (LIVE) tab.
6. Type a message, click **Send**, verify streaming renders.
7. Switch protocol manually via `<ProtocolSwitcher>`, send again, verify it hits the right endpoint.
8. Send a follow-up message, verify the assistant sees the prior turn.
9. Click **Markdown** export, verify the file downloads.
10. Open the Raw Inspector drawer (next step would add it; for v1 the inspector ships as the `state.error` + per-message model/protocol labels; the full drawer is YAGNI for v1 — see Task 15).

If anything fails, file a follow-up — do not skip the manual smoke.

**Step: Commit smoke notes (optional)**

```bash
cd /home/bilfid/projects/nixllm
# No automated commit; if notes were taken, add to docs/playground.md
```

---

## Task 15: (Out-of-scope for v1) Raw inspector drawer

The design calls for a `<RawInspectorDrawer>` showing the full outgoing/incoming JSON. This is deferred from v1 because:
- The fetch + dispatch pipeline already snapshots outgoing bodies implicitly (via the `body` variable passed to `fetch`).
- The `state.error.body` already captures upstream failure payloads.
- A proper inspector needs a collapsible JSON viewer component (e.g. `react-json-view`), which would be a new dep.

Track in `docs/plans/2026-08-29-playground-feature-design.md` under a "Deferred from v1" section if a follow-up is desired. The page works without it; the operator gets protocol + status code + error message from the existing UI.

No code in this plan. No commit.

---

## Task 16: Final verification

**Step 1: Run all dashboard tests**

```bash
cd /home/bilfid/projects/nixllm/web/dashboard && npm test 2>&1 | tail -10
```

Expected: all pass.

**Step 2: Run lint**

```bash
cd /home/bilfid/projects/nixllm/web/dashboard && npm run lint 2>&1 | tail -10
```

Expected: 0 errors.

**Step 3: Run Go build**

```bash
cd /home/bilfid/projects/nixllm && go build -o /tmp/nixllm-build ./cmd/server && rm /tmp/nixllm-build
```

Expected: success.

**Step 4: Run `gofmt -l .`**

```bash
gofmt -l /home/bilfid/projects/nixllm
```

Expected: empty output.

**Step 5: Check git status is clean (excluding ignored files)**

```bash
cd /home/bilfid/projects/nixllm && git status --short
```

Expected: only `payload.json` (untracked) and any auto-generated embed artifacts. No unstaged playground files.

**Step 6: Commit any final cleanup**

```bash
cd /home/bilfid/projects/nixllm
git add -A 2>/dev/null || true
git status --short
# If only embed artifacts and tests are listed, commit them:
git commit -m "chore(playground): post-build cleanup" || true
```

---

## Definition of done

- [ ] All 4 protocol adapters pass unit tests (Tasks 2-5)
- [ ] `usePlaygroundKey` tested for cache-hit, create, and 409-fallback (Task 6)
- [ ] `usePlaygroundChat` reducer tested for SEND/TOKEN/DONE/ERROR (Task 7)
- [ ] `openStream` SSE helper tested for chunking and error paths (Task 8)
- [ ] `ModelPicker` filter helpers tested (Task 9)
- [ ] `detectProtocol` tested (Task 10)
- [ ] `exportConversation` tested (Task 11)
- [ ] `PlaygroundPage` renders, routes, builds, lints (Task 12)
- [ ] `make dash-embed` succeeds and Playground chunk is in dist (Task 13)
- [ ] Manual smoke against a real LIVE model passes (Task 14)
- [ ] All tests pass, lint clean, Go builds clean (Task 16)
- [ ] No new Go code; existing `gofmt` + `go build` still pass
