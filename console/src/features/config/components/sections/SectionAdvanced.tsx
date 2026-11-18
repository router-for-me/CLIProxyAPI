import { useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Collapsible } from '@/components/ui/Collapsible';
import {
  LiteralReplacementEditor,
  PatternReplacementEditor,
} from '@/components/providers/SensitiveReplacementEditor';
import type { PluginStoreAuthRule } from '@/types/visualConfig';
import { CONFIG_TAB_ICONS, SECTION_INDEX_LABELS } from '../../constants';
import type { ConfigSectionProps } from '../../types';
import { SectionCard } from '../SectionCard';
import {
  BlockSetting,
  FieldStack,
  SettingCell,
  SettingList,
  TextSetting,
  ToggleSetting,
} from '../fields/FieldPrimitives';
import { PluginStoreAuthEditor } from '../blocks/PluginStoreAuthEditor';
import { StringListEditor } from '../blocks/StringListEditor';
import { getValidationMessage } from '../blocks/shared';
import { SectionOAuthBehavior } from './SectionOAuthBehavior';

const Icon = CONFIG_TAB_ICONS.advanced;

/** 05 高级与实验：插件源、供应商敏感词、签名缓存与请求头默认值。 */
export function SectionAdvanced({
  values,
  validationErrors,
  disabled,
  animateIn,
  onChange,
}: ConfigSectionProps) {
  const { t } = useTranslation();

  const handlePluginStoreSourcesChange = useCallback(
    (pluginStoreSources: string[]) => onChange({ pluginStoreSources }),
    [onChange]
  );
  const handlePluginStoreAuthChange = useCallback(
    (pluginStoreAuth: PluginStoreAuthRule[]) => onChange({ pluginStoreAuth }),
    [onChange]
  );
  const handleAntigravitySensitiveWordsChange = useCallback(
    (antigravitySensitiveWords: string[]) => onChange({ antigravitySensitiveWords }),
    [onChange]
  );
  const handleDevinSensitiveWordsChange = useCallback(
    (devinSensitiveWords: string[]) => onChange({ devinSensitiveWords }),
    [onChange]
  );

  return (
    <SectionCard
      indexLabel={SECTION_INDEX_LABELS.advanced}
      icon={<Icon size={16} />}
      title={t('config_management.visual.sections.advanced.title')}
      animateIn={animateIn}
    >
      <SectionOAuthBehavior
        values={values}
        validationErrors={validationErrors}
        disabled={disabled}
        onChange={onChange}
      />

      <Collapsible
        label={t('config_management.visual.sections.advanced.plugins_title')}
        defaultOpen={false}
      >
        <FieldStack>
          <SettingList>
            <ToggleSetting
              fieldId="pluginsEnabled"
              label={t('config_management.visual.sections.system.plugins_enabled')}
              description={t('config_management.visual.sections.system.plugins_enabled_desc')}
              checked={values.pluginsEnabled}
              disabled={disabled}
              onChange={(pluginsEnabled) => onChange({ pluginsEnabled })}
            />
          </SettingList>

          <SettingList
            title={t('config_management.visual.sections.system.plugin_store_sources')}
            description={t('config_management.visual.sections.system.plugin_store_sources_desc')}
          >
            <BlockSetting
              fieldId="pluginStoreSources"
              label={t('config_management.visual.sections.system.plugin_store_sources_label')}
            >
              <StringListEditor
                value={values.pluginStoreSources}
                disabled={disabled}
                placeholder={t(
                  'config_management.visual.sections.system.plugin_store_sources_placeholder'
                )}
                inputAriaLabel={t(
                  'config_management.visual.sections.system.plugin_store_sources_label'
                )}
                onChange={handlePluginStoreSourcesChange}
              />
            </BlockSetting>
          </SettingList>

          <SettingList
            title={t('config_management.visual.sections.system.plugin_store_auth')}
            // 凭据处理：只填环境变量名、不填真实 token——常驻在组标题下。
            description={t('config_management.visual.sections.system.plugin_store_auth_hint')}
            keepDescriptionVisible
          >
            <SettingCell fieldId="pluginStoreAuth">
              <PluginStoreAuthEditor
                value={values.pluginStoreAuth}
                disabled={disabled}
                onChange={handlePluginStoreAuthChange}
              />
            </SettingCell>
          </SettingList>
        </FieldStack>
      </Collapsible>

      <Collapsible
        label={t('config_management.visual.sections.advanced.antigravity_title')}
        defaultOpen={false}
      >
        <FieldStack>
          <SettingList>
            <ToggleSetting
              fieldId="quotaAntigravityCredits"
              label={t('config_management.visual.sections.advanced.antigravity_credits')}
              checked={values.quotaAntigravityCredits}
              disabled={disabled}
              onChange={(quotaAntigravityCredits) => onChange({ quotaAntigravityCredits })}
            />
          </SettingList>
          <SettingList
            title={t('config_management.visual.sections.system.antigravity_sensitive_words')}
          >
            {/* 组里只有这一行：「做什么」与「怎么填」合进同一个 InfoTip，不在组标题和行标题上各挂一个。 */}
            <BlockSetting
              fieldId="antigravitySensitiveWords"
              label={t(
                'config_management.visual.sections.system.antigravity_sensitive_words_label'
              )}
              description={
                <>
                  {t('config_management.visual.sections.system.antigravity_sensitive_words_desc')}
                  <br />
                  {t('config_management.visual.sections.system.antigravity_sensitive_words_hint')}
                </>
              }
            >
              <StringListEditor
                value={values.antigravitySensitiveWords}
                disabled={disabled}
                placeholder={t(
                  'config_management.visual.sections.system.antigravity_sensitive_words_placeholder'
                )}
                inputAriaLabel={t(
                  'config_management.visual.sections.system.antigravity_sensitive_words_label'
                )}
                onChange={handleAntigravitySensitiveWordsChange}
              />
            </BlockSetting>
          </SettingList>

          <SettingList title={t('sensitive_replacements.group_title')}>
            <BlockSetting
              fieldId="antigravitySensitiveWordsReplace"
              label={t('sensitive_replacements.literal_title')}
              description={t('sensitive_replacements.literal_hint')}
            >
              {({ describedBy }) => (
                <LiteralReplacementEditor
                  value={values.antigravitySensitiveWordsReplace}
                  onChange={(antigravitySensitiveWordsReplace) =>
                    onChange({ antigravitySensitiveWordsReplace })
                  }
                  disabled={disabled}
                  describedBy={describedBy}
                />
              )}
            </BlockSetting>
            {/* 规则按序执行、无效模式被后端静默丢弃：不读就会配错，常驻可见。 */}
            <BlockSetting
              fieldId="antigravitySensitivePatternsReplace"
              label={t('sensitive_replacements.pattern_title')}
              description={t('sensitive_replacements.pattern_hint')}
              keepDescriptionVisible
            >
              {({ describedBy }) => (
                <PatternReplacementEditor
                  value={values.antigravitySensitivePatternsReplace}
                  onChange={(antigravitySensitivePatternsReplace) =>
                    onChange({ antigravitySensitivePatternsReplace })
                  }
                  disabled={disabled}
                  describedBy={describedBy}
                />
              )}
            </BlockSetting>
          </SettingList>

          <SettingList title={t('config_management.visual.sections.advanced.signature_title')}>
            <ToggleSetting
              fieldId="antigravitySignatureCacheEnabled"
              label={t('config_management.visual.sections.system.antigravity_signature_cache')}
              description={t(
                'config_management.visual.sections.system.antigravity_signature_cache_desc'
              )}
              checked={values.antigravitySignatureCacheEnabled}
              disabled={disabled}
              onChange={(antigravitySignatureCacheEnabled) =>
                onChange({ antigravitySignatureCacheEnabled })
              }
            />
            <ToggleSetting
              fieldId="antigravitySignatureBypassStrict"
              label={t('config_management.visual.sections.system.antigravity_signature_strict')}
              description={t(
                'config_management.visual.sections.system.antigravity_signature_strict_desc'
              )}
              checked={values.antigravitySignatureBypassStrict}
              disabled={disabled}
              onChange={(antigravitySignatureBypassStrict) =>
                onChange({ antigravitySignatureBypassStrict })
              }
            />
          </SettingList>
        </FieldStack>
      </Collapsible>

      <Collapsible
        label={t('config_management.visual.sections.advanced.devin_title')}
        defaultOpen={false}
      >
        <FieldStack>
          <SettingList
            title={t('config_management.visual.sections.system.devin_sensitive_words')}
            description={t('config_management.visual.sections.system.devin_sensitive_words_desc')}
          >
            <BlockSetting
              fieldId="devinSensitiveWords"
              label={t('config_management.visual.sections.system.devin_sensitive_words_label')}
              description={t('config_management.visual.sections.system.devin_sensitive_words_hint')}
              // 命中即整行删除系统提示词：破坏性后果，常驻可见。
              keepDescriptionVisible
            >
              <StringListEditor
                value={values.devinSensitiveWords}
                disabled={disabled}
                placeholder={t(
                  'config_management.visual.sections.system.devin_sensitive_words_placeholder'
                )}
                inputAriaLabel={t(
                  'config_management.visual.sections.system.devin_sensitive_words_label'
                )}
                onChange={handleDevinSensitiveWordsChange}
              />
            </BlockSetting>
          </SettingList>

          <SettingList title={t('sensitive_replacements.group_title')}>
            <BlockSetting
              fieldId="devinSensitiveWordsReplace"
              label={t('sensitive_replacements.literal_title')}
              description={t('sensitive_replacements.literal_hint')}
            >
              {({ describedBy }) => (
                <LiteralReplacementEditor
                  value={values.devinSensitiveWordsReplace}
                  onChange={(devinSensitiveWordsReplace) =>
                    onChange({ devinSensitiveWordsReplace })
                  }
                  disabled={disabled}
                  describedBy={describedBy}
                />
              )}
            </BlockSetting>
            {/* 规则按序执行、无效模式被后端静默丢弃：不读就会配错，常驻可见。 */}
            <BlockSetting
              fieldId="devinSensitivePatternsReplace"
              label={t('sensitive_replacements.pattern_title')}
              description={t('sensitive_replacements.pattern_hint')}
              keepDescriptionVisible
            >
              {({ describedBy }) => (
                <PatternReplacementEditor
                  value={values.devinSensitivePatternsReplace}
                  onChange={(devinSensitivePatternsReplace) =>
                    onChange({ devinSensitivePatternsReplace })
                  }
                  disabled={disabled}
                  describedBy={describedBy}
                />
              )}
            </BlockSetting>
          </SettingList>
        </FieldStack>
      </Collapsible>

      <Collapsible label={t('config_management.visual.sections.headers.title')} defaultOpen={false}>
        <FieldStack>
          <SettingList title={t('config_management.visual.sections.headers.claude_title')}>
            <TextSetting
              fieldId="claudeHeaderUserAgent"
              size="lg"
              label={t('config_management.visual.sections.headers.user_agent')}
              placeholder="claude-cli/2.1.44 (external, sdk-cli)"
              value={values.claudeHeaderUserAgent}
              onChange={(claudeHeaderUserAgent) => onChange({ claudeHeaderUserAgent })}
              disabled={disabled}
            />
            <TextSetting
              fieldId="claudeHeaderPackageVersion"
              size="sm"
              label={t('config_management.visual.sections.headers.package_version')}
              placeholder="0.74.0"
              value={values.claudeHeaderPackageVersion}
              onChange={(claudeHeaderPackageVersion) => onChange({ claudeHeaderPackageVersion })}
              disabled={disabled}
            />
            <TextSetting
              fieldId="claudeHeaderRuntimeVersion"
              size="sm"
              label={t('config_management.visual.sections.headers.runtime_version')}
              placeholder="v24.3.0"
              value={values.claudeHeaderRuntimeVersion}
              onChange={(claudeHeaderRuntimeVersion) => onChange({ claudeHeaderRuntimeVersion })}
              disabled={disabled}
            />
            <TextSetting
              fieldId="claudeHeaderOs"
              size="sm"
              label={t('config_management.visual.sections.headers.os')}
              placeholder="MacOS"
              value={values.claudeHeaderOs}
              onChange={(claudeHeaderOs) => onChange({ claudeHeaderOs })}
              disabled={disabled}
            />
            <TextSetting
              fieldId="claudeHeaderArch"
              size="sm"
              label={t('config_management.visual.sections.headers.arch')}
              placeholder="arm64"
              value={values.claudeHeaderArch}
              onChange={(claudeHeaderArch) => onChange({ claudeHeaderArch })}
              disabled={disabled}
            />
            <TextSetting
              fieldId="claudeHeaderTimezone"
              label={t('config_management.visual.additions.claudeHeaderTimezone.label')}
              description={t('config_management.visual.additions.claudeHeaderTimezone.hint')}
              value={values.claudeHeaderTimezone}
              onChange={(claudeHeaderTimezone) => onChange({ claudeHeaderTimezone })}
              disabled={disabled}
              error={getValidationMessage(t, validationErrors?.claudeHeaderTimezone)}
            />
            <TextSetting
              fieldId="claudeHeaderTimeout"
              size="sm"
              label={t('config_management.visual.sections.headers.timeout')}
              placeholder="600"
              value={values.claudeHeaderTimeout}
              onChange={(claudeHeaderTimeout) => onChange({ claudeHeaderTimeout })}
              disabled={disabled}
            />
            <ToggleSetting
              fieldId="claudeHeaderStabilizeDeviceProfile"
              label={t('config_management.visual.sections.headers.stabilize_device')}
              description={t('config_management.visual.sections.headers.stabilize_device_desc')}
              checked={values.claudeHeaderStabilizeDeviceProfile}
              disabled={disabled}
              onChange={(claudeHeaderStabilizeDeviceProfile) =>
                onChange({ claudeHeaderStabilizeDeviceProfile })
              }
            />
          </SettingList>

          <SettingList title={t('config_management.visual.sections.headers.codex_title')}>
            <TextSetting
              fieldId="codexHeaderUserAgent"
              size="lg"
              label={t('config_management.visual.sections.headers.user_agent')}
              placeholder="codex_cli_rs/0.114.0 (Mac OS 14.2.0; x86_64) vscode/1.111.0"
              value={values.codexHeaderUserAgent}
              onChange={(codexHeaderUserAgent) => onChange({ codexHeaderUserAgent })}
              disabled={disabled}
            />
            <TextSetting
              fieldId="codexHeaderBetaFeatures"
              label={t('config_management.visual.sections.headers.beta_features')}
              placeholder="multi_agent"
              value={values.codexHeaderBetaFeatures}
              onChange={(codexHeaderBetaFeatures) => onChange({ codexHeaderBetaFeatures })}
              disabled={disabled}
            />
          </SettingList>
        </FieldStack>
      </Collapsible>
    </SectionCard>
  );
}
