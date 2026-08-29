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
