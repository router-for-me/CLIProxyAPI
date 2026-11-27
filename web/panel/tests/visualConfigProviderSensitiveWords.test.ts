import { describe, expect, test } from 'bun:test';
import { parse as parseYaml, stringify } from 'yaml';
import { buildConfigPatch, ConfigDraftConflictError } from '@/services/api/configPatch';
import { runVisualConfig } from './helpers/visualConfig';

const providers = [
  ['antigravity', 'devin'],
  ['devin', 'antigravity'],
] as const;

describe('visual config provider sensitive words', () => {
  for (const [provider, other] of providers) {
    const field = `${provider}SensitiveWords` as const;
    const otherField = `${other}SensitiveWords` as const;

    test(`${provider}: loads sensitive-words independently from ${other}`, () => {
      const yaml = stringify({
        oauth: {
          providers: {
            [provider]: { 'sensitive-words': ['Claude Code', 'security testing'] },
            [other]: { 'sensitive-words': ['proxy'] },
          },
        },
      });
      const { visualValues } = runVisualConfig(yaml);
      expect(visualValues[field]).toEqual(['Claude Code', 'security testing']);
      expect(visualValues[otherField]).toEqual(['proxy']);
    });

    test(`${provider}: writes trimmed values preserving unknown keys, comments, and ${other}`, () => {
      const yaml = `# root comment
oauth:
  providers:
    ${provider}:
      # list comment
      sensitive-words:
        # retained item comment
        - old-word # inline item note
      # unknown comment
      future-option: true
    ${other}:
      sensitive-words:
        - proxy
`;
      const config = runVisualConfig(yaml, [{ [field]: [' old-word ', '', 'new phrase'] }]);
      const output = config.applyVisualChangesToYaml(yaml);
      expect(parseYaml(output)).toEqual({
        oauth: {
          providers: {
            [provider]: { 'sensitive-words': ['old-word', 'new phrase'], 'future-option': true },
            [other]: { 'sensitive-words': ['proxy'] },
          },
        },
      });
      for (const comment of ['root', 'list', 'unknown']) {
        expect(output).toContain(`# ${comment} comment`);
      }
      expect(output).toContain('# retained item comment');
      expect(output).toContain('- old-word # inline item note');
      // Source mode can save this YAML. The visual JSON operation cannot carry those comments.
      expect(() => buildConfigPatch(yaml, output)).toThrow(
        new ConfigDraftConflictError(
          ['oauth', 'providers', provider, 'sensitive-words'],
          'sensitive_word_comments'
        )
      );
    });

    test(`${provider}: blocks retained comments but not unrelated edits or removed comments`, () => {
      const yaml = `server: {port: 8317}
oauth:
  providers:
    ${provider}:
      sensitive-words:
        - ''
        - apple # apple note
        - pear # pear note
`;
      const draft = (words: string[]) =>
        runVisualConfig(yaml, [{ [field]: words }]).applyVisualChangesToYaml(yaml);
      const retained = draft(['pear', 'banana']);
      expect(retained).toContain('- pear # pear note');
      expect(retained).not.toContain('# apple note');
      expect(() => buildConfigPatch(yaml, retained)).toThrow(ConfigDraftConflictError);
      expect(buildConfigPatch(yaml, yaml.replace('8317', '9000')).patch).toEqual({
        server: { port: 9000 },
      });
      expect(buildConfigPatch(yaml, draft(['banana'])).patch).toEqual({
        oauth: { providers: { [provider]: { 'sensitive-words': ['banana'] } } },
      });
    });

    test(`${provider}: clearing removes the key and an otherwise empty block`, () => {
      const yaml = stringify({
        oauth: { providers: { [provider]: { 'sensitive-words': ['remove-me'] } } },
        observability: { logs: { debug: true } },
      });
      const config = runVisualConfig(yaml, [{ [field]: [] }]);
      expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
        observability: { logs: { debug: true } },
      });
    });

    test(`${provider}: restoring cancels dirty state and never marks ${other} dirty`, () => {
      const yaml = stringify({
        oauth: {
          providers: {
            [provider]: { 'sensitive-words': ['original'] },
            [other]: { 'sensitive-words': ['untouched'] },
          },
        },
      });
      const config = runVisualConfig(yaml, [{ [field]: ['changed'] }, { [field]: ['original'] }]);
      expect(config.visualDirty).toBe(false);
      expect([...config.visualDirtyFields]).toEqual([]);
      expect(config.applyVisualChangesToYaml(yaml)).toBe(yaml);
    });
  }
});
