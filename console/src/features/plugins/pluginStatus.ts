import type { PluginListEntry } from '@/types';

/**
 * One derived status per installed plugin, answering "is this plugin working?".
 *
 * Backend contract (CLIProxyAPI `ListPlugins`):
 * `effective_enabled = plugins.enabled && enabled && registered`, where `enabled` is
 * `plugins.configs.<id>.enabled` (false when the plugin has no config entry) and `registered`
 * means the plugin host currently has the plugin loaded.
 */
export type PluginStatusKind = 'active' | 'disabled' | 'paused' | 'notRegistered' | 'inactive';

/** Transient states shown in place of the derived status while a mutation is in flight. */
export type PluginTransientStatus = 'applying' | 'removing';

export type PluginRowStatus = PluginStatusKind | PluginTransientStatus;

export type PluginStatusTone = 'success' | 'muted' | 'warning' | 'pending';

type PluginStatusInput = Pick<PluginListEntry, 'enabled' | 'registered' | 'effectiveEnabled'>;

export function getPluginStatus(
  plugin: PluginStatusInput,
  pluginsEnabled: boolean
): PluginStatusKind {
  if (plugin.effectiveEnabled) return 'active';
  if (!plugin.enabled) return 'disabled';
  if (!pluginsEnabled) return 'paused';
  if (!plugin.registered) return 'notRegistered';
  // Unreachable with the current backend formula; kept so a future backend condition
  // cannot make an enabled, non-effective plugin look healthy.
  return 'inactive';
}

export const PLUGIN_STATUS_TONE: Record<PluginRowStatus, PluginStatusTone> = {
  active: 'success',
  disabled: 'muted',
  paused: 'muted',
  notRegistered: 'warning',
  inactive: 'warning',
  applying: 'pending',
  removing: 'pending',
};

export const PLUGIN_STATUS_LABEL_KEY: Record<PluginRowStatus, string> = {
  active: 'plugin_management.status_effective',
  disabled: 'plugin_management.status_disabled',
  paused: 'plugin_management.status_paused',
  notRegistered: 'plugin_management.not_registered',
  inactive: 'plugin_management.status_inactive',
  applying: 'plugin_management.status_applying',
  removing: 'plugin_management.status_removing',
};
