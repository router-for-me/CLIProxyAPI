// Write T3 Code's provider instances, defaults and hub usage source on auto-vps (docs/t3-code.md).
// Idempotent. Never prints a key. Run on the T3 host:
//   T3_TOKEN=$(t3 auth session issue --label t3-hub-config --ttl 15m --scope settings:write --scope providers:manage --token-only 2>/dev/null | grep -E '^[A-Za-z0-9._~+/=-]{40,}$') \
//     bwrun HUBKEY=AUT_HUB_ACCESS_KEY MGMT=AUT_HUB_MANAGEMENT_KEY -- node docs/estate-hub-devices/t3-hub-instances.mjs
// then revoke that session (`t3 auth session list`, `t3 auth session revoke <id>`).
// A providerInstances patch replaces the whole map: every instance T3 should keep must be listed here.
const H = process.env.HOME;
const HUB = 'https://waseems-mac-mini-m6.taild6c90e.ts.net:8317';
const { T3_TOKEN, HUBKEY, MGMT } = process.env;
if (!T3_TOKEN || !HUBKEY || !MGMT) throw new Error('need T3_TOKEN, HUBKEY and MGMT in the environment');

const env = (name, value, sensitive = false) => ({ name, value, sensitive });
const key = env('HUB_ACCESS_KEY', HUBKEY, true);
const claude = (extra = []) => [env('ANTHROPIC_BASE_URL', HUB), env('ANTHROPIC_AUTH_TOKEN', HUBKEY, true), env('ANTHROPIC_API_KEY', ''), ...extra];
const claudeConfig = { homePath: `${H}/.config/t3-hub/claude` };

const patch = {
  providerInstances: {
    claudeAgent: { driver: 'claudeAgent', enabled: false },
    codex: { driver: 'codex', enabled: false },
    cursor: { driver: 'cursor', enabled: false },
    'claude-hub': { driver: 'claudeAgent', displayName: 'Claude (hub)', enabled: true, environment: claude(), config: claudeConfig },
    'claude-hub-pinned': { driver: 'claudeAgent', displayName: 'Claude (hub, pinned)', enabled: true,
      environment: claude([env('ANTHROPIC_CUSTOM_HEADERS', 'X-CPA-Account: 1')]), config: claudeConfig },
    'codex-hub': { driver: 'codex', displayName: 'Codex (hub)', enabled: true, environment: [key],
      config: { setupMode: 'existing', homePath: `${H}/.config/t3-hub/codex`, customModels: ['gpt-6.1-terra'] } },
    'pi-hub': { driver: 'pi', displayName: 'Pi (hub)', enabled: true, environment: [env('PI_CODING_AGENT_DIR', `${H}/.config/t3-hub/pi`), key] },
    opencode: { driver: 'opencode', enabled: true, environment: [env('OPENCODE_CONFIG', `${H}/.config/t3-hub/opencode/opencode.json`), key] },
    grok: { driver: 'grok', enabled: true },
    muse: { driver: 'muse', enabled: true },
    antigravity: { driver: 'antigravity', enabled: true },
  },
  defaultRuntimeMode: 'full-access',
  defaultModelSelection: { instanceId: 'claude-hub', model: 'claude-opus-5-5' },
  textGenerationModelSelection: { instanceId: 'codex-hub', model: 'gpt-6-luna', options: [{ id: 'reasoningEffort', value: 'low' }] },
  usageLimitSources: { hub: { kind: 'cliproxy', label: 'Estate hub', url: HUB, managementKey: MGMT, enabled: true } },
};

const ws = new WebSocket('ws://127.0.0.1:3773/ws?orchestrationProtocol=2', { headers: { Authorization: `Bearer ${T3_TOKEN}` } });
setTimeout(() => { console.error('timed out'); process.exit(2); }, 30000);
ws.onopen = () => ws.send(JSON.stringify({ _tag: 'Request', id: '1', tag: 'server.updateSettings', payload: { patch }, headers: [] }));
ws.onerror = () => { console.error('cannot reach T3 on 127.0.0.1:3773'); process.exit(3); };
ws.onmessage = (e) => {
  const m = JSON.parse(e.data);
  if (m._tag === 'Ping') return ws.send(JSON.stringify({ _tag: 'Pong' }));
  if (m.requestId !== '1') return;
  const ok = m.exit?._tag === 'Success';
  console.log(ok ? `saved: ${Object.keys(m.exit.value.providerInstances).join(', ')}` : `failed: ${JSON.stringify(m.exit?.cause ?? m).slice(0, 500)}`);
  process.exit(ok ? 0 : 1);
};
