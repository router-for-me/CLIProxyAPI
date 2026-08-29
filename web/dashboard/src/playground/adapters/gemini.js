export const geminiAdapter = {
  id: 'gemini',
  endpoint: (m) => `/v1beta/models/${m}:generateContent`,
  buildRequest: () => ({}),
  parseStreamChunk: () => ({ token: '', done: true }),
  buildErrorPayload: (err) => ({ message: String(err), type: 'error' }),
  supports: { streaming: true, tools: true, vision: true, system_prompt: true },
};
