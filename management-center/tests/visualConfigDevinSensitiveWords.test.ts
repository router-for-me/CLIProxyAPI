import { describe, expect, test } from 'bun:test';
import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { parse as parseYaml } from 'yaml';
import { useVisualConfig } from '../src/hooks/useVisualConfig';

function unwrapPre(markup: string): string {
  return markup.slice('<pre>'.length, -'</pre>'.length);
}

describe('visual config Devin sensitive words', () => {
  test('loads devin.sensitive-words independently from Antigravity', () => {
    function Harness() {
      const visualConfig = useVisualConfig();
      const [loaded, setLoaded] = useState(false);

      if (!loaded) {
        visualConfig.loadVisualValuesFromYaml(
          'devin:\n  sensitive-words:\n    - Claude Code\n    - security testing\n' +
            'antigravity:\n  sensitive-words:\n    - proxy\n'
        );
        setLoaded(true);
        return null;
      }

      return createElement(
        'pre',
        null,
        `${visualConfig.visualValues.devinSensitiveWords.join('|')}::${visualConfig.visualValues.antigravitySensitiveWords.join('|')}`
      );
    }

    expect(unwrapPre(renderToStaticMarkup(createElement(Harness)))).toBe(
      'Claude Code|security testing::proxy'
    );
  });

  test('writes trimmed values while preserving unknown keys, comments, and Antigravity', () => {
    const yaml =
      '# root comment\n' +
      'devin:\n' +
      '  # list comment\n' +
      '  sensitive-words:\n' +
      '    # retained item comment\n' +
      '    - old-word\n' +
      '  # unknown comment\n' +
      '  future-option: true\n' +
      'antigravity:\n' +
      '  sensitive-words:\n' +
      '    - proxy\n';

    function Harness() {
      const visualConfig = useVisualConfig();
      const [phase, setPhase] = useState(0);

      if (phase === 0) {
        visualConfig.loadVisualValuesFromYaml(yaml);
        setPhase(1);
      } else if (phase === 1) {
        visualConfig.setVisualValues({ devinSensitiveWords: [' old-word ', '', 'new phrase'] });
        setPhase(2);
      } else {
        return createElement('pre', null, visualConfig.applyVisualChangesToYaml(yaml));
      }

      return null;
    }

    const output = unwrapPre(renderToStaticMarkup(createElement(Harness)));
    expect(parseYaml(output)).toEqual({
      devin: {
        'sensitive-words': ['old-word', 'new phrase'],
        'future-option': true,
      },
      antigravity: { 'sensitive-words': ['proxy'] },
    });
    expect(output).toContain('# root comment');
    expect(output).toContain('# list comment');
    expect(output).toContain('# retained item comment');
    expect(output).toContain('# unknown comment');
  });

  test('clears the sensitive-words key and removes an otherwise empty Devin block', () => {
    const yaml = 'debug: true\ndevin:\n  sensitive-words:\n    - remove-me\n';

    function Harness() {
      const visualConfig = useVisualConfig();
      const [phase, setPhase] = useState(0);

      if (phase === 0) {
        visualConfig.loadVisualValuesFromYaml(yaml);
        setPhase(1);
      } else if (phase === 1) {
        visualConfig.setVisualValues({ devinSensitiveWords: [] });
        setPhase(2);
      } else {
        return createElement('pre', null, visualConfig.applyVisualChangesToYaml(yaml));
      }

      return null;
    }

    expect(parseYaml(unwrapPre(renderToStaticMarkup(createElement(Harness))))).toEqual({
      debug: true,
    });
  });

  test('cancels dirty state when restored and never marks Antigravity dirty', () => {
    const yaml =
      'devin:\n  sensitive-words:\n    - original\n' +
      'antigravity:\n  sensitive-words:\n    - untouched\n';

    function Harness() {
      const visualConfig = useVisualConfig();
      const [phase, setPhase] = useState(0);

      if (phase === 0) {
        visualConfig.loadVisualValuesFromYaml(yaml);
        setPhase(1);
      } else if (phase === 1) {
        visualConfig.setVisualValues({ devinSensitiveWords: ['changed'] });
        setPhase(2);
      } else if (phase === 2) {
        visualConfig.setVisualValues({ devinSensitiveWords: ['original'] });
        setPhase(3);
      } else {
        const output = visualConfig.applyVisualChangesToYaml(yaml);
        return createElement(
          'pre',
          null,
          `${visualConfig.visualDirty}|${[...visualConfig.visualDirtyFields].join(',')}|${output === yaml}`
        );
      }

      return null;
    }

    expect(unwrapPre(renderToStaticMarkup(createElement(Harness)))).toBe('false||true');
  });
});
