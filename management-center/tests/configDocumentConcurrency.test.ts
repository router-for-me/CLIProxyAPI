import { describe, expect, test } from 'bun:test';
import { parse as parseYaml, parseDocument } from 'yaml';
import {
  buildConfigSaveDraft,
  selectVisualMergeBase,
  shouldReloadVisualDraft,
} from '../src/features/config/hooks/useConfigDocument';

function applyProxyEdit(yaml: string): string {
  const document = parseDocument(yaml);
  document.set('proxy-url', 'http://local-proxy.example');
  return document.toString();
}

describe('config document concurrency policy', () => {
  test('a visual/source mode round trip keeps merging visual edits onto the latest server YAML', () => {
    const synchronizedSource =
      'debug: false\nproxy-url: http://local-proxy.example\nsource-only: draft\n';
    const latestServer = 'debug: true\nproxy-url: http://old-proxy.example\nsource-only: server\n';

    expect(shouldReloadVisualDraft(false, null)).toBe(false);

    const merged = applyProxyEdit(selectVisualMergeBase(latestServer, synchronizedSource, false));
    expect(parseYaml(merged)).toEqual({
      debug: true,
      'proxy-url': 'http://local-proxy.example',
      'source-only': 'server',
    });
  });

  test('a real source edit keeps the local draft as the visual merge base', () => {
    const sourceDraft =
      'debug: false\nproxy-url: http://old-proxy.example\nsource-only: local-draft\n';
    const latestServer = 'debug: true\nproxy-url: http://old-proxy.example\nsource-only: server\n';

    expect(shouldReloadVisualDraft(true, null)).toBe(true);

    const merged = applyProxyEdit(selectVisualMergeBase(latestServer, sourceDraft, true));
    expect(parseYaml(merged)).toEqual({
      debug: false,
      'proxy-url': 'http://local-proxy.example',
      'source-only': 'local-draft',
    });
  });

  test('visual edits after a real source edit are applied on top of the source draft', () => {
    const source = 'debug: false\nsource-only: local\n';
    const server = 'debug: true\nsource-only: server\n';
    const result = buildConfigSaveDraft(server, source, true, 'visual', applyProxyEdit);
    expect(parseYaml(result)).toEqual({
      debug: false,
      'source-only': 'local',
      'proxy-url': 'http://local-proxy.example',
    });
    expect(buildConfigSaveDraft(server, source, true, 'source', applyProxyEdit)).toBe(source);
  });

  test('viewing generated source retains the visual-origin save strategy', () => {
    const result = buildConfigSaveDraft(
      'debug: true\n',
      'debug: false\n',
      false,
      'source',
      applyProxyEdit
    );
    expect(parseYaml(result)).toEqual({
      debug: true,
      'proxy-url': 'http://local-proxy.example',
    });
  });

  test('retries parsing after a YAML error even without a source edit', () => {
    expect(shouldReloadVisualDraft(false, 'Invalid YAML')).toBe(true);
  });
});
