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
      // Usage arrives split across frames: input tokens on message_start,
      // output tokens on message_delta. mergeUsage in the chat layer folds
      // these partials together.
      const usage = claudeUsage(json);
      if (eventMatch?.[1] === 'content_block_delta') {
        return { token: json?.delta?.text ?? '', done: false, ...(usage ? { usage } : {}) };
      }
      if (json?.type === 'message_stop') return { token: '', done: true, ...(usage ? { usage } : {}) };
      return { token: '', done: false, ...(usage ? { usage } : {}) };
    } catch {
      return { token: '', done: false };
    }
  },

  // Non-streaming Messages response.
  parseResponse(json) {
    const blocks = json?.content || [];
    const text = blocks.map((b) => (typeof b?.text === 'string' ? b.text : '')).join('');
    return { text, usage: claudeUsage(json) };
  },

  buildErrorPayload(err) {
    if (err && err.status) {
      return { message: err.message || 'Request failed', type: 'upstream', status: err.status };
    }
    return { message: String(err), type: 'network' };
  },
};

// claudeUsage extracts the canonical usage fields from any Claude frame that
// carries them (message_start → input, message_delta → output).
function claudeUsage(json) {
  const u = json?.usage || json?.message?.usage;
  if (!u || typeof u !== 'object') return null;
  const out = {};
  if (Number.isFinite(Number(u.input_tokens))) out.prompt_tokens = Number(u.input_tokens);
  if (Number.isFinite(Number(u.output_tokens))) out.completion_tokens = Number(u.output_tokens);
  return Object.keys(out).length ? out : null;
}
