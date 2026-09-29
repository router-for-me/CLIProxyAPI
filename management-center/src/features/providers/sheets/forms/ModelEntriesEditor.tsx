import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconChevronDown, IconPlus, IconX } from '@/components/ui/icons';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { THINKING_LEVELS, type ThinkingLevel } from '../../thinkingLevels';
import type { ModelEntryInput } from '../../types';
import styles from './sharedForm.module.scss';

const COLLAPSED_LIMIT = 10;

interface ModelEntriesEditorProps {
  models: ModelEntryInput[];
  /** Only OpenAI-compatible entries can expose the image-generation capability. */
  supportsImage: boolean;
  /** Every backend provider model can override its thinking capability. */
  supportsThinking: boolean;
  mutating: boolean;
  removeDisabled: boolean;
  onUpdate: (idx: number, patch: Partial<ModelEntryInput>) => void;
  onAdd: () => void;
  onRemove: (idx: number) => void;
}

export function ModelEntriesEditor({
  models,
  supportsImage,
  supportsThinking,
  mutating,
  removeDisabled,
  onUpdate,
  onAdd,
  onRemove,
}: ModelEntriesEditorProps) {
  const { t } = useTranslation();
  const [expandedIdx, setExpandedIdx] = useState<number | null>(null);
  const [showAll, setShowAll] = useState(false);

  const handleAdd = () => {
    // New rows are appended; make sure the truncated list doesn't hide them.
    if (!showAll && models.length >= COLLAPSED_LIMIT) {
      setShowAll(true);
    }
    onAdd();
  };

  const handleRemove = (removeIdx: number) => {
    setExpandedIdx((prev) => {
      if (prev === null || prev === removeIdx) return null;
      return prev > removeIdx ? prev - 1 : prev;
    });
    onRemove(removeIdx);
  };

  const visible = showAll ? models : models.slice(0, COLLAPSED_LIMIT);

  return (
    <>
      {visible.map((entry, idx) => {
        const hasExtendedOptions = supportsImage || supportsThinking;
        const expanded = hasExtendedOptions && expandedIdx === idx;
        const thinkingLevels = entry.thinkingLevels ?? [];
        const hasThinking = entry.thinkingLevelsTouched
          ? thinkingLevels.length > 0
          : (entry.thinkingJson ?? '').trim().length > 0;
        const toggleThinkingLevel = (level: ThinkingLevel) => {
          const nextLevels = thinkingLevels.includes(level)
            ? thinkingLevels.filter((item) => item !== level)
            : THINKING_LEVELS.filter((item) => item === level || thinkingLevels.includes(item));
          onUpdate(idx, { thinkingLevels: nextLevels, thinkingLevelsTouched: true });
        };
        return (
          <div key={idx} className={styles.modelEntry}>
            <div className={styles.modelAliasRow}>
              <input
                className={styles.input}
                placeholder="model-name"
                value={entry.name}
                onChange={(e) => onUpdate(idx, { name: e.target.value })}
                disabled={mutating}
              />
              <input
                className={styles.input}
                placeholder="alias (optional)"
                value={entry.alias ?? ''}
                onChange={(e) => onUpdate(idx, { alias: e.target.value })}
                disabled={mutating}
              />
              <div className={styles.modelEntryActions}>
                {supportsImage && !expanded && entry.image === true ? (
                  <span className={styles.entryBadge}>
                    {t('providersPage.form.modelBadgeImage')}
                  </span>
                ) : null}
                {supportsThinking && !expanded && hasThinking ? (
                  <span className={styles.entryBadge}>
                    {t('providersPage.form.modelBadgeThinking')}
                  </span>
                ) : null}
                {hasExtendedOptions ? (
                  <button
                    type="button"
                    className={styles.entryCardIconBtn}
                    onClick={() => setExpandedIdx(expanded ? null : idx)}
                    title={expanded ? t('common.collapse') : t('common.expand')}
                    aria-label={expanded ? t('common.collapse') : t('common.expand')}
                    aria-expanded={expanded}
                  >
                    <IconChevronDown
                      className={[
                        styles.entryCardChevron,
                        expanded ? styles.entryCardChevronOpen : '',
                      ]
                        .filter(Boolean)
                        .join(' ')}
                      size={14}
                    />
                  </button>
                ) : null}
                <button
                  type="button"
                  className={styles.removeBtn}
                  disabled={mutating || removeDisabled}
                  onClick={() => handleRemove(idx)}
                >
                  <IconX size={12} />
                </button>
              </div>
            </div>
            {expanded ? (
              <div className={styles.modelEntryDetails}>
                {supportsImage ? (
                  <label className={styles.checkboxRow}>
                    <input
                      type="checkbox"
                      className={styles.checkboxBox}
                      checked={entry.image === true}
                      disabled={mutating}
                      onChange={(e) => onUpdate(idx, { image: e.target.checked })}
                    />
                    <span className={styles.checkboxText}>
                      <span>{t('providersPage.form.modelImage')}</span>
                      <small>{t('providersPage.form.modelImageHint')}</small>
                    </span>
                  </label>
                ) : null}
                {supportsThinking ? (
                  <fieldset className={styles.thinkingFieldset}>
                    <legend className={styles.label}>
                      {t('providersPage.form.thinkingConfig')}
                    </legend>
                    <div className={styles.thinkingLevelGrid}>
                      {THINKING_LEVELS.map((level) => (
                        <SelectionCheckbox
                          key={level}
                          checked={thinkingLevels.includes(level)}
                          disabled={mutating}
                          onChange={() => toggleThinkingLevel(level)}
                          className={`${styles.thinkingLevelOption} ${
                            thinkingLevels.includes(level) ? styles.thinkingLevelOptionSelected : ''
                          }`}
                          labelClassName={styles.thinkingLevelLabel}
                          label={
                            <>
                              <span>{t(`providersPage.form.thinkingLevels.${level}`)}</span>
                              <code>{level}</code>
                            </>
                          }
                        />
                      ))}
                    </div>
                    {(entry.thinkingJson ?? '').trim() && !entry.thinkingLevelsTouched ? (
                      <p className={styles.thinkingExistingHint}>
                        {t('providersPage.form.thinkingExistingHint')}
                      </p>
                    ) : null}
                  </fieldset>
                ) : null}
              </div>
            ) : null}
          </div>
        );
      })}
      {models.length > COLLAPSED_LIMIT ? (
        <button type="button" className={styles.showMoreBtn} onClick={() => setShowAll((v) => !v)}>
          {showAll
            ? t('providersPage.form.showFewerEntries')
            : t('providersPage.form.showAllEntries', { count: models.length })}
        </button>
      ) : null}
      <button type="button" className={styles.addBtn} disabled={mutating} onClick={handleAdd}>
        <IconPlus size={12} />
        <span>{t('providersPage.form.addModel')}</span>
      </button>
    </>
  );
}
