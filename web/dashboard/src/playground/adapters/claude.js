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
