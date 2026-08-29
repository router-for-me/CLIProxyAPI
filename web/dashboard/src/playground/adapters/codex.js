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
