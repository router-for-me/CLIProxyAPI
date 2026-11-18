import { memo, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import type {
  PayloadHeaderEntry,
  PayloadModelEntry,
  PayloadParamEntry,
  PayloadParamValueType,
} from '@/types/visualConfig';
import { makeClientId } from '@/types/visualConfig';
import {
  getPayloadParamValidationError,
  VISUAL_CONFIG_PAYLOAD_VALUE_TYPE_OPTIONS,
} from '@/hooks/useVisualConfig';
import { FieldShell } from '../fields/FieldPrimitives';
import { ExpandableInput } from './ExpandableInput';
import { StringListEditor } from './StringListEditor';
import { buildProtocolOptions, getValidationMessage } from './shared';
import styles from './Blocks.module.scss';

function hasPayloadModelAdvancedSettings(model: PayloadModelEntry) {
  return Boolean(
    model.fromProtocol ||
    (model.headers?.length ?? 0) > 0 ||
    (model.match?.length ?? 0) > 0 ||
    (model.notMatch?.length ?? 0) > 0 ||
    (model.exist?.length ?? 0) > 0 ||
    (model.notExist?.length ?? 0) > 0
  );
}

export const PayloadModelsEditor = memo(function PayloadModelsEditor({
  value,
  disabled,
  protocolFirst = false,
  onChange,
}: {
  value: PayloadModelEntry[];
  disabled?: boolean;
  protocolFirst?: boolean;
  onChange: (next: PayloadModelEntry[]) => void;
}) {
  const { t } = useTranslation();
  const rule = { models: value };
  const protocolOptions = useMemo(() => buildProtocolOptions(t, [{ models: value }]), [value, t]);
  const updateRule = (patch: { models: PayloadModelEntry[] }) => onChange(patch.models);
  const fromProtocolOptions = useMemo(
    () => [
      {
        value: '',
        label: t('config_management.visual.payload_rules.provider_default'),
      },
      {
        value: 'openai',
        label: t('config_management.visual.payload_rules.provider_openai'),
      },
      {
        value: 'responses',
        label: t('config_management.visual.payload_rules.provider_responses'),
      },
      {
        value: 'gemini',
        label: t('config_management.visual.payload_rules.provider_gemini'),
      },
      {
        value: 'claude',
        label: t('config_management.visual.payload_rules.provider_claude'),
      },
    ],
    [t]
  );
  const payloadValueTypeOptions = useMemo(
    () =>
      VISUAL_CONFIG_PAYLOAD_VALUE_TYPE_OPTIONS.map((option) => ({
        value: option.value,
        label: t(option.labelKey, { defaultValue: option.defaultLabel }),
      })),
    [t]
  );
  const booleanValueOptions = useMemo(
    () => [
      { value: 'true', label: t('config_management.visual.payload_rules.boolean_true') },
      { value: 'false', label: t('config_management.visual.payload_rules.boolean_false') },
    ],
    [t]
  );
  const [modelAdvancedOverrides, setModelAdvancedOverrides] = useState<Record<string, boolean>>({});

  const addModel = () => {
    const nextModel: PayloadModelEntry = { id: makeClientId(), name: '', protocol: undefined };
    updateRule({ models: [...rule.models, nextModel] });
  };

  const removeModel = (modelIndex: number) => {
    updateRule({ models: rule.models.filter((_, i) => i !== modelIndex) });
  };

  const updateModel = (modelIndex: number, patch: Partial<PayloadModelEntry>) => {
    updateRule({
      models: rule.models.map((m, i) => (i === modelIndex ? { ...m, ...patch } : m)),
    });
  };

  const toggleModelAdvanced = (modelId: string, defaultExpanded: boolean) => {
    setModelAdvancedOverrides((current) => ({
      ...current,
      [modelId]: !(current[modelId] ?? defaultExpanded),
    }));
  };

  const addHeader = (modelIndex: number) => {
    const model = rule.models[modelIndex];
    updateModel(modelIndex, {
      headers: [...(model.headers ?? []), { id: makeClientId(), name: '', value: '' }],
    });
  };

  const updateHeader = (
    modelIndex: number,
    headerIndex: number,
    patch: Partial<PayloadHeaderEntry>
  ) => {
    const model = rule.models[modelIndex];
    updateModel(modelIndex, {
      headers: (model.headers ?? []).map((header, i) =>
        i === headerIndex ? { ...header, ...patch } : header
      ),
    });
  };

  const removeHeader = (modelIndex: number, headerIndex: number) => {
    const model = rule.models[modelIndex];
    updateModel(modelIndex, {
      headers: (model.headers ?? []).filter((_, i) => i !== headerIndex),
    });
  };

  const addCondition = (modelIndex: number, key: 'match' | 'notMatch') => {
    const model = rule.models[modelIndex];
    updateModel(modelIndex, {
      [key]: [
        ...(model[key] ?? []),
        { id: makeClientId(), path: '', valueType: 'string', value: '' },
      ],
    });
  };

  const updateCondition = (
    modelIndex: number,
    key: 'match' | 'notMatch',
    conditionIndex: number,
    patch: Partial<PayloadParamEntry>
  ) => {
    const model = rule.models[modelIndex];
    updateModel(modelIndex, {
      [key]: (model[key] ?? []).map((condition, i) =>
        i === conditionIndex ? { ...condition, ...patch } : condition
      ),
    });
  };

  const removeCondition = (
    modelIndex: number,
    key: 'match' | 'notMatch',
    conditionIndex: number
  ) => {
    const model = rule.models[modelIndex];
    updateModel(modelIndex, {
      [key]: (model[key] ?? []).filter((_, i) => i !== conditionIndex),
    });
  };

  const getValuePlaceholder = (valueType: PayloadParamValueType) => {
    switch (valueType) {
      case 'string':
        return t('config_management.visual.payload_rules.value_string');
      case 'number':
        return t('config_management.visual.payload_rules.value_number');
      case 'boolean':
        return t('config_management.visual.payload_rules.value_boolean');
      case 'json':
        return t('config_management.visual.payload_rules.value_json');
      default:
        return t('config_management.visual.payload_rules.value_default');
    }
  };

  const renderConditionValueEditor = (
    modelIndex: number,
    key: 'match' | 'notMatch',
    conditionIndex: number,
    condition: PayloadParamEntry
  ) => {
    if (condition.valueType === 'boolean') {
      return (
        <Select
          value={
            condition.value.toLowerCase() === 'true' || condition.value.toLowerCase() === 'false'
              ? condition.value.toLowerCase()
              : ''
          }
          options={booleanValueOptions}
          placeholder={t('config_management.visual.payload_rules.value_boolean')}
          disabled={disabled}
          ariaLabel={t('config_management.visual.payload_rules.condition_value')}
          onChange={(nextValue) =>
            updateCondition(modelIndex, key, conditionIndex, { value: nextValue })
          }
        />
      );
    }

    if (condition.valueType === 'json') {
      return (
        <textarea
          className={`input ${styles.payloadJsonInput}`}
          placeholder={getValuePlaceholder(condition.valueType)}
          aria-label={t('config_management.visual.payload_rules.condition_value')}
          value={condition.value}
          onChange={(e) =>
            updateCondition(modelIndex, key, conditionIndex, {
              value: e.target.value,
            })
          }
          disabled={disabled}
        />
      );
    }

    return (
      <ExpandableInput
        placeholder={getValuePlaceholder(condition.valueType)}
        ariaLabel={t('config_management.visual.payload_rules.condition_value')}
        value={condition.value}
        onChange={(nextValue) =>
          updateCondition(modelIndex, key, conditionIndex, { value: nextValue })
        }
        disabled={disabled}
      />
    );
  };

  return (
    <div className={styles.blockStack}>
      <div className={styles.blockLabel}>{t('config_management.visual.payload_rules.models')}</div>
      {(rule.models.length ? rule.models : []).map((model, modelIndex) => {
        const hasAdvancedSettings = hasPayloadModelAdvancedSettings(model);
        const advancedExpanded = modelAdvancedOverrides[model.id] ?? hasAdvancedSettings;

        return (
          <div key={model.id} className={styles.payloadModelGroup}>
            <div
              className={[
                styles.payloadRuleModelRow,
                protocolFirst ? styles.payloadRuleModelRowProtocolFirst : '',
              ]
                .filter(Boolean)
                .join(' ')}
            >
              {protocolFirst ? (
                <>
                  <Select
                    value={model.protocol ?? ''}
                    options={protocolOptions}
                    disabled={disabled}
                    ariaLabel={t('config_management.visual.payload_rules.provider_type')}
                    onChange={(nextValue) =>
                      updateModel(modelIndex, {
                        protocol: (nextValue || undefined) as PayloadModelEntry['protocol'],
                      })
                    }
                  />
                  <ExpandableInput
                    placeholder={t('config_management.visual.payload_rules.model_name')}
                    ariaLabel={t('config_management.visual.payload_rules.model_name')}
                    value={model.name}
                    onChange={(nextValue) => updateModel(modelIndex, { name: nextValue })}
                    disabled={disabled}
                  />
                </>
              ) : (
                <>
                  <ExpandableInput
                    placeholder={t('config_management.visual.payload_rules.model_name')}
                    ariaLabel={t('config_management.visual.payload_rules.model_name')}
                    value={model.name}
                    onChange={(nextValue) => updateModel(modelIndex, { name: nextValue })}
                    disabled={disabled}
                  />
                  <Select
                    value={model.protocol ?? ''}
                    options={protocolOptions}
                    disabled={disabled}
                    ariaLabel={t('config_management.visual.payload_rules.provider_type')}
                    onChange={(nextValue) =>
                      updateModel(modelIndex, {
                        protocol: (nextValue || undefined) as PayloadModelEntry['protocol'],
                      })
                    }
                  />
                </>
              )}
              <Button
                variant="secondary"
                size="sm"
                className={styles.payloadRowActionButton}
                onClick={() => toggleModelAdvanced(model.id, hasAdvancedSettings)}
                disabled={disabled}
              >
                {advancedExpanded
                  ? t('config_management.visual.payload_rules.hide_advanced')
                  : t('config_management.visual.payload_rules.advanced')}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className={styles.payloadRowActionButton}
                onClick={() => removeModel(modelIndex)}
                disabled={disabled}
              >
                {t('config_management.visual.common.delete')}
              </Button>
            </div>

            {advancedExpanded ? (
              <div className={styles.payloadModelAdvanced}>
                <div className={styles.payloadAdvancedGrid}>
                  <FieldShell label={t('config_management.visual.payload_rules.from_protocol')}>
                    <Select
                      value={model.fromProtocol ?? ''}
                      options={fromProtocolOptions}
                      disabled={disabled}
                      ariaLabel={t('config_management.visual.payload_rules.from_protocol')}
                      onChange={(nextValue) =>
                        updateModel(modelIndex, {
                          fromProtocol: (nextValue ||
                            undefined) as PayloadModelEntry['fromProtocol'],
                        })
                      }
                    />
                  </FieldShell>
                </div>

                <div className={styles.blockStack}>
                  <div className={styles.blockLabel}>
                    {t('config_management.visual.payload_rules.headers')}
                  </div>
                  {(model.headers ?? []).map((header, headerIndex) => (
                    <div key={header.id} className={styles.payloadHeaderRow}>
                      <ExpandableInput
                        placeholder={t('config_management.visual.payload_rules.header_name')}
                        ariaLabel={t('config_management.visual.payload_rules.header_name')}
                        value={header.name}
                        onChange={(nextValue) =>
                          updateHeader(modelIndex, headerIndex, {
                            name: nextValue,
                          })
                        }
                        disabled={disabled}
                      />
                      <ExpandableInput
                        placeholder={t('config_management.visual.payload_rules.header_value')}
                        ariaLabel={t('config_management.visual.payload_rules.header_value')}
                        value={header.value}
                        onChange={(nextValue) =>
                          updateHeader(modelIndex, headerIndex, {
                            value: nextValue,
                          })
                        }
                        disabled={disabled}
                      />
                      <Button
                        variant="ghost"
                        size="sm"
                        className={styles.payloadRowActionButton}
                        onClick={() => removeHeader(modelIndex, headerIndex)}
                        disabled={disabled}
                      >
                        {t('config_management.visual.common.delete')}
                      </Button>
                    </div>
                  ))}
                  <div className={styles.actionRow}>
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => addHeader(modelIndex)}
                      disabled={disabled}
                    >
                      {t('config_management.visual.payload_rules.add_header')}
                    </Button>
                  </div>
                </div>

                {(['match', 'notMatch'] as const).map((conditionKey) => (
                  <div key={conditionKey} className={styles.blockStack}>
                    <div className={styles.blockLabel}>
                      {t(`config_management.visual.payload_rules.${conditionKey}`)}
                    </div>
                    {(model[conditionKey] ?? []).map((condition, conditionIndex) => {
                      const conditionError = getValidationMessage(
                        t,
                        getPayloadParamValidationError(condition)
                      );

                      return (
                        <div key={condition.id} className={styles.payloadRuleParamGroup}>
                          <div className={styles.payloadRuleParamRow}>
                            <ExpandableInput
                              placeholder={t(
                                'config_management.visual.payload_rules.condition_path'
                              )}
                              ariaLabel={t('config_management.visual.payload_rules.condition_path')}
                              value={condition.path}
                              onChange={(nextValue) =>
                                updateCondition(modelIndex, conditionKey, conditionIndex, {
                                  path: nextValue,
                                })
                              }
                              disabled={disabled}
                            />
                            <Select
                              value={condition.valueType}
                              options={payloadValueTypeOptions}
                              disabled={disabled}
                              ariaLabel={t('config_management.visual.payload_rules.param_type')}
                              onChange={(nextValue) =>
                                updateCondition(modelIndex, conditionKey, conditionIndex, {
                                  valueType: nextValue as PayloadParamValueType,
                                  value:
                                    nextValue === 'boolean'
                                      ? 'true'
                                      : nextValue === 'json' && condition.value.trim() === ''
                                        ? '{}'
                                        : condition.value,
                                })
                              }
                            />
                            {renderConditionValueEditor(
                              modelIndex,
                              conditionKey,
                              conditionIndex,
                              condition
                            )}
                            <Button
                              variant="ghost"
                              size="sm"
                              className={styles.payloadRowActionButton}
                              onClick={() =>
                                removeCondition(modelIndex, conditionKey, conditionIndex)
                              }
                              disabled={disabled}
                            >
                              {t('config_management.visual.common.delete')}
                            </Button>
                          </div>
                          {conditionError ? (
                            <div className={`error-box ${styles.payloadParamError}`}>
                              {conditionError}
                            </div>
                          ) : null}
                        </div>
                      );
                    })}
                    <div className={styles.actionRow}>
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => addCondition(modelIndex, conditionKey)}
                        disabled={disabled}
                      >
                        {t('config_management.visual.payload_rules.add_condition')}
                      </Button>
                    </div>
                  </div>
                ))}

                <div className={styles.payloadAdvancedGrid}>
                  <div className={styles.blockStack}>
                    <div className={styles.blockLabel}>
                      {t('config_management.visual.payload_rules.exist')}
                    </div>
                    <StringListEditor
                      value={model.exist ?? []}
                      disabled={disabled}
                      placeholder={t('config_management.visual.payload_rules.condition_path')}
                      inputAriaLabel={t('config_management.visual.payload_rules.condition_path')}
                      onChange={(exist) => updateModel(modelIndex, { exist })}
                    />
                  </div>
                  <div className={styles.blockStack}>
                    <div className={styles.blockLabel}>
                      {t('config_management.visual.payload_rules.notExist')}
                    </div>
                    <StringListEditor
                      value={model.notExist ?? []}
                      disabled={disabled}
                      placeholder={t('config_management.visual.payload_rules.condition_path')}
                      inputAriaLabel={t('config_management.visual.payload_rules.condition_path')}
                      onChange={(notExist) => updateModel(modelIndex, { notExist })}
                    />
                  </div>
                </div>
              </div>
            ) : null}
          </div>
        );
      })}
      <div className={styles.actionRow}>
        <Button variant="secondary" size="sm" onClick={() => addModel()} disabled={disabled}>
          {t('config_management.visual.payload_rules.add_model')}
        </Button>
      </div>
    </div>
  );
});
