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
