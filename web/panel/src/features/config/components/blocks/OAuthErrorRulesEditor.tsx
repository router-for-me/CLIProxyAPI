import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { OAUTH_PROVIDER_PRESETS } from '@/features/authFiles/constants';
import { ErrorRulesEditor } from '@/features/providers/sheets/forms/ErrorRulesEditor';
import type { OAuthChannelRules } from '@/types/visualConfig';

export function OAuthErrorRulesEditor({
  value,
  disabled,
  onChange,
}: {
  value: OAuthChannelRules[];
  disabled: boolean;
  onChange: (groups: OAuthChannelRules[]) => void;
}) {
  const { t } = useTranslation();
  const listId = useId();
  const [selection, setSelection] = useState('copilot');
  const normalized = selection.trim().toLowerCase();
  const current =
    value.find(({ channel }) => channel === selection) ??
    value.find(({ channel }) => channel.trim().toLowerCase() === normalized);
  const channel = current?.channel ?? normalized;
  const options = new Set([...OAUTH_PROVIDER_PRESETS, ...value.map((group) => group.channel)]);
  return (
    <>
      <label htmlFor={listId + '-channel'}>
        {t('config_management.visual.additions.oauthErrorRules.channel')}
      </label>
      <input
        className="input"
        id={listId + '-channel'}
        list={listId}
        value={selection}
        onChange={(event) => setSelection(event.target.value)}
        disabled={disabled}
        spellCheck={false}
        autoComplete="off"
      />
      <datalist id={listId}>
        {Array.from(options, (name) => (
          <option key={name} value={name} />
        ))}
      </datalist>
      <ErrorRulesEditor
        trimMatches
        key={channel}
        rules={current?.rules ?? []}
        disabled={disabled || !channel}
        onChange={(rules) => {
          if (!rules.length) onChange(value.filter((group) => group.channel !== channel));
          else if (current)
            onChange(value.map((group) => (group === current ? { channel, rules } : group)));
          else onChange([...value, { channel, rules }]);
        }}
      />
    </>
  );
}
