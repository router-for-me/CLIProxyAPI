import { useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Collapsible } from '@/components/ui/Collapsible';
import { Input } from '@/components/ui/Input';
import type { PluginStoreAuthRule } from '@/types/visualConfig';
import { CONFIG_TAB_ICONS, SECTION_INDEX_LABELS } from '../../constants';
import type { ConfigSectionProps } from '../../types';
import { SectionCard } from '../SectionCard';
import {
  Divider,
  FieldAnchor,
  FieldGrid,
  FieldGroup,
  FieldGroupHeading,
  FieldHint,
  FieldShell,
  FieldStack,
  ToggleRow,
} from '../fields/FieldPrimitives';
import { PluginStoreAuthEditor } from '../blocks/PluginStoreAuthEditor';
import { StringListEditor } from '../blocks/StringListEditor';

const Icon = CONFIG_TAB_ICONS.advanced;

/** 06 高级与实验：插件源、供应商敏感词、签名缓存与请求头默认值。 */
export function SectionAdvanced({ values, disabled, animateIn, onChange }: ConfigSectionProps) {
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
      description={t('config_management.visual.sections.advanced.description')}
      animateIn={animateIn}
    >
      <FieldStack>
        <Collapsible
          label={t('config_management.visual.sections.advanced.plugins_title')}
          defaultOpen={false}
        >
          <FieldStack>
            <FieldGrid>
              <FieldAnchor fieldId="pluginsEnabled">
                <ToggleRow
                  title={t('config_management.visual.sections.system.plugins_enabled')}
                  description={t('config_management.visual.sections.system.plugins_enabled_desc')}
                  checked={values.pluginsEnabled}
                  disabled={disabled}
                  onChange={(pluginsEnabled) => onChange({ pluginsEnabled })}
                />
              </FieldAnchor>
            </FieldGrid>

            <FieldAnchor fieldId="pluginStoreSources">
              <FieldGroup
                title={t('config_management.visual.sections.system.plugin_store_sources')}
                description={t(
                  'config_management.visual.sections.system.plugin_store_sources_desc'
                )}
              >
                <FieldShell
                  label={t('config_management.visual.sections.system.plugin_store_sources_label')}
                  hint={t('config_management.visual.sections.system.plugin_store_sources_hint')}
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
                </FieldShell>
              </FieldGroup>
            </FieldAnchor>

            <FieldAnchor fieldId="pluginStoreAuth">
              <FieldGroup
                title={t('config_management.visual.sections.system.plugin_store_auth')}
                description={t('config_management.visual.sections.system.plugin_store_auth_desc')}
              >
                <FieldHint>
                  {t('config_management.visual.sections.system.plugin_store_auth_hint')}
                </FieldHint>
                <PluginStoreAuthEditor
                  value={values.pluginStoreAuth}
                  disabled={disabled}
                  onChange={handlePluginStoreAuthChange}
                />
              </FieldGroup>
            </FieldAnchor>
          </FieldStack>
        </Collapsible>

        <Collapsible
          label={t('config_management.visual.sections.advanced.antigravity_title')}
          defaultOpen={false}
        >
          <FieldStack>
            <FieldAnchor fieldId="antigravitySensitiveWords">
              <FieldGroup
                title={t('config_management.visual.sections.system.antigravity_sensitive_words')}
                description={t(
                  'config_management.visual.sections.system.antigravity_sensitive_words_desc'
                )}
              >
                <FieldShell
                  label={t(
                    'config_management.visual.sections.system.antigravity_sensitive_words_label'
                  )}
                  hint={t(
                    'config_management.visual.sections.system.antigravity_sensitive_words_hint'
                  )}
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
                </FieldShell>
              </FieldGroup>
            </FieldAnchor>

            <Divider />
            <FieldGroupHeading
              title={t('config_management.visual.sections.advanced.signature_title')}
            />
            <FieldGrid>
              <FieldAnchor fieldId="antigravitySignatureCacheEnabled">
                <ToggleRow
                  title={t('config_management.visual.sections.system.antigravity_signature_cache')}
                  description={t(
                    'config_management.visual.sections.system.antigravity_signature_cache_desc'
                  )}
                  checked={values.antigravitySignatureCacheEnabled}
                  disabled={disabled}
                  onChange={(antigravitySignatureCacheEnabled) =>
                    onChange({ antigravitySignatureCacheEnabled })
                  }
                />
              </FieldAnchor>
              <FieldAnchor fieldId="antigravitySignatureBypassStrict">
                <ToggleRow
                  title={t('config_management.visual.sections.system.antigravity_signature_strict')}
                  description={t(
                    'config_management.visual.sections.system.antigravity_signature_strict_desc'
                  )}
                  checked={values.antigravitySignatureBypassStrict}
                  disabled={disabled}
                  onChange={(antigravitySignatureBypassStrict) =>
                    onChange({ antigravitySignatureBypassStrict })
                  }
                />
              </FieldAnchor>
            </FieldGrid>
          </FieldStack>
        </Collapsible>

        <Collapsible
          label={t('config_management.visual.sections.advanced.devin_title')}
          defaultOpen={false}
        >
          <FieldStack>
            <FieldAnchor fieldId="devinSensitiveWords">
              <FieldGroup
                title={t('config_management.visual.sections.system.devin_sensitive_words')}
                description={t(
                  'config_management.visual.sections.system.devin_sensitive_words_desc'
                )}
              >
                <FieldShell
                  label={t('config_management.visual.sections.system.devin_sensitive_words_label')}
                  hint={t('config_management.visual.sections.system.devin_sensitive_words_hint')}
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
                </FieldShell>
              </FieldGroup>
            </FieldAnchor>
          </FieldStack>
        </Collapsible>

        <Collapsible
          label={t('config_management.visual.sections.headers.title')}
          hint={t('config_management.visual.sections.headers.description')}
          defaultOpen={false}
        >
          <FieldStack>
            <FieldGroupHeading
              title={t('config_management.visual.sections.headers.claude_title')}
            />
            <FieldGrid>
              <FieldAnchor fieldId="claudeHeaderUserAgent">
                <Input
                  label={t('config_management.visual.sections.headers.user_agent')}
                  placeholder="claude-cli/2.1.44 (external, sdk-cli)"
                  value={values.claudeHeaderUserAgent}
                  onChange={(e) => onChange({ claudeHeaderUserAgent: e.target.value })}
                  disabled={disabled}
                />
              </FieldAnchor>
              <FieldAnchor fieldId="claudeHeaderPackageVersion">
                <Input
                  label={t('config_management.visual.sections.headers.package_version')}
                  placeholder="0.74.0"
                  value={values.claudeHeaderPackageVersion}
                  onChange={(e) => onChange({ claudeHeaderPackageVersion: e.target.value })}
                  disabled={disabled}
                />
              </FieldAnchor>
              <FieldAnchor fieldId="claudeHeaderRuntimeVersion">
                <Input
                  label={t('config_management.visual.sections.headers.runtime_version')}
                  placeholder="v24.3.0"
                  value={values.claudeHeaderRuntimeVersion}
                  onChange={(e) => onChange({ claudeHeaderRuntimeVersion: e.target.value })}
                  disabled={disabled}
                />
              </FieldAnchor>
              <FieldAnchor fieldId="claudeHeaderOs">
                <Input
                  label={t('config_management.visual.sections.headers.os')}
                  placeholder="MacOS"
                  value={values.claudeHeaderOs}
                  onChange={(e) => onChange({ claudeHeaderOs: e.target.value })}
                  disabled={disabled}
                />
              </FieldAnchor>
              <FieldAnchor fieldId="claudeHeaderArch">
                <Input
                  label={t('config_management.visual.sections.headers.arch')}
                  placeholder="arm64"
                  value={values.claudeHeaderArch}
                  onChange={(e) => onChange({ claudeHeaderArch: e.target.value })}
                  disabled={disabled}
                />
              </FieldAnchor>
              <FieldAnchor fieldId="claudeHeaderTimeout">
                <Input
                  label={t('config_management.visual.sections.headers.timeout')}
                  placeholder="600"
                  value={values.claudeHeaderTimeout}
                  onChange={(e) => onChange({ claudeHeaderTimeout: e.target.value })}
                  disabled={disabled}
                />
              </FieldAnchor>
            </FieldGrid>
            <FieldGrid>
              <FieldAnchor fieldId="claudeHeaderStabilizeDeviceProfile">
                <ToggleRow
                  title={t('config_management.visual.sections.headers.stabilize_device')}
                  description={t('config_management.visual.sections.headers.stabilize_device_desc')}
                  checked={values.claudeHeaderStabilizeDeviceProfile}
                  disabled={disabled}
                  onChange={(claudeHeaderStabilizeDeviceProfile) =>
                    onChange({ claudeHeaderStabilizeDeviceProfile })
                  }
                />
              </FieldAnchor>
            </FieldGrid>
            <Divider />
            <FieldGroupHeading title={t('config_management.visual.sections.headers.codex_title')} />
            <FieldGrid>
              <FieldAnchor fieldId="codexHeaderUserAgent">
                <Input
                  label={t('config_management.visual.sections.headers.user_agent')}
                  placeholder="codex_cli_rs/0.114.0 (Mac OS 14.2.0; x86_64) vscode/1.111.0"
                  value={values.codexHeaderUserAgent}
                  onChange={(e) => onChange({ codexHeaderUserAgent: e.target.value })}
                  disabled={disabled}
                />
              </FieldAnchor>
              <FieldAnchor fieldId="codexHeaderBetaFeatures">
                <Input
                  label={t('config_management.visual.sections.headers.beta_features')}
                  placeholder="multi_agent"
                  value={values.codexHeaderBetaFeatures}
                  onChange={(e) => onChange({ codexHeaderBetaFeatures: e.target.value })}
                  disabled={disabled}
                />
              </FieldAnchor>
            </FieldGrid>
          </FieldStack>
        </Collapsible>
      </FieldStack>
    </SectionCard>
  );
}
