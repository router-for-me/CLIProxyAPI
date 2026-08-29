// Protocol adapter registry.
//
// Each protocol owns its wire format (endpoint URL, request body shape,
// stream chunk parsing, error normalization). The chat pipeline stays
// protocol-agnostic — it calls adapter.buildRequest() and
// adapter.parseStreamChunk() and never branches on protocol id.
//
// Adapters are pure functions of their inputs; no network or DOM access.

import { openaiCompatAdapter } from './adapters/openaiCompat.js';
import { geminiAdapter } from './adapters/gemini.js';
import { claudeAdapter } from './adapters/claude.js';
import { codexAdapter } from './adapters/codex.js';

const ADAPTERS = [openaiCompatAdapter, geminiAdapter, claudeAdapter, codexAdapter];

export function listProtocols() {
  return ADAPTERS.slice();
}

export function getProtocol(id) {
  const adapter = ADAPTERS.find((a) => a.id === id);
  if (!adapter) throw new Error(`Unknown protocol: ${id}`);
  return adapter;
}
