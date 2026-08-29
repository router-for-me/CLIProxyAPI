export const codexAdapter = {
  id: 'codex',
  endpoint: () => '/v1/responses',
  buildRequest: () => ({}),
  parseStreamChunk: () => ({ token: '', done: true }),
  buildErrorPayload: (err) => ({ message: String(err), type: 'error' }),
  supports: { streaming: true, tools: true, vision: false, system_prompt: false },
};
