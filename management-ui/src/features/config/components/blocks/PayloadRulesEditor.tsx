import { memo, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import type {
  PayloadHeaderEntry,
  PayloadModelEntry,
  PayloadParamEntry,
  PayloadParamValueType,
  PayloadRule,
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

export const PayloadRulesEditor = memo(function PayloadRulesEditor({
  value,
  disabled,
  protocolFirst = false,
  rawJsonValues = false,
  onChange,
}: {
  value: PayloadRule[];
  disabled?: boolean;
  protocolFirst?: boolean;
  rawJsonValues?: boolean;
  onChange: (next: PayloadRule[]) => void;
}) {
  const { t } = useTranslation();
  const rules = value;
  const protocolOptions = useMemo(() => buildProtocolOptions(t, rules), [rules, t]);
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

  const addRule = () => onChange([...rules, { id: makeClientId(), models: [], params: [] }]);
  const removeRule = (ruleIndex: number) => onChange(rules.filter((_, i) => i !== ruleIndex));

  const updateRule = (ruleIndex: number, patch: Partial<PayloadRule>) =>
    onChange(rules.map((rule, i) => (i === ruleIndex ? { ...rule, ...patch } : rule)));

  const addModel = (ruleIndex: number) => {
    const rule = rules[ruleIndex];
    const nextModel: PayloadModelEntry = { id: makeClientId(), name: '', protocol: undefined };
    updateRule(ruleIndex, { models: [...rule.models, nextModel] });
  };

  const removeModel = (ruleIndex: number, modelIndex: number) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, { models: rule.models.filter((_, i) => i !== modelIndex) });
  };

  const updateModel = (
    ruleIndex: number,
    modelIndex: number,
    patch: Partial<PayloadModelEntry>
  ) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, {
      models: rule.models.map((m, i) => (i === modelIndex ? { ...m, ...patch } : m)),
    });
  };

  const toggleModelAdvanced = (modelId: string, defaultExpanded: boolean) => {
    setModelAdvancedOverrides((current) => ({
      ...current,
      [modelId]: !(current[modelId] ?? defaultExpanded),
    }));
  };

  const addHeader = (ruleIndex: number, modelIndex: number) => {
    const rule = rules[ruleIndex];
    const model = rule.models[modelIndex];
    updateModel(ruleIndex, modelIndex, {
      headers: [...(model.headers ?? []), { id: makeClientId(), name: '', value: '' }],
    });
  };

  const updateHeader = (
    ruleIndex: number,
    modelIndex: number,
    headerIndex: number,
    patch: Partial<PayloadHeaderEntry>
  ) => {
    const model = rules[ruleIndex].models[modelIndex];
    updateModel(ruleIndex, modelIndex, {
      headers: (model.headers ?? []).map((header, i) =>
        i === headerIndex ? { ...header, ...patch } : header
      ),
    });
  };

  const removeHeader = (ruleIndex: number, modelIndex: number, headerIndex: number) => {
    const model = rules[ruleIndex].models[modelIndex];
    updateModel(ruleIndex, modelIndex, {
      headers: (model.headers ?? []).filter((_, i) => i !== headerIndex),
    });
  };

  const addCondition = (ruleIndex: number, modelIndex: number, key: 'match' | 'notMatch') => {
    const model = rules[ruleIndex].models[modelIndex];
    updateModel(ruleIndex, modelIndex, {
      [key]: [
        ...(model[key] ?? []),
        { id: makeClientId(), path: '', valueType: 'string', value: '' },
      ],
    });
  };

  const updateCondition = (
    ruleIndex: number,
    modelIndex: number,
    key: 'match' | 'notMatch',
    conditionIndex: number,
    patch: Partial<PayloadParamEntry>
  ) => {
    const model = rules[ruleIndex].models[modelIndex];
    updateModel(ruleIndex, modelIndex, {
      [key]: (model[key] ?? []).map((condition, i) =>
        i === conditionIndex ? { ...condition, ...patch } : condition
      ),
    });
  };

  const removeCondition = (
    ruleIndex: number,
    modelIndex: number,
    key: 'match' | 'notMatch',
    conditionIndex: number
  ) => {
    const model = rules[ruleIndex].models[modelIndex];
    updateModel(ruleIndex, modelIndex, {
      [key]: (model[key] ?? []).filter((_, i) => i !== conditionIndex),
    });
  };

  const addParam = (ruleIndex: number) => {
    const rule = rules[ruleIndex];
    const nextParam: PayloadParamEntry = {
      id: makeClientId(),
      path: '',
      valueType: rawJsonValues ? 'json' : 'string',
      value: '',
    };
    updateRule(ruleIndex, { params: [...rule.params, nextParam] });
  };

  const removeParam = (ruleIndex: number, paramIndex: number) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, { params: rule.params.filter((_, i) => i !== paramIndex) });
  };

  const updateParam = (
    ruleIndex: number,
    paramIndex: number,
    patch: Partial<PayloadParamEntry>
  ) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, {
      params: rule.params.map((p, i) => (i === paramIndex ? { ...p, ...patch } : p)),
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

  const getParamErrorMessage = (param: PayloadParamEntry) => {
    const errorCode = getPayloadParamValidationError(
      rawJsonValues ? { ...param, valueType: 'json' } : param
    );
    return getValidationMessage(t, errorCode);
  };

  const renderConditionValueEditor = (
    ruleIndex: number,
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
            updateCondition(ruleIndex, modelIndex, key, conditionIndex, { value: nextValue })
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
            updateCondition(ruleIndex, modelIndex, key, conditionIndex, {
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
          updateCondition(ruleIndex, modelIndex, key, conditionIndex, { value: nextValue })
        }
        disabled={disabled}
      />
    );
  };

  const renderParamValueEditor = (
    ruleIndex: number,
    paramIndex: number,
    param: PayloadParamEntry
  ) => {
    if (rawJsonValues) {
      return (
        <textarea
          className={`input ${styles.payloadJsonInput}`}
          placeholder={t('config_management.visual.payload_rules.value_raw_json')}
          aria-label={t('config_management.visual.payload_rules.param_value')}
          value={param.value}
          onChange={(e) =>
            updateParam(ruleIndex, paramIndex, { value: e.target.value, valueType: 'json' })
          }
          disabled={disabled}
        />
      );
    }

    if (param.valueType === 'boolean') {
      return (
        <Select
          value={
            param.value.toLowerCase() === 'true' || param.value.toLowerCase() === 'false'
              ? param.value.toLowerCase()
              : ''
          }
          options={booleanValueOptions}
          placeholder={t('config_management.visual.payload_rules.value_boolean')}
          disabled={disabled}
          ariaLabel={t('config_management.visual.payload_rules.param_value')}
          onChange={(nextValue) => updateParam(ruleIndex, paramIndex, { value: nextValue })}
        />
      );
    }

    if (param.valueType === 'json') {
      return (
        <textarea
          className={`input ${styles.payloadJsonInput}`}
          placeholder={getValuePlaceholder(param.valueType)}
          aria-label={t('config_management.visual.payload_rules.param_value')}
          value={param.value}
          onChange={(e) => updateParam(ruleIndex, paramIndex, { value: e.target.value })}
          disabled={disabled}
        />
      );
    }

    return (
      <ExpandableInput
        placeholder={getValuePlaceholder(param.valueType)}
        ariaLabel={t('config_management.visual.payload_rules.param_value')}
        value={param.value}
        onChange={(nextValue) => updateParam(ruleIndex, paramIndex, { value: nextValue })}
        disabled={disabled}
      />
    );
  };

  return (
    <div className={styles.blockStack}>
      {rules.map((rule, ruleIndex) => (
        <div key={rule.id} className={styles.ruleCard}>
          <div className={styles.ruleCardHeader}>
            <div className={styles.ruleCardTitle}>
              {t('config_management.visual.payload_rules.rule')} {ruleIndex + 1}
            </div>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => removeRule(ruleIndex)}
              disabled={disabled}
            >
              {t('config_management.visual.common.delete')}
            </Button>
          </div>

          <div className={styles.blockStack}>
            <div className={styles.blockLabel}>
              {t('config_management.visual.payload_rules.models')}
            </div>
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
                            updateModel(ruleIndex, modelIndex, {
                              protocol: (nextValue || undefined) as PayloadModelEntry['protocol'],
                            })
                          }
                        />
                        <ExpandableInput
                          placeholder={t('config_management.visual.payload_rules.model_name')}
                          ariaLabel={t('config_management.visual.payload_rules.model_name')}
                          value={model.name}
                          onChange={(nextValue) =>
                            updateModel(ruleIndex, modelIndex, { name: nextValue })
                          }
                          disabled={disabled}
                        />
                      </>
                    ) : (
                      <>
                        <ExpandableInput
                          placeholder={t('config_management.visual.payload_rules.model_name')}
                          ariaLabel={t('config_management.visual.payload_rules.model_name')}
                          value={model.name}
                          onChange={(nextValue) =>
                            updateModel(ruleIndex, modelIndex, { name: nextValue })
                          }
                          disabled={disabled}
                        />
                        <Select
                          value={model.protocol ?? ''}
                          options={protocolOptions}
                          disabled={disabled}
                          ariaLabel={t('config_management.visual.payload_rules.provider_type')}
                          onChange={(nextValue) =>
                            updateModel(ruleIndex, modelIndex, {
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
                      onClick={() => removeModel(ruleIndex, modelIndex)}
                      disabled={disabled}
                    >
                      {t('config_management.visual.common.delete')}
                    </Button>
                  </div>

                  {advancedExpanded ? (
                    <div className={styles.payloadModelAdvanced}>
                      <div className={styles.payloadAdvancedGrid}>
                        <FieldShell
                          label={t('config_management.visual.payload_rules.from_protocol')}
                        >
                          <Select
                            value={model.fromProtocol ?? ''}
                            options={fromProtocolOptions}
                            disabled={disabled}
                            ariaLabel={t('config_management.visual.payload_rules.from_protocol')}
                            onChange={(nextValue) =>
                              updateModel(ruleIndex, modelIndex, {
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
                                updateHeader(ruleIndex, modelIndex, headerIndex, {
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
                                updateHeader(ruleIndex, modelIndex, headerIndex, {
                                  value: nextValue,
                                })
                              }
                              disabled={disabled}
                            />
                            <Button
                              variant="ghost"
                              size="sm"
                              className={styles.payloadRowActionButton}
                              onClick={() => removeHeader(ruleIndex, modelIndex, headerIndex)}
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
                            onClick={() => addHeader(ruleIndex, modelIndex)}
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
                                    ariaLabel={t(
                                      'config_management.visual.payload_rules.condition_path'
                                    )}
                                    value={condition.path}
                                    onChange={(nextValue) =>
                                      updateCondition(
                                        ruleIndex,
                                        modelIndex,
                                        conditionKey,
                                        conditionIndex,
                                        { path: nextValue }
                                      )
                                    }
                                    disabled={disabled}
                                  />
                                  <Select
                                    value={condition.valueType}
                                    options={payloadValueTypeOptions}
                                    disabled={disabled}
                                    ariaLabel={t(
                                      'config_management.visual.payload_rules.param_type'
                                    )}
                                    onChange={(nextValue) =>
                                      updateCondition(
                                        ruleIndex,
                                        modelIndex,
                                        conditionKey,
                                        conditionIndex,
                                        {
                                          valueType: nextValue as PayloadParamValueType,
                                          value:
                                            nextValue === 'boolean'
                                              ? 'true'
                                              : nextValue === 'json' &&
                                                  condition.value.trim() === ''
                                                ? '{}'
                                                : condition.value,
                                        }
                                      )
                                    }
                                  />
                                  {renderConditionValueEditor(
                                    ruleIndex,
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
                                      removeCondition(
                                        ruleIndex,
                                        modelIndex,
                                        conditionKey,
                                        conditionIndex
                                      )
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
                              onClick={() => addCondition(ruleIndex, modelIndex, conditionKey)}
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
                            inputAriaLabel={t(
                              'config_management.visual.payload_rules.condition_path'
                            )}
                            onChange={(exist) => updateModel(ruleIndex, modelIndex, { exist })}
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
                            inputAriaLabel={t(
                              'config_management.visual.payload_rules.condition_path'
                            )}
                            onChange={(notExist) =>
                              updateModel(ruleIndex, modelIndex, { notExist })
                            }
                          />
                        </div>
                      </div>
                    </div>
                  ) : null}
                </div>
              );
            })}
            <div className={styles.actionRow}>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => addModel(ruleIndex)}
                disabled={disabled}
              >
                {t('config_management.visual.payload_rules.add_model')}
              </Button>
            </div>
          </div>

          <div className={styles.blockStack}>
            <div className={styles.blockLabel}>
              {t('config_management.visual.payload_rules.params')}
            </div>
            {(rule.params.length ? rule.params : []).map((param, paramIndex) => {
              const paramError = getParamErrorMessage(param);

              return (
                <div key={param.id} className={styles.payloadRuleParamGroup}>
                  <div
                    className={[
                      styles.payloadRuleParamRow,
                      rawJsonValues ? styles.payloadRuleRawParamRow : '',
                    ]
                      .filter(Boolean)
                      .join(' ')}
                  >
                    <ExpandableInput
                      placeholder={t('config_management.visual.payload_rules.json_path')}
                      ariaLabel={t('config_management.visual.payload_rules.json_path')}
                      value={param.path}
                      onChange={(nextValue) =>
                        updateParam(ruleIndex, paramIndex, { path: nextValue })
                      }
                      disabled={disabled}
                    />
                    {rawJsonValues ? null : (
                      <Select
                        value={param.valueType}
                        options={payloadValueTypeOptions}
                        disabled={disabled}
                        ariaLabel={t('config_management.visual.payload_rules.param_type')}
                        onChange={(nextValue) =>
                          updateParam(ruleIndex, paramIndex, {
                            valueType: nextValue as PayloadParamValueType,
                            value:
                              nextValue === 'boolean'
                                ? 'true'
                                : nextValue === 'json' && param.value.trim() === ''
                                  ? '{}'
                                  : param.value,
                          })
                        }
                      />
                    )}
                    {renderParamValueEditor(ruleIndex, paramIndex, param)}
                    <Button
                      variant="ghost"
                      size="sm"
                      className={styles.payloadRowActionButton}
                      onClick={() => removeParam(ruleIndex, paramIndex)}
                      disabled={disabled}
                    >
                      {t('config_management.visual.common.delete')}
                    </Button>
                  </div>
                  {paramError && (
                    <div className={`error-box ${styles.payloadParamError}`}>{paramError}</div>
                  )}
                </div>
              );
            })}
            <div className={styles.actionRow}>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => addParam(ruleIndex)}
                disabled={disabled}
              >
                {t('config_management.visual.payload_rules.add_param')}
              </Button>
            </div>
          </div>
        </div>
      ))}

      {rules.length === 0 && (
        <div className={styles.emptyState}>
          {t('config_management.visual.payload_rules.no_rules')}
        </div>
      )}

      <div className={styles.actionRow}>
        <Button variant="secondary" size="sm" onClick={addRule} disabled={disabled}>
          {t('config_management.visual.payload_rules.add_rule')}
        </Button>
      </div>
    </div>
  );
});
