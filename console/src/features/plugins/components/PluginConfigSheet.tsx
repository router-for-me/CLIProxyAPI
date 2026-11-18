import { useState, type ChangeEvent, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { Sheet } from '@/components/ui/Sheet';
import { Skeleton } from '@/components/ui/Skeleton';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { FieldLabel } from '@/components/ui/FieldLabel';
import type { PluginConfigField, PluginListEntry } from '@/types';
import {
  normalizePluginConfigFieldType,
  type PluginConfigDraft,
  type PluginDraftValue,
} from '../pluginConfigDraft';
import { getPluginTitle } from '../pluginResources';
import styles from './PluginConfigSheet.module.scss';

type DraftUpdater = (current: PluginConfigDraft) => PluginConfigDraft;

interface PluginConfigSheetProps {
  plugin: PluginListEntry | null;
  draft: PluginConfigDraft | null;
  saving: boolean;
  onClose: () => void;
  onSave: () => void;
  onDraftChange: (updater: DraftUpdater) => void;
}

const setFieldValue =
  (fieldName: string, value: PluginDraftValue): DraftUpdater =>
  (current) => ({
    ...current,
    values: { ...current.values, [fieldName]: value },
    errors: { ...current.errors, [fieldName]: '' },
    touchedFields: { ...current.touchedFields, [fieldName]: true },
  });

function DetailRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className={styles.detailRow}>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

/**
 * Field name with its declared description in an InfoTip, the same way the config page explains
 * settings. The description is also kept in a hidden element (`${id}-desc`) so the control can
 * point at it with aria-describedby.
 */
const fieldLabel = (id: string, label: string, description: string | undefined) => (
  <FieldLabel
    htmlFor={id}
    label={label}
    hint={description}
    hintId={`${id}-desc`}
    className={styles.labelRow}
    labelClassName={styles.rowLabel}
  />
);

const describedBy = (id: string, description: string | undefined) =>
  description ? `${id}-desc` : undefined;

function SheetSkeleton() {
  return (
    <div className={styles.skeleton} aria-hidden="true">
      {[2, 3].map((rows, groupIndex) => (
        <section key={groupIndex} className={styles.section}>
          <Skeleton width={96} height={12} rounded={4} />
          <div className={styles.group}>
            {Array.from({ length: rows }, (_, index) => (
              <div key={index} className={styles.skeletonRow}>
                <Skeleton width={index === 0 ? '32%' : '24%'} height={13} rounded={4} />
                <Skeleton width={index === 0 ? '40%' : '18%'} height={13} rounded={4} />
              </div>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

export function PluginConfigSheet({
  plugin,
  draft,
  saving,
  onClose,
  onSave,
  onDraftChange,
}: PluginConfigSheetProps) {
  const { t } = useTranslation();

  // Keep the last plugin and draft so the content stays put while the sheet animates out.
  const [retained, setRetained] = useState({ plugin, draft });
  if (plugin && (plugin !== retained.plugin || draft !== retained.draft)) {
    setRetained({ plugin, draft });
  }
  const shownPlugin = plugin ?? retained.plugin;
  const shownDraft = plugin ? draft : retained.draft;

  const handleTextChange =
    (fieldName: string) => (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
      onDraftChange(setFieldValue(fieldName, event.target.value));

  const renderBooleanRow = (
    id: string,
    label: string,
    description: string | undefined,
    checked: boolean,
    onChange: (value: boolean) => void
  ) => (
    <div key={id} className={styles.switchRow}>
      {fieldLabel(id, label, description)}
      <ToggleSwitch
        id={id}
        checked={checked}
        onChange={onChange}
        ariaDescribedBy={describedBy(id, description)}
      />
    </div>
  );

  const renderFieldEditor = (field: PluginConfigField, currentDraft: PluginConfigDraft) => {
    const fieldId = `plugin-field-${field.name}`;
    const fieldType = normalizePluginConfigFieldType(field);
    const value = currentDraft.values[field.name];
    const textValue = typeof value === 'string' ? value : '';
    const errorText = currentDraft.errors[field.name];

    if (fieldType === 'boolean') {
      return renderBooleanRow(fieldId, field.name, field.description, value === true, (next) =>
        onDraftChange(setFieldValue(field.name, next))
      );
    }

    if (fieldType === 'enum' && field.enumValues.length > 0) {
      return (
        <div key={field.name} className={styles.fieldRow}>
          {fieldLabel(fieldId, field.name, field.description)}
          <Select
            id={fieldId}
            value={textValue}
            options={field.enumValues.map((item) => ({ value: item, label: item }))}
            onChange={(next) => onDraftChange(setFieldValue(field.name, next))}
            placeholder={t('plugin_management.select_placeholder')}
            ariaDescribedBy={describedBy(fieldId, field.description)}
          />
          {errorText ? <div className={styles.fieldError}>{errorText}</div> : null}
        </div>
      );
    }

    if (fieldType === 'array' || fieldType === 'object') {
      return (
        <div key={field.name} className={styles.fieldRow}>
          {fieldLabel(fieldId, field.name, field.description)}
          <textarea
            id={fieldId}
            className={styles.textarea}
            value={textValue}
            onChange={handleTextChange(field.name)}
            placeholder={fieldType === 'array' ? '[]' : '{}'}
            spellCheck={false}
            aria-describedby={describedBy(fieldId, field.description)}
          />
          {errorText ? <div className={styles.fieldError}>{errorText}</div> : null}
        </div>
      );
    }

    return (
      <div key={field.name} className={styles.fieldRow}>
        <Input
          id={fieldId}
          label={field.name}
          value={textValue}
          onChange={handleTextChange(field.name)}
          inputMode={fieldType === 'integer' || fieldType === 'number' ? 'decimal' : undefined}
          hint={field.description || undefined}
          error={errorText || undefined}
        />
      </div>
    );
  };

  const renderDetails = (current: PluginListEntry) => {
    const github = current.metadata?.githubRepository.trim();
    return (
      <section className={styles.section}>
        <h3 className={styles.sectionTitle}>{t('plugin_management.details_title')}</h3>
        <dl className={`${styles.group} ${styles.details}`}>
          {current.path ? (
            <DetailRow label={t('plugin_management.detail_path')}>
              <span className={styles.mono}>{current.path}</span>
            </DetailRow>
          ) : null}
          <DetailRow label={t('plugin_management.detail_runtime')}>
            {current.registered
              ? t('plugin_management.registered')
              : t('plugin_management.not_registered')}
          </DetailRow>
          <DetailRow label={t('plugin_management.detail_config')}>
            {current.configured
              ? t('plugin_management.configured')
              : t('plugin_management.not_configured')}
          </DetailRow>
          <DetailRow label={t('plugin_management.oauth')}>
            {current.supportsOAuth
              ? t('plugin_management.oauth_supported')
              : t('plugin_management.oauth_not_supported')}
          </DetailRow>
          {github ? (
            <DetailRow label={t('plugin_management.detail_repository')}>
              <a className={styles.link} href={github} target="_blank" rel="noreferrer">
                {github.replace(/^https?:\/\/(www\.)?/i, '')}
              </a>
            </DetailRow>
          ) : null}
        </dl>
      </section>
    );
  };

  const renderForm = (current: PluginListEntry, currentDraft: PluginConfigDraft) => (
    <>
      <section className={styles.section}>
        <h3 className={styles.sectionTitle}>{t('plugin_management.base_settings')}</h3>
        <div className={styles.group}>
          {renderBooleanRow(
            'plugin-config-enabled',
            t('plugin_management.enabled'),
            t('plugin_management.enabled_hint'),
            currentDraft.enabled,
            (enabled) =>
              onDraftChange((draftValue) => ({ ...draftValue, enabled, enabledTouched: true }))
          )}
          <div className={styles.fieldRow}>
            <Input
              id="plugin-config-priority"
              label={t('plugin_management.priority')}
              value={currentDraft.priority}
              onChange={(event) => {
                const value = event.target.value;
                onDraftChange((draftValue) => ({
                  ...draftValue,
                  priority: value,
                  errors: { ...draftValue.errors, priority: '' },
                  priorityTouched: true,
                }));
              }}
              inputMode="numeric"
              error={currentDraft.errors.priority || undefined}
            />
          </div>
        </div>
      </section>

      <section className={styles.section}>
        <h3 className={styles.sectionTitle}>{t('plugin_management.config_fields')}</h3>
        <div className={styles.group}>
          {current.configFields.length > 0 ? (
            current.configFields.map((field) => renderFieldEditor(field, currentDraft))
          ) : (
            <div className={styles.emptyRow}>{t('plugin_management.no_config_fields')}</div>
          )}
        </div>
      </section>
    </>
  );

  return (
    <Sheet
      open={Boolean(plugin)}
      onClose={onClose}
      size="lg"
      title={
        shownPlugin
          ? t('plugin_management.config_title', { name: getPluginTitle(shownPlugin) })
          : t('plugin_management.edit_config')
      }
      description={shownPlugin ? <span className={styles.mono}>{shownPlugin.id}</span> : null}
      closeDisabled={saving}
      footer={
        <div className={styles.footer}>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('common.cancel')}
          </Button>
          <Button onClick={onSave} loading={saving} disabled={!shownDraft}>
            {t('common.save')}
          </Button>
        </div>
      }
    >
      {shownPlugin ? (
        <div className={styles.form} aria-busy={!shownDraft}>
          {renderDetails(shownPlugin)}
          {shownDraft ? renderForm(shownPlugin, shownDraft) : <SheetSkeleton />}
        </div>
      ) : null}
    </Sheet>
  );
}
