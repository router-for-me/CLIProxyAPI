export const claudeAdapter = {
  id: 'claude',
  endpoint: () => '/v1/messages',
  buildRequest: () => ({}),
  parseStreamChunk: () => ({ token: '', done: true }),
  buildErrorPayload: (err) => ({ message: String(err), type: 'error' }),
  supports: { streaming: true, tools: true, vision: true, system_prompt: true },
};
