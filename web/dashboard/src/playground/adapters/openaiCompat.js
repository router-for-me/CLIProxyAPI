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
    // Ask the upstream to emit a final usage frame while streaming so the
    // playground can show real token counts. Proxies that don't understand
    // the option ignore it; the footer degrades to em-dashes.
    if (params.stream) body.stream_options = { include_usage: true };
    return body;
  },

  parseStreamChunk(line) {
    if (!line.startsWith('data:')) return { token: '', done: false };
    const payload = line.slice(5).trim();
    if (payload === '[DONE]') return { token: '', done: true };
    try {
      const json = JSON.parse(payload);
      const token = json?.choices?.[0]?.delta?.content ?? '';
      const usage = normalizeUsage(json?.usage);
      return { token: token || '', done: false, ...(usage ? { usage } : {}) };
    } catch {
      return { token: '', done: false };
    }
  },

  // Non-streaming responses arrive as a single JSON body. The chat layer
  // feeds the parsed object here so text + usage are extracted uniformly.
  parseResponse(json) {
    const text = json?.choices?.[0]?.message?.content ?? '';
    return { text: text || '', usage: normalizeUsage(json?.usage) };
  },

  buildErrorPayload(err) {
    if (err && err.status) {
      return { message: err.message || 'Request failed', type: 'upstream', status: err.status };
    }
    return { message: String(err), type: 'network' };
  },
};

// normalizeUsage maps an OpenAI usage object onto the canonical shape.
export function normalizeUsage(u) {
  if (!u || typeof u !== 'object') return null;
  const prompt = Number(u.prompt_tokens);
  const completion = Number(u.completion_tokens);
  if (!Number.isFinite(prompt) && !Number.isFinite(completion) && !Number.isFinite(Number(u.total_tokens))) {
    return null;
  }
  const out = {};
  if (Number.isFinite(prompt)) out.prompt_tokens = prompt;
  if (Number.isFinite(completion)) out.completion_tokens = completion;
  if (Number.isFinite(Number(u.total_tokens))) out.total_tokens = Number(u.total_tokens);
  return out;
}
