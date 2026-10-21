import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { useVisualConfig } from '../../src/hooks/useVisualConfig';
import type { VisualConfigValues } from '../../src/types/visualConfig';

/** Exercise the real reducer/codec without a browser or management connection. */
export function runVisualConfig(yaml?: string, patches: Partial<VisualConfigValues>[] = []) {
  let result: ReturnType<typeof useVisualConfig> | undefined;

  function Harness() {
    const visualConfig = useVisualConfig();
    const [phase, setPhase] = useState(0);

    if (phase === 0) {
      if (yaml !== undefined) {
        const loaded = visualConfig.loadVisualValuesFromYaml(yaml);
        if (!loaded.ok) throw new Error(loaded.error);
      }
      setPhase(1);
    } else if (phase <= patches.length) {
      visualConfig.setVisualValues(patches[phase - 1]);
      setPhase(phase + 1);
    } else {
      result = visualConfig;
    }
    return null;
  }

  renderToStaticMarkup(createElement(Harness));
  if (!result) throw new Error('Visual config harness did not finish');
  return result;
}
