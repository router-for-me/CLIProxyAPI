import { describe, expect, test } from 'bun:test';
import { parse as parseYaml, parseDocument } from 'yaml';
import {
  buildConfigSaveDraft,
  shouldReloadVisualDraft,
} from '../src/features/config/hooks/useConfigDocument';
import { buildConfigPatch } from '../src/services/api/configPatch';

function applyProxyEdit(yaml: string): string {
  const document = parseDocument(yaml);
  document.setIn(['requests', 'proxy-url'], 'http://local-proxy.example');
  return document.toString();
}

describe('config document concurrency policy', () => {
  test('a visual/source round trip preserves field dirty state and patches only local edits', () => {
    const synchronizedSource =
      'requests:\n  proxy-url: http://local-proxy.example\nobservability:\n  logs:\n    debug: false\n';
    const latestServer =
      'requests:\n  proxy-url: http://old-proxy.example\nobservability:\n  logs:\n    debug: true\n';

    expect(shouldReloadVisualDraft(false, null)).toBe(false);
    const merged = buildConfigSaveDraft(
      latestServer,
      synchronizedSource,
      false,
      'visual',
      applyProxyEdit
    );
    expect(parseYaml(merged)).toEqual({
      observability: { logs: { debug: true } },
      requests: { 'proxy-url': 'http://local-proxy.example' },
    });
    expect(buildConfigPatch(latestServer, merged)).toEqual({
      patch: { requests: { 'proxy-url': 'http://local-proxy.example' } },
      deletions: [],
    });
  });

  test('real source edits cannot be submitted from visual mode (decision A)', () => {
    expect(() =>
      buildConfigSaveDraft(
        'server: {port: 8317}',
        'server: {port: 9000}',
        true,
        'visual',
        applyProxyEdit
      )
    ).toThrow('Unsaved source edits');
  });

  test('source saves preserve the complete draft, including comments, without rebuilding it', () => {
    const source = '# intentional source formatting\nserver: {port: 9000}\n';
    expect(
      buildConfigSaveDraft('server: {port: 8317}', source, true, 'source', () => {
        throw new Error('Do not apply visual values to a source draft');
      })
    ).toBe(source);
  });

  test.each(['\n', '\r\n'])('source edits preserve server line endings (%j)', (eol) => {
    const lines = Array.from({ length: 165 }, (_, i) => `key${i}: ${i}`);
    const server = lines.join(eol);
    const save = (draft: string) =>
      buildConfigSaveDraft(server, draft, true, 'source', applyProxyEdit);

    expect(save(lines.join('\n'))).toBe(server); // An edit followed by undo is a no-op.
    lines.splice(103, 0, ...Array.from({ length: 13 }, (_, i) => `extra${i}: true`));
    expect(save(lines.join('\n'))).toBe(lines.join(eol));
    expect(save(lines.join('\n') + '\n')).toBe(lines.join(eol) + eol);
  });

  test('viewing generated source still builds on latest server values before a full source save', () => {
    const result = buildConfigSaveDraft(
      'observability:\n  logs:\n    debug: true\n',
      'observability:\n  logs:\n    debug: false\n',
      false,
      'source',
      applyProxyEdit
    );
    expect(parseYaml(result)).toEqual({
      observability: { logs: { debug: true } },
      requests: { 'proxy-url': 'http://local-proxy.example' },
    });
  });

  test('retries parsing after a YAML error even without a source edit', () => {
    expect(shouldReloadVisualDraft(false, 'Invalid YAML')).toBe(true);
  });
});
