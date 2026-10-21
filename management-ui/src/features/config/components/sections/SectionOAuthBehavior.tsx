import { useTranslation } from 'react-i18next';
import { Collapsible } from '@/components/ui/Collapsible';
import type { ConfigSectionProps } from '../../types';
import { CodexLiveICEServersEditor } from '../blocks/CodexLiveICEServersEditor';
import { getValidationMessage } from '../blocks/shared';
import {
  BlockSetting,
  FieldStack,
  SettingList,
  TextSetting,
  ToggleSetting,
} from '../fields/FieldPrimitives';

/** OAuth/file-backed provider behavior, independent of API-key provider configuration. */
export function SectionOAuthBehavior({
  values,
  validationErrors,
  disabled,
  onChange,
}: ConfigSectionProps) {
  const { t } = useTranslation();
  return (
    <Collapsible
      label={t('config_management.visual.additions.oauthTitle')}
      hint={t('config_management.visual.additions.oauthHint')}
    >
      <FieldStack>
        <SettingList title={t('config_management.visual.additions.claudeTitle')}>
          <ToggleSetting
            fieldId="claudeModelLevelCooling"
            label={t('config_management.visual.additions.claudeModelLevelCooling.label')}
            description={t('config_management.visual.additions.claudeModelLevelCooling.hint')}
            checked={values.claudeModelLevelCooling}
            disabled={disabled}
            onChange={(claudeModelLevelCooling) => onChange({ claudeModelLevelCooling })}
          />
          <ToggleSetting
            fieldId="claudeDisableCloakMode"
            label={t('config_management.visual.additions.claudeDisableCloakMode.label')}
            description={t('config_management.visual.additions.claudeDisableCloakMode.hint')}
            checked={values.claudeDisableCloakMode}
            disabled={disabled}
            onChange={(claudeDisableCloakMode) => onChange({ claudeDisableCloakMode })}
          />
          <ToggleSetting
            fieldId="claudeCodeDisableCloakingModelList"
            label={t('config_management.visual.additions.claudeCodeDisableCloakingModelList.label')}
            description={t(
              'config_management.visual.additions.claudeCodeDisableCloakingModelList.hint'
            )}
            checked={values.claudeCodeDisableCloakingModelList}
            disabled={disabled}
            onChange={(claudeCodeDisableCloakingModelList) =>
              onChange({ claudeCodeDisableCloakingModelList })
            }
          />
        </SettingList>

        <SettingList title={t('config_management.visual.additions.codexTitle')}>
          <ToggleSetting
            fieldId="codexDisableCloaking"
            label={t('config_management.visual.additions.codexDisableCloaking.label')}
            description={t('config_management.visual.additions.codexDisableCloaking.hint')}
            checked={values.codexDisableCloaking}
            disabled={disabled}
            onChange={(codexDisableCloaking) => onChange({ codexDisableCloaking })}
          />
          <ToggleSetting
            fieldId="codexModelLevelCooling"
            label={t('config_management.visual.additions.codexModelLevelCooling.label')}
            description={t('config_management.visual.additions.codexModelLevelCooling.hint')}
            checked={values.codexModelLevelCooling}
            disabled={disabled}
            onChange={(codexModelLevelCooling) => onChange({ codexModelLevelCooling })}
          />
          <ToggleSetting
            fieldId="codexStreamBootstrapBuffering"
            label={t('config_management.visual.additions.codexStreamBootstrapBuffering.label')}
            description={t('config_management.visual.additions.codexStreamBootstrapBuffering.hint')}
            checked={values.codexStreamBootstrapBuffering}
            disabled={disabled}
            onChange={(codexStreamBootstrapBuffering) =>
              onChange({ codexStreamBootstrapBuffering })
            }
          />
          <TextSetting
            fieldId="codexStreamBootstrapTimeout"
            size="sm"
            label={t('config_management.visual.additions.codexStreamBootstrapTimeout.label')}
            description={t('config_management.visual.additions.codexStreamBootstrapTimeout.hint')}
            value={values.codexStreamBootstrapTimeout}
            onChange={(codexStreamBootstrapTimeout) => onChange({ codexStreamBootstrapTimeout })}
            disabled={disabled}
            error={getValidationMessage(t, validationErrors?.codexStreamBootstrapTimeout)}
          />
          <ToggleSetting
            fieldId="codexOptimizeMultiAgentV2"
            label={t('config_management.visual.additions.codexOptimizeMultiAgentV2.label')}
            description={t('config_management.visual.additions.codexOptimizeMultiAgentV2.hint')}
            checked={values.codexOptimizeMultiAgentV2}
            disabled={disabled}
            onChange={(codexOptimizeMultiAgentV2) => onChange({ codexOptimizeMultiAgentV2 })}
          />
          <ToggleSetting
            fieldId="codexOrphanDelegationCompatibility"
            label={t('config_management.visual.additions.codexOrphanDelegationCompatibility.label')}
            description={t(
              'config_management.visual.additions.codexOrphanDelegationCompatibility.hint'
            )}
            checked={values.codexOrphanDelegationCompatibility}
            disabled={disabled}
            onChange={(codexOrphanDelegationCompatibility) =>
              onChange({ codexOrphanDelegationCompatibility })
            }
          />
          <ToggleSetting
            fieldId="codexResponseSteering"
            label={t('config_management.visual.additions.codexResponseSteering.label')}
            description={t('config_management.visual.additions.codexResponseSteering.hint')}
            checked={values.codexResponseSteering}
            disabled={disabled}
            onChange={(codexResponseSteering) => onChange({ codexResponseSteering })}
          />
        </SettingList>

        <SettingList title={t('config_management.visual.additions.antigravityTitle')}>
          <ToggleSetting
            fieldId="antigravityConnectionPoolEnabled"
            wide
            label={t('config_management.visual.additions.antigravityConnectionPoolEnabled.label')}
            description={t(
              'config_management.visual.additions.antigravityConnectionPoolEnabled.hint'
            )}
            checked={values.antigravityConnectionPoolEnabled}
            disabled={disabled}
            onChange={(antigravityConnectionPoolEnabled) =>
              onChange({ antigravityConnectionPoolEnabled })
            }
          />
          <TextSetting
            fieldId="antigravityConnectionPoolIdleTimeout"
            size="sm"
            label={t(
              'config_management.visual.additions.antigravityConnectionPoolIdleTimeout.label'
            )}
            description={t(
              'config_management.visual.additions.antigravityConnectionPoolIdleTimeout.hint'
            )}
            value={values.antigravityConnectionPoolIdleTimeout}
            onChange={(antigravityConnectionPoolIdleTimeout) =>
              onChange({ antigravityConnectionPoolIdleTimeout })
            }
            disabled={disabled}
            error={getValidationMessage(t, validationErrors?.antigravityConnectionPoolIdleTimeout)}
          />
          <TextSetting
            fieldId="antigravityConnectionPoolMaxIdleConnsPerHost"
            size="sm"
            label={t(
              'config_management.visual.additions.antigravityConnectionPoolMaxIdleConnsPerHost.label'
            )}
            description={t(
              'config_management.visual.additions.antigravityConnectionPoolMaxIdleConnsPerHost.hint'
            )}
            type="number"
            value={values.antigravityConnectionPoolMaxIdleConnsPerHost}
            onChange={(antigravityConnectionPoolMaxIdleConnsPerHost) =>
              onChange({ antigravityConnectionPoolMaxIdleConnsPerHost })
            }
            disabled={disabled}
            error={getValidationMessage(
              t,
              validationErrors?.antigravityConnectionPoolMaxIdleConnsPerHost
            )}
          />
        </SettingList>

        <SettingList title={t('config_management.visual.additions.xaiTitle')}>
          <ToggleSetting
            fieldId="xaiInjectXSearch"
            label={t('config_management.visual.additions.xaiInjectXSearch.label')}
            description={t('config_management.visual.additions.xaiInjectXSearch.hint')}
            checked={values.xaiInjectXSearch}
            disabled={disabled}
            onChange={(xaiInjectXSearch) => onChange({ xaiInjectXSearch })}
          />
        </SettingList>

        <SettingList
          title={t('config_management.visual.additions.liveRelayTitle')}
          description={t('config_management.visual.additions.liveRelayHint')}
        >
          <ToggleSetting
            fieldId="codexLiveMediaRelayEnabled"
            label={t('config_management.visual.additions.codexLiveMediaRelayEnabled.label')}
            description={t('config_management.visual.additions.codexLiveMediaRelayEnabled.hint')}
            checked={values.codexLiveMediaRelayEnabled}
            disabled={disabled}
            onChange={(codexLiveMediaRelayEnabled) => onChange({ codexLiveMediaRelayEnabled })}
          />
          <TextSetting
            fieldId="codexLiveMediaRelayMaxSessions"
            size="sm"
            label={t('config_management.visual.additions.codexLiveMediaRelayMaxSessions.label')}
            description={t(
              'config_management.visual.additions.codexLiveMediaRelayMaxSessions.hint'
            )}
            type="number"
            value={values.codexLiveMediaRelayMaxSessions}
            onChange={(codexLiveMediaRelayMaxSessions) =>
              onChange({ codexLiveMediaRelayMaxSessions })
            }
            disabled={disabled}
            error={getValidationMessage(t, validationErrors?.codexLiveMediaRelayMaxSessions)}
          />
          <ToggleSetting
            fieldId="codexLiveMediaRelayDisablePrivateRemoteIPs"
            label={t(
              'config_management.visual.additions.codexLiveMediaRelayDisablePrivateRemoteIPs.label'
            )}
            description={t(
              'config_management.visual.additions.codexLiveMediaRelayDisablePrivateRemoteIPs.hint'
            )}
            checked={values.codexLiveMediaRelayDisablePrivateRemoteIPs}
            disabled={disabled}
            onChange={(codexLiveMediaRelayDisablePrivateRemoteIPs) =>
              onChange({ codexLiveMediaRelayDisablePrivateRemoteIPs })
            }
          />
          <TextSetting
            fieldId="codexLiveMediaRelayPublicIP"
            label={t('config_management.visual.additions.codexLiveMediaRelayPublicIP.label')}
            description={t('config_management.visual.additions.codexLiveMediaRelayPublicIP.hint')}
            value={values.codexLiveMediaRelayPublicIP}
            onChange={(codexLiveMediaRelayPublicIP) => onChange({ codexLiveMediaRelayPublicIP })}
            disabled={disabled}
            error={getValidationMessage(t, validationErrors?.codexLiveMediaRelayPublicIP)}
          />
          <TextSetting
            fieldId="codexLiveMediaRelayUDPPortMin"
            size="sm"
            label={t('config_management.visual.additions.codexLiveMediaRelayUDPPortMin.label')}
            description={t('config_management.visual.additions.codexLiveMediaRelayUDPPortMin.hint')}
            type="number"
            value={values.codexLiveMediaRelayUDPPortMin}
            onChange={(codexLiveMediaRelayUDPPortMin) =>
              onChange({ codexLiveMediaRelayUDPPortMin })
            }
            disabled={disabled}
            error={getValidationMessage(t, validationErrors?.codexLiveMediaRelayUDPPortMin)}
          />
          <TextSetting
            fieldId="codexLiveMediaRelayUDPPortMax"
            size="sm"
            label={t('config_management.visual.additions.codexLiveMediaRelayUDPPortMax.label')}
            description={t('config_management.visual.additions.codexLiveMediaRelayUDPPortMax.hint')}
            type="number"
            value={values.codexLiveMediaRelayUDPPortMax}
            onChange={(codexLiveMediaRelayUDPPortMax) =>
              onChange({ codexLiveMediaRelayUDPPortMax })
            }
            disabled={disabled}
            error={getValidationMessage(t, validationErrors?.codexLiveMediaRelayUDPPortMax)}
          />
          <BlockSetting
            fieldId="codexLiveMediaRelayICEServers"
            label={t('config_management.visual.additions.codexLiveMediaRelayICEServers.label')}
            description={t('config_management.visual.additions.codexLiveMediaRelayICEServers.hint')}
            error={getValidationMessage(t, validationErrors?.codexLiveMediaRelayICEServers)}
          >
            {({ describedBy, invalid }) => (
              <CodexLiveICEServersEditor
                value={values.codexLiveMediaRelayICEServers}
                disabled={disabled}
                describedBy={describedBy}
                invalid={invalid}
                onChange={(codexLiveMediaRelayICEServers) =>
                  onChange({ codexLiveMediaRelayICEServers })
                }
              />
            )}
          </BlockSetting>
        </SettingList>
      </FieldStack>
    </Collapsible>
  );
}
