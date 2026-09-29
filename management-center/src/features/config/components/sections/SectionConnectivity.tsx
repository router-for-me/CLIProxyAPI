import { useTranslation } from 'react-i18next';
import { Collapsible } from '@/components/ui/Collapsible';
import { Input } from '@/components/ui/Input';
import { CONFIG_TAB_ICONS, SECTION_INDEX_LABELS } from '../../constants';
import type { ConfigSectionProps } from '../../types';
import { SectionCard } from '../SectionCard';
import { Divider, FieldAnchor, FieldGrid, FieldStack, ToggleRow } from '../fields/FieldPrimitives';
import { ApiKeysField, HostField, PortField } from '../fields/sharedFields';
import { getValidationMessage } from '../blocks/shared';

const Icon = CONFIG_TAB_ICONS.connectivity;

/** 01 接入与认证：服务地址、端口、认证目录、API 密钥 + TLS / 远程管理折叠组。 */
export function SectionConnectivity({
  values,
  validationErrors,
  disabled,
  animateIn,
  onChange,
}: ConfigSectionProps) {
  const { t } = useTranslation();
  const portError = getValidationMessage(t, validationErrors?.port);

  return (
    <SectionCard
      indexLabel={SECTION_INDEX_LABELS.connectivity}
      icon={<Icon size={16} />}
      title={t('config_management.visual.sections.connectivity.title')}
      description={t('config_management.visual.sections.connectivity.description')}
      animateIn={animateIn}
    >
      <FieldStack>
        <FieldGrid>
          <HostField values={values} disabled={disabled} onChange={onChange} />
          <PortField values={values} disabled={disabled} onChange={onChange} error={portError} />
        </FieldGrid>

        <FieldAnchor fieldId="authDir">
          <Input
            label={t('config_management.visual.sections.auth.auth_dir')}
            placeholder="~/.cli-proxy-api"
            value={values.authDir}
            onChange={(e) => onChange({ authDir: e.target.value })}
            disabled={disabled}
            hint={t('config_management.visual.sections.auth.auth_dir_hint')}
          />
        </FieldAnchor>

        <ApiKeysField values={values} disabled={disabled} onChange={onChange} />

        <Collapsible
          label={t('config_management.visual.sections.tls.title')}
          hint={t('config_management.visual.sections.tls.description')}
          defaultOpen={false}
        >
          <FieldStack>
            <FieldAnchor fieldId="tlsEnable">
              <ToggleRow
                title={t('config_management.visual.sections.tls.enable')}
                description={t('config_management.visual.sections.tls.enable_desc')}
                checked={values.tlsEnable}
                disabled={disabled}
                onChange={(tlsEnable) => onChange({ tlsEnable })}
              />
            </FieldAnchor>

            {values.tlsEnable ? (
              <>
                <Divider />
                <FieldGrid>
                  <FieldAnchor fieldId="tlsCert">
                    <Input
                      label={t('config_management.visual.sections.tls.cert')}
                      placeholder="/path/to/cert.pem"
                      value={values.tlsCert}
                      onChange={(e) => onChange({ tlsCert: e.target.value })}
                      disabled={disabled}
                    />
                  </FieldAnchor>
                  <FieldAnchor fieldId="tlsKey">
                    <Input
                      label={t('config_management.visual.sections.tls.key')}
                      placeholder="/path/to/key.pem"
                      value={values.tlsKey}
                      onChange={(e) => onChange({ tlsKey: e.target.value })}
                      disabled={disabled}
                    />
                  </FieldAnchor>
                </FieldGrid>
              </>
            ) : null}
          </FieldStack>
        </Collapsible>

        <Collapsible
          label={t('config_management.visual.sections.remote.title')}
          hint={t('config_management.visual.sections.remote.description')}
          defaultOpen={false}
        >
          <FieldStack>
            <FieldGrid>
              <FieldAnchor fieldId="rmAllowRemote">
                <ToggleRow
                  title={t('config_management.visual.sections.remote.allow_remote')}
                  description={t('config_management.visual.sections.remote.allow_remote_desc')}
                  checked={values.rmAllowRemote}
                  disabled={disabled}
                  onChange={(rmAllowRemote) => onChange({ rmAllowRemote })}
                />
              </FieldAnchor>
              <FieldAnchor fieldId="rmDisableControlPanel">
                <ToggleRow
                  title={t('config_management.visual.sections.remote.disable_panel')}
                  description={t('config_management.visual.sections.remote.disable_panel_desc')}
                  checked={values.rmDisableControlPanel}
                  disabled={disabled}
                  onChange={(rmDisableControlPanel) => onChange({ rmDisableControlPanel })}
                />
              </FieldAnchor>
              <FieldAnchor fieldId="rmDisableAutoUpdatePanel">
                <ToggleRow
                  title={t('config_management.visual.sections.remote.disable_auto_update_panel')}
                  description={t(
                    'config_management.visual.sections.remote.disable_auto_update_panel_desc'
                  )}
                  checked={values.rmDisableAutoUpdatePanel}
                  disabled={disabled}
                  onChange={(rmDisableAutoUpdatePanel) => onChange({ rmDisableAutoUpdatePanel })}
                />
              </FieldAnchor>
            </FieldGrid>
            <FieldGrid>
              <FieldAnchor fieldId="rmSecretKey">
                <Input
                  label={t('config_management.visual.sections.remote.secret_key')}
                  type="password"
                  placeholder={t('config_management.visual.sections.remote.secret_key_placeholder')}
                  value={values.rmSecretKey}
                  onChange={(e) => onChange({ rmSecretKey: e.target.value })}
                  disabled={disabled}
                />
              </FieldAnchor>
              <FieldAnchor fieldId="rmPanelRepo">
                <Input
                  label={t('config_management.visual.sections.remote.panel_repo')}
                  placeholder="https://github.com/router-for-me/Cli-Proxy-API-Management-Center"
                  value={values.rmPanelRepo}
                  onChange={(e) => onChange({ rmPanelRepo: e.target.value })}
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
