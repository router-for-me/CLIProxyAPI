import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { makeClientId, type CodexLiveICEServerDraft } from '@/types/visualConfig';
import { FieldGrid, FieldShell } from '../fields/FieldPrimitives';
import styles from './Blocks.module.scss';

/**
 * ICE 服务器列表。标题、说明与校验错误由外层 BlockSetting 渲染；
 * describedBy / invalid 由它传入，挂到每个内部控件上。
 */
export function CodexLiveICEServersEditor({
  value,
  disabled,
  describedBy,
  invalid = false,
  onChange,
}: {
  value: CodexLiveICEServerDraft[];
  disabled?: boolean;
  describedBy?: string;
  invalid?: boolean;
  onChange: (next: CodexLiveICEServerDraft[]) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const updateServer = (serverId: string, patch: Partial<CodexLiveICEServerDraft>) =>
    onChange(value.map((server) => (server.id === serverId ? { ...server, ...patch } : server)));

  return (
    <div className={styles.blockStack}>
      {value.map((server, index) => {
        const serverLabel = t('config_management.visual.additions.iceServer', {
          index: index + 1,
        });
        const removeLabel = t('config_management.visual.additions.iceRemove', {
          index: index + 1,
        });
        const urlsId = `${id}-${server.id}-urls`;
        return (
          <div key={server.id} role="group" aria-label={serverLabel} className={styles.ruleCard}>
            <div className={styles.ruleCardHeader}>
              <span className={styles.ruleCardTitle}>{serverLabel}</span>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={disabled}
                aria-label={removeLabel}
                onClick={() => onChange(value.filter((item) => item.id !== server.id))}
              >
                {removeLabel}
              </Button>
            </div>
            <FieldShell label={t('config_management.visual.additions.iceURLs')} htmlFor={urlsId}>
              <textarea
                id={urlsId}
                className="input"
                rows={3}
                value={server.urlsText}
                disabled={disabled}
                spellCheck={false}
                aria-invalid={invalid}
                aria-describedby={describedBy}
                onChange={(event) => updateServer(server.id, { urlsText: event.target.value })}
              />
            </FieldShell>
            <FieldGrid>
              <Input
                label={t('config_management.visual.additions.iceUsername')}
                value={server.username}
                disabled={disabled}
                autoComplete="off"
                spellCheck={false}
                aria-describedby={describedBy}
                onChange={(event) => updateServer(server.id, { username: event.target.value })}
              />
              <Input
                label={t('config_management.visual.additions.iceCredential')}
                type="password"
                value={server.credential}
                disabled={disabled}
                autoComplete="new-password"
                aria-describedby={describedBy}
                onChange={(event) => updateServer(server.id, { credential: event.target.value })}
              />
            </FieldGrid>
          </div>
        );
      })}
      <div>
        <Button
          type="button"
          variant="secondary"
          size="sm"
          disabled={disabled}
          onClick={() =>
            onChange([...value, { id: makeClientId(), urlsText: '', username: '', credential: '' }])
          }
        >
          {t('config_management.visual.additions.iceAdd')}
        </Button>
      </div>
    </div>
  );
}
