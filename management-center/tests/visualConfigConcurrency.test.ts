import { describe, expect, test } from 'bun:test';
import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { parse as parseYaml } from 'yaml';
import { useVisualConfig } from '../src/hooks/useVisualConfig';

describe('visual config concurrency', () => {
  test('only applies dirty visual fields to the latest server YAML', () => {
    function Harness() {
      const visualConfig = useVisualConfig();
      const [phase, setPhase] = useState(0);

      if (phase === 0) {
        visualConfig.loadVisualValuesFromYaml(
          'debug: false\nproxy-url: http://old-proxy.example\n'
        );
        setPhase(1);
      } else if (phase === 1) {
        visualConfig.setVisualValues({ proxyUrl: 'http://localhost:8080' });
        setPhase(2);
      } else {
        return createElement(
          'pre',
          null,
          visualConfig.applyVisualChangesToYaml(
            'debug: true\nproxy-url: http://old-proxy.example\n'
          )
        );
      }

      return null;
    }

    const markup = renderToStaticMarkup(createElement(Harness));
    const merged = markup.slice('<pre>'.length, -'</pre>'.length);

    expect(parseYaml(merged)).toEqual({
      debug: true,
      'proxy-url': 'http://localhost:8080',
    });
  });
});
