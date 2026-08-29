export const openaiCompatAdapter = {
  id: 'openai-compat',
  endpoint: () => '/v1/chat/completions',
  buildRequest: () => ({}),
  parseStreamChunk: () => ({ token: '', done: true }),
  buildErrorPayload: (err) => ({ message: String(err), type: 'error' }),
  supports: { streaming: true, tools: true, vision: true, system_prompt: true },
};
