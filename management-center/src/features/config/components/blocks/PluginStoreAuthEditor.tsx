import { memo } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import type {
  PluginStoreAuthApplyTo,
  PluginStoreAuthRule,
  PluginStoreAuthType,
} from '@/types/visualConfig';
import { makeClientId } from '@/types/visualConfig';
import { ExpandableInput } from './ExpandableInput';
import styles from './Blocks.module.scss';

const PLUGIN_STORE_AUTH_TYPE_OPTIONS: Array<{ value: PluginStoreAuthType; labelKey: string }> = [
  { value: 'bearer', labelKey: 'config_management.visual.sections.system.store_auth_type_bearer' },
  {
    value: 'github-token',
    labelKey: 'config_management.visual.sections.system.store_auth_type_github_token',
  },
  { value: 'basic', labelKey: 'config_management.visual.sections.system.store_auth_type_basic' },
  { value: 'header', labelKey: 'config_management.visual.sections.system.store_auth_type_header' },
  { value: 'none', labelKey: 'config_management.visual.sections.system.store_auth_type_none' },
];

const PLUGIN_STORE_AUTH_APPLY_TO_OPTIONS: Array<{
  value: PluginStoreAuthApplyTo;
  labelKey: string;
}> = [
  {
    value: 'registry',
    labelKey: 'config_management.visual.sections.system.store_auth_apply_registry',
  },
  {
    value: 'metadata',
    labelKey: 'config_management.visual.sections.system.store_auth_apply_metadata',
  },
  {
    value: 'artifact',
    labelKey: 'config_management.visual.sections.system.store_auth_apply_artifact',
  },
];

const createPluginStoreAuthRule = (): PluginStoreAuthRule => ({
  id: makeClientId(),
  match: '',
  applyTo: [],
  type: 'bearer',
  tokenEnv: '',
  usernameEnv: '',
  passwordEnv: '',
  headerName: '',
  headerValueEnv: '',
  allowInsecure: false,
});

export const PluginStoreAuthEditor = memo(function PluginStoreAuthEditor({
  value,
  disabled,
  onChange,
}: {
  value: PluginStoreAuthRule[];
  disabled?: boolean;
  onChange: (next: PluginStoreAuthRule[]) => void;
}) {
  const { t } = useTranslation();

  const updateRule = (id: string, patch: Partial<PluginStoreAuthRule>) => {
    onChange(value.map((rule) => (rule.id === id ? { ...rule, ...patch } : rule)));
  };
  const addRule = () => onChange([...value, createPluginStoreAuthRule()]);
  const removeRule = (id: string) => onChange(value.filter((rule) => rule.id !== id));
  const toggleApplyTo = (rule: PluginStoreAuthRule, kind: PluginStoreAuthApplyTo) => {
    const nextApplyTo = rule.applyTo.includes(kind)
      ? rule.applyTo.filter((item) => item !== kind)
      : [...rule.applyTo, kind];
    updateRule(rule.id, { applyTo: nextApplyTo });
  };

  return (
    <div className={styles.storeAuthEditor}>
      {value.length === 0 ? (
        <p className={styles.storeAuthEmpty}>
          {t('config_management.visual.sections.system.store_auth_empty')}
        </p>
      ) : null}
      {value.map((rule) => {
        const usesToken = rule.type === 'bearer' || rule.type === 'github-token';
        const usesBasic = rule.type === 'basic';
        const usesHeader = rule.type === 'header';
        return (
          <div key={rule.id} className={styles.storeAuthRule}>
            <div className={styles.storeAuthRuleHeader}>
              <strong>
                {rule.match || t('config_management.visual.sections.system.store_auth_rule')}
              </strong>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => removeRule(rule.id)}
                disabled={disabled}
              >
                {t('config_management.visual.common.delete')}
              </Button>
            </div>
            <div className={styles.storeAuthGrid}>
              <label className={styles.storeAuthField}>
                <span>{t('config_management.visual.sections.system.store_auth_match')}</span>
                <ExpandableInput
                  value={rule.match}
                  placeholder="https://api.github.com/repos/owner/repo/releases/"
                  disabled={disabled}
                  onChange={(match) => updateRule(rule.id, { match })}
                />
              </label>
              <label className={styles.storeAuthField}>
                <span>{t('config_management.visual.sections.system.store_auth_type')}</span>
                <Select
                  value={rule.type}
                  options={PLUGIN_STORE_AUTH_TYPE_OPTIONS.map((option) => ({
                    value: option.value,
                    label: t(option.labelKey),
                  }))}
                  disabled={disabled}
                  onChange={(type) => updateRule(rule.id, { type: type as PluginStoreAuthType })}
                />
              </label>
            </div>

            <div className={styles.storeAuthApplyTo}>
              <span>{t('config_management.visual.sections.system.store_auth_apply_to')}</span>
              <div className={styles.storeAuthCheckboxes}>
                {PLUGIN_STORE_AUTH_APPLY_TO_OPTIONS.map((option) => (
                  <label key={option.value} className={styles.storeAuthCheckbox}>
                    <input
                      type="checkbox"
                      checked={rule.applyTo.includes(option.value)}
                      disabled={disabled}
                      onChange={() => toggleApplyTo(rule, option.value)}
                    />
                    <span>{t(option.labelKey)}</span>
                  </label>
                ))}
              </div>
              <small>
                {t('config_management.visual.sections.system.store_auth_apply_to_hint')}
              </small>
            </div>

            {usesToken ? (
              <label className={styles.storeAuthField}>
                <span>{t('config_management.visual.sections.system.store_auth_token_env')}</span>
                <input
                  className="input"
                  value={rule.tokenEnv}
                  placeholder="CLIPROXY_PLUGIN_STORE_TOKEN"
                  disabled={disabled}
                  onChange={(event) => updateRule(rule.id, { tokenEnv: event.target.value })}
                />
              </label>
            ) : null}

            {usesBasic ? (
              <div className={styles.storeAuthGrid}>
                <label className={styles.storeAuthField}>
                  <span>
                    {t('config_management.visual.sections.system.store_auth_username_env')}
                  </span>
                  <input
                    className="input"
                    value={rule.usernameEnv}
                    disabled={disabled}
                    onChange={(event) => updateRule(rule.id, { usernameEnv: event.target.value })}
                  />
                </label>
                <label className={styles.storeAuthField}>
                  <span>
                    {t('config_management.visual.sections.system.store_auth_password_env')}
                  </span>
                  <input
                    className="input"
                    value={rule.passwordEnv}
                    disabled={disabled}
                    onChange={(event) => updateRule(rule.id, { passwordEnv: event.target.value })}
                  />
                </label>
              </div>
            ) : null}

            {usesHeader ? (
              <div className={styles.storeAuthGrid}>
                <label className={styles.storeAuthField}>
                  <span>
                    {t('config_management.visual.sections.system.store_auth_header_name')}
                  </span>
                  <input
                    className="input"
                    value={rule.headerName}
                    placeholder="X-Plugin-Token"
                    disabled={disabled}
                    onChange={(event) => updateRule(rule.id, { headerName: event.target.value })}
                  />
                </label>
                <label className={styles.storeAuthField}>
                  <span>
                    {t('config_management.visual.sections.system.store_auth_header_value_env')}
                  </span>
                  <input
                    className="input"
                    value={rule.headerValueEnv}
                    disabled={disabled}
                    onChange={(event) =>
                      updateRule(rule.id, { headerValueEnv: event.target.value })
                    }
                  />
                </label>
              </div>
            ) : null}

            <label className={styles.storeAuthCheckbox}>
              <input
                type="checkbox"
                checked={rule.allowInsecure}
                disabled={disabled}
                onChange={(event) => updateRule(rule.id, { allowInsecure: event.target.checked })}
              />
              <span>{t('config_management.visual.sections.system.store_auth_allow_insecure')}</span>
            </label>
          </div>
        );
      })}
      <div className={styles.actionRow}>
        <Button variant="secondary" size="sm" onClick={addRule} disabled={disabled}>
          {t('config_management.visual.sections.system.store_auth_add')}
        </Button>
      </div>
    </div>
  );
});
