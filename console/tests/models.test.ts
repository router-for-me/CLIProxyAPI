import { describe, expect, test } from 'bun:test';
import { classifyModels } from '@/utils/models';

const namesIn = (groups: ReturnType<typeof classifyModels>, id: string) =>
  groups.find((group) => group.id === id)?.items.map((item) => item.name) ?? [];

describe('classifyModels', () => {
  test('places Xiaomi MiMo models in their own family instead of Other', () => {
    const groups = classifyModels([
      { name: 'mimo-v2.6-pro' },
      { name: 'mimo-v2.6-flash' },
      { name: 'xiaomi/mimo-v2.5-pro' },
      { name: 'XiaomiMiMo/MiMo-7B-RL' },
      { name: 'custom-xiaomi', alias: '小米旗舰' },
      { name: 'gpt-5' },
    ]);

    expect(namesIn(groups, 'xiaomi')).toEqual([
      'mimo-v2.6-pro',
      'mimo-v2.6-flash',
      'xiaomi/mimo-v2.5-pro',
      'XiaomiMiMo/MiMo-7B-RL',
      'custom-xiaomi',
    ]);
    expect(namesIn(groups, 'gpt')).toEqual(['gpt-5']);
    expect(groups.find((group) => group.id === 'other')).toBeUndefined();
  });

  test('places StepFun models in their own family instead of Other', () => {
    const groups = classifyModels([
      { name: 'step-5-preview' },
      { name: 'step-3.7-flash' },
      { name: 'step-3.5-flash-2603' },
      { name: 'step-1o-turbo-vision' },
      { name: 'step-2-mini' },
      { name: 'stepfun/step-r1-v-mini' },
      { name: 'StepAudio-2.5-Chat' },
      { name: 'step-router-v1' },
      { name: 'custom-step', alias: '阶跃星辰' },
    ]);

    expect(namesIn(groups, 'stepfun')).toEqual([
      'step-5-preview',
      'step-3.7-flash',
      'step-3.5-flash-2603',
      'step-1o-turbo-vision',
      'step-2-mini',
      'stepfun/step-r1-v-mini',
      'StepAudio-2.5-Chat',
      'step-router-v1',
      'custom-step',
    ]);
    expect(groups.find((group) => group.id === 'other')).toBeUndefined();
  });

  test('classifies Codex subscription models such as codex-auto-review as GPT', () => {
    const groups = classifyModels([
      { name: 'codex-auto-review' },
      { name: 'codex-mini-latest' },
      { name: 'gpt-5-codex' },
      { name: 'unknown-local-model' },
    ]);

    expect(namesIn(groups, 'gpt')).toEqual([
      'codex-auto-review',
      'codex-mini-latest',
      'gpt-5-codex',
    ]);
    expect(namesIn(groups, 'other')).toEqual(['unknown-local-model']);
  });

  test('keeps Devin namespaces and existing families ahead of the new matches', () => {
    const groups = classifyModels(
      [
        { name: 'devin/mimo-v2.6-pro' },
        { name: 'devin/step-3.7-flash' },
        { name: 'devin/codex-auto-review' },
        { name: 'muse-spark-1.3' },
        { name: 'minimax-m2' },
        { name: 'abab6.5s' },
      ],
      { otherLabel: '其他' }
    );

    expect(namesIn(groups, 'devin')).toEqual([
      'devin/mimo-v2.6-pro',
      'devin/step-3.7-flash',
      'devin/codex-auto-review',
    ]);
    expect(namesIn(groups, 'meta')).toEqual(['muse-spark-1.3']);
    expect(namesIn(groups, 'minimax')).toEqual(['minimax-m2', 'abab6.5s']);
    expect(groups.find((group) => group.id === 'xiaomi')).toBeUndefined();
    expect(groups.find((group) => group.id === 'stepfun')).toBeUndefined();
    expect(groups.find((group) => group.id === 'other')).toBeUndefined();
  });
});
