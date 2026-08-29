// Protocol auto-detection from a model descriptor or upstream provider row.
// Used by the playground to pick a default protocol; the user can always
// override via the ProtocolSwitcher.

const GEMINI_RE = /gemini|^google(-|$)|\/models\/.*gemini/i;
const CLAUDE_RE = /claude/i;

export function detectProtocol(model) {
  const m = String(model?.model || model?.id || '');
  const provider = String(model?.provider || model?.provider_type || '');
  if (GEMINI_RE.test(m) || GEMINI_RE.test(provider)) return 'gemini';
  if (CLAUDE_RE.test(m) || CLAUDE_RE.test(provider)) return 'claude';
  return 'openai-compat';
}

export function detectProtocolFromUpstream(upstream) {
  const t = String(upstream?.provider_type || '').toLowerCase();
  if (t === 'gemini' || t === 'google') return 'gemini';
  if (t === 'claude' || t === 'anthropic') return 'claude';
  if (t === 'codex') return 'codex';
  return 'openai-compat';
}
