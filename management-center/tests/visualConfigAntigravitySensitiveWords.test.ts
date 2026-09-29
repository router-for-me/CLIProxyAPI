import { describe, expect, test } from 'bun:test';
import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { parse as parseYaml } from 'yaml';
import { useVisualConfig } from '../src/hooks/useVisualConfig';

function unwrapPre(markup: string): string {
  return markup.slice('<pre>'.length, -'</pre>'.length);
}

describe('visual config Antigravity sensitive words', () => {
  test('loads antigravity.sensitive-words from YAML', () => {
    function Harness() {
      const visualConfig = useVisualConfig();
      const [loaded, setLoaded] = useState(false);

      if (!loaded) {
        visualConfig.loadVisualValuesFromYaml(
          'antigravity:\n  sensitive-words:\n    - Hermes\n    - Nous Research\n'
        );
        setLoaded(true);
        return null;
      }

      return createElement(
        'pre',
        null,
        visualConfig.visualValues.antigravitySensitiveWords.join('|')
      );
    }

    const markup = renderToStaticMarkup(createElement(Harness));
    expect(unwrapPre(markup)).toBe('Hermes|Nous Research');
  });

  test('writes trimmed values and preserves other Antigravity settings', () => {
    function Harness() {
      const visualConfig = useVisualConfig();
      const [phase, setPhase] = useState(0);

      if (phase === 0) {
        visualConfig.loadVisualValuesFromYaml(
          'antigravity:\n  sensitive-words:\n    - old-word\n  future-option: true\n'
        );
        setPhase(1);
      } else if (phase === 1) {
        visualConfig.setVisualValues({
          antigravitySensitiveWords: [' Hermes ', '', 'Nous Research'],
        });
        setPhase(2);
      } else {
        return createElement(
          'pre',
          null,
          visualConfig.applyVisualChangesToYaml(
            'antigravity:\n  sensitive-words:\n    - old-word\n  future-option: true\n'
          )
        );
      }

      return null;
    }

    const markup = renderToStaticMarkup(createElement(Harness));
    expect(parseYaml(unwrapPre(markup))).toEqual({
      antigravity: {
        'sensitive-words': ['Hermes', 'Nous Research'],
        'future-option': true,
      },
    });
  });

  test('removes an empty Antigravity block after clearing the list', () => {
    function Harness() {
      const visualConfig = useVisualConfig();
      const [phase, setPhase] = useState(0);

      if (phase === 0) {
        visualConfig.loadVisualValuesFromYaml(
          'debug: true\nantigravity:\n  sensitive-words:\n    - proxy\n'
        );
        setPhase(1);
      } else if (phase === 1) {
        visualConfig.setVisualValues({ antigravitySensitiveWords: [] });
        setPhase(2);
      } else {
        return createElement(
          'pre',
          null,
          visualConfig.applyVisualChangesToYaml(
            'debug: true\nantigravity:\n  sensitive-words:\n    - proxy\n'
          )
        );
      }

      return null;
    }

    const markup = renderToStaticMarkup(createElement(Harness));
    expect(parseYaml(unwrapPre(markup))).toEqual({ debug: true });
  });
});
