/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useQuery } from '@tanstack/react-query'
import {
  ArrowUpCircle,
  CheckCircle2,
  Download,
  TriangleAlert,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { getChannelTypeLabel } from '@/features/channels/lib'
import { resolveLocalizedText } from '@/lib/localized-text'

import {
  findMarketplaceVersion,
  marketplaceBuiltInVersion,
  resolvePluginSourceUrl,
  type InstallState,
} from '../lib/marketplace'
import { fetchPluginIconDataUri } from '../lib/plugin-icon-file'
import type { MarketplacePlugin, TaskPluginListItem } from '../types'
import { PluginIcon } from './plugin-icon'
import { PluginWebsiteLink } from './plugin-website-link'

type MarketplacePluginCardProps = {
  plugin: MarketplacePlugin
  /** Index URL the plugin came from; sidecar logos resolve against it. */
  indexUrl?: string
  installState: InstallState
  installed?: TaskPluginListItem
  onInstall: () => void
}

export function MarketplacePluginCard(props: MarketplacePluginCardProps) {
  const { t, i18n } = useTranslation()
  const plugin = props.plugin
  const description = resolveLocalizedText(plugin.description, i18n.language)
  const channelTypes = plugin.channelTypes ?? []
  const latestEntry = findMarketplaceVersion(plugin, plugin.latest)
  const labelClass = 'text-muted-foreground text-[11px] font-medium select-none'
  const builtInVersion = marketplaceBuiltInVersion(props.installed)
  // Same-origin-as-index rule as the source itself: a logo is only ever
  // fetched from the repository the administrator chose to trust.
  const iconUrl =
    plugin.iconFile && props.indexUrl
      ? resolvePluginSourceUrl(props.indexUrl, plugin.iconFile.path)
      : null

  return (
    <div className='flex h-full flex-col gap-2.5 rounded-xl border p-3'>
      <div className='flex items-start justify-between gap-2'>
        <div className='flex min-w-0 flex-1 items-center gap-2.5'>
          <span className='mt-0.5 shrink-0'>
            {iconUrl ? (
              <MarketplacePluginLogo
                plugin={plugin}
                iconUrl={iconUrl}
                sha256={plugin.iconFile?.sha256}
              />
            ) : (
              <PluginIcon plugin={plugin} size={20} />
            )}
          </span>
          <div className='min-w-0'>
            <div className='truncate text-sm font-medium'>{plugin.name}</div>
            <div className='text-muted-foreground truncate font-mono text-xs'>
              {plugin.key}
            </div>
          </div>
        </div>
        <InstallStateBadge state={props.installState} />
      </div>

      <PluginWebsiteLink website={plugin.website} />

      {description ? (
        <p className='text-muted-foreground line-clamp-2 text-xs'>
          {description}
        </p>
      ) : null}

      <div className='grid grid-cols-3 gap-x-3 gap-y-1'>
        <div className='min-w-0'>
          <div className={labelClass}>{t('Latest version')}</div>
          <div className='truncate font-mono text-xs'>{plugin.latest}</div>
        </div>
        <div className='min-w-0'>
          <div className={labelClass}>{t('Channel type')}</div>
          <div className='truncate text-xs'>
            {channelTypes.length > 0
              ? t(getChannelTypeLabel(channelTypes[0]))
              : t('Task Plugin')}
          </div>
        </div>
        <div className='min-w-0'>
          <div className={labelClass}>{t('Models')}</div>
          <div className='truncate text-xs'>{plugin.models?.length ?? 0}</div>
        </div>
      </div>

      {builtInVersion && (
        <div className='text-xs'>
          <span className={labelClass}>{t('Versions')}</span>{' '}
          <span className='font-mono'>
            {t('Built-in v{{factory}} / marketplace v{{market}}', {
              factory: builtInVersion,
              market: plugin.latest,
            })}
          </span>
        </div>
      )}

      {!latestEntry?.sha256 && (
        <div className='text-destructive flex items-center gap-1 text-xs'>
          <TriangleAlert className='size-3 shrink-0' aria-hidden='true' />
          {t('No integrity hash')}
        </div>
      )}

      <div className='mt-auto border-t pt-2'>
        <Button
          size='sm'
          variant={
            props.installState.status === 'up_to_date' ? 'outline' : 'default'
          }
          className='w-full'
          onClick={props.onInstall}
        >
          <Download />
          {t('Install')}
        </Button>
      </div>
    </div>
  )
}

function InstallStateBadge({ state }: { state: InstallState }) {
  const { t } = useTranslation()
  if (state.status === 'not_installed') {
    return <Badge variant='outline'>{t('Not installed')}</Badge>
  }
  if (state.status === 'up_to_date') {
    return (
      <Badge variant='secondary'>
        <CheckCircle2 aria-hidden='true' />
        {t('Up to date')}
      </Badge>
    )
  }
  if (state.status === 'upgradable') {
    return (
      <Badge
        aria-label={t('Upgrade available: v{{installed}} to v{{latest}}', {
          installed: state.installedVersion,
          latest: state.latestVersion,
        })}
      >
        <ArrowUpCircle aria-hidden='true' />
        <span className='font-mono'>
          v{state.installedVersion} → v{state.latestVersion}
        </span>
      </Badge>
    )
  }
  return (
    <Badge variant='destructive'>
      {t('Installed v{{installed}} not listed', {
        installed: state.installedVersion,
      })}
    </Badge>
  )
}

type MarketplacePluginLogoProps = {
  plugin: MarketplacePlugin
  iconUrl: string
  sha256?: string
}

/**
 * Loads the sidecar logo through fetch and shows it as a data URI. Linking the
 * raw file with `<img src>` fails on hosts that serve SVG as text/plain with
 * nosniff (raw.githubusercontent.com does), and fetching also lets the index
 * digest be checked before anything is displayed. Until the bytes arrive, or if
 * they never do, the manifest icon or text avatar renders instead.
 */
function MarketplacePluginLogo(props: MarketplacePluginLogoProps) {
  const iconQuery = useQuery({
    queryKey: [
      'task-plugin-marketplace-icon',
      props.iconUrl,
      props.sha256 ?? '',
    ],
    queryFn: () =>
      fetchPluginIconDataUri(props.iconUrl, { sha256: props.sha256 }),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
    meta: { errorToast: false },
  })
  return (
    <PluginIcon
      plugin={{ ...props.plugin, iconSrc: iconQuery.data ?? undefined }}
      size={20}
    />
  )
}
