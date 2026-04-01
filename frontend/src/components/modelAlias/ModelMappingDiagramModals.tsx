import type { KeyboardEvent } from 'react';
import type { TFunction } from 'i18next';
import { Modal } from '@/components/ui/Modal';
import { Input } from '@/components/ui/Input';
import { Button } from '@/components/ui/Button';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconTrash2 } from '@/components/ui/icons';
import type { AliasNode, SourceNode } from './ModelMappingDiagramTypes';
import styles from './ModelMappingDiagram.module.scss';

interface RenameAliasModalProps {
  open: boolean;
  t: TFunction;
  value: string;
  error: string;
  onChange: (value: string) => void;
  onClose: () => void;
  onSubmit: () => void;
}

export function RenameAliasModal({
  open,
  t,
  value,
  error,
  onChange,
  onClose,
  onSubmit
}: RenameAliasModalProps) {
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('oauth_model_alias.diagram_rename_alias_title')}
      width={400}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={onSubmit}>{t('oauth_model_alias.diagram_rename_btn')}</Button>
        </>
      }
    >
      <Input
        label={t('oauth_model_alias.diagram_rename_alias_label')}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e: KeyboardEvent<HTMLInputElement>) => {
          if (e.key === 'Enter') onSubmit();
        }}
        error={error}
        placeholder={t('oauth_model_alias.diagram_rename_placeholder')}
        autoFocus
      />
    </Modal>
  );
}

interface AddAliasModalProps {
  open: boolean;
  t: TFunction;
  value: string;
  error: string;
  onChange: (value: string) => void;
  onClose: () => void;
  onSubmit: () => void;
}

export function AddAliasModal({
  open,
  t,
  value,
  error,
  onChange,
  onClose,
  onSubmit
}: AddAliasModalProps) {
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('oauth_model_alias.diagram_add_alias_title')}
      width={400}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={onSubmit}>{t('oauth_model_alias.diagram_add_btn')}</Button>
        </>
      }
    >
      <Input
        label={t('oauth_model_alias.diagram_add_alias_label')}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e: KeyboardEvent<HTMLInputElement>) => {
          if (e.key === 'Enter') onSubmit();
        }}
        error={error}
        placeholder={t('oauth_model_alias.diagram_add_placeholder')}
        autoFocus
      />
    </Modal>
  );
}

interface SettingsAliasModalProps {
  open: boolean;
  t: TFunction;
  alias: string | null;
  aliasNodes: AliasNode[];
  onClose: () => void;
  onToggleFork: (provider: string, sourceModel: string, alias: string, fork: boolean) => void;
  onUnlink: (provider: string, sourceModel: string, alias: string) => void;
}

export function SettingsAliasModal({
  open,
  t,
  alias,
  aliasNodes,
  onClose,
  onToggleFork,
  onUnlink
}: SettingsAliasModalProps) {
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('oauth_model_alias.diagram_settings_title', { alias: alias ?? '' })}
      width={720}
      footer={
        <Button variant="secondary" onClick={onClose}>
          {t('common.close')}
        </Button>
      }
    >
      {alias ? (
        (() => {
          const node = aliasNodes.find((n) => n.alias === alias);
          if (!node || node.sources.length === 0) {
            return <div className={styles.settingsEmpty}>{t('oauth_model_alias.diagram_settings_empty')}</div>;
          }
          return (
            <div className={styles.settingsList}>
              {node.sources.map((source) => {
                const entry = source.aliases.find((item) => item.alias === alias);
                const forkEnabled = entry?.fork === true;
                return (
                  <div key={source.id} className={styles.settingsRow}>
                    <div className={styles.settingsNames}>
                      <span className={styles.settingsSource}>{source.name}</span>
                      <span className={styles.settingsArrow}>→</span>
                      <span className={styles.settingsAlias}>{alias}</span>
                    </div>
                    <div className={styles.settingsActions}>
                      <span className={styles.settingsLabel}>
                        {t('oauth_model_alias.alias_fork_label')}
                      </span>
                      <ToggleSwitch
                        checked={forkEnabled}
                        onChange={(value) => onToggleFork(source.provider, source.name, alias, value)}
                        ariaLabel={t('oauth_model_alias.alias_fork_label')}
                      />
                      <button
                        type="button"
                        className={styles.settingsDelete}
                        onClick={() => onUnlink(source.provider, source.name, alias)}
                        aria-label={t('oauth_model_alias.diagram_delete_link', {
                          provider: source.provider,
                          name: source.name
                        })}
                        title={t('oauth_model_alias.diagram_delete_link', {
                          provider: source.provider,
                          name: source.name
                        })}
                      >
                        <IconTrash2 size={14} />
                      </button>
                    </div>
                  </div>
                );
              })}
            </div>
          );
        })()
      ) : null}
    </Modal>
  );
}

interface SettingsSourceModalProps {
  open: boolean;
  t: TFunction;
  source: SourceNode | null;
  onClose: () => void;
  onToggleFork: (provider: string, sourceModel: string, alias: string, fork: boolean) => void;
  onUnlink: (provider: string, sourceModel: string, alias: string) => void;
}

export function SettingsSourceModal({
  open,
  t,
  source,
  onClose,
  onToggleFork,
  onUnlink
}: SettingsSourceModalProps) {
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('oauth_model_alias.diagram_settings_source_title')}
      width={720}
      footer={
        <Button variant="secondary" onClick={onClose}>
          {t('common.close')}
        </Button>
      }
    >
      {source ? (
        source.aliases.length === 0 ? (
          <div className={styles.settingsEmpty}>{t('oauth_model_alias.diagram_settings_empty')}</div>
        ) : (
          <div className={styles.settingsList}>
            {source.aliases.map((entry) => (
              <div key={`${source.id}-${entry.alias}`} className={styles.settingsRow}>
                <div className={styles.settingsNames}>
                  <span className={styles.settingsSource}>{source.name}</span>
                  <span className={styles.settingsArrow}>→</span>
                  <span className={styles.settingsAlias}>{entry.alias}</span>
                </div>
                <div className={styles.settingsActions}>
                  <span className={styles.settingsLabel}>
                    {t('oauth_model_alias.alias_fork_label')}
                  </span>
                  <ToggleSwitch
                    checked={entry.fork === true}
                    onChange={(value) => onToggleFork(source.provider, source.name, entry.alias, value)}
                    ariaLabel={t('oauth_model_alias.alias_fork_label')}
                  />
                  <button
                    type="button"
                    className={styles.settingsDelete}
                    onClick={() => onUnlink(source.provider, source.name, entry.alias)}
                    aria-label={t('oauth_model_alias.diagram_delete_link', {
                      provider: source.provider,
                      name: source.name
                    })}
                    title={t('oauth_model_alias.diagram_delete_link', {
                      provider: source.provider,
                      name: source.name
                    })}
                  >
                    <IconTrash2 size={14} />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )
      ) : null}
    </Modal>
  );
}
