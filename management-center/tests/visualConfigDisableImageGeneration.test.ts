import { describe, expect, test } from 'bun:test';
import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { parse as parseYaml } from 'yaml';
import {
  parseDisableImageGenerationMode,
  useVisualConfig,
} from '../src/hooks/useVisualConfig';

describe('visual config disable-image-generation', () => {
  test('loads and writes the passthrough mode', () => {
    expect(parseDisableImageGenerationMode('passthrough')).toBe('passthrough');

    function Harness() {
      const visualConfig = useVisualConfig();
      const [phase, setPhase] = useState(0);

      if (phase === 0) {
        visualConfig.setVisualValues({ disableImageGeneration: 'passthrough' });
        setPhase(1);
      } else {
        return createElement(
          'pre',
          null,
          visualConfig.applyVisualChangesToYaml('disable-image-generation: false\n')
        );
      }

      return null;
    }

    const markup = renderToStaticMarkup(createElement(Harness));
    const result = markup.slice('<pre>'.length, -'</pre>'.length);

    expect(parseYaml(result)).toEqual({ 'disable-image-generation': 'passthrough' });
  });
});
