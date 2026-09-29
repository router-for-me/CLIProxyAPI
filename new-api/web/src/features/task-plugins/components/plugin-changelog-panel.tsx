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
import { ExternalLink, History } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Markdown } from '@/components/ui/markdown'

import { findMarketplaceVersion } from '../lib/marketplace'
import { changelogLocale, fetchPluginChangelog } from '../lib/plugin-changelog'
import type { MarketplacePlugin } from '../types'

type PluginChangelogPanelProps = {
  indexUrl: string
  plugin: MarketplacePlugin
  version: string
}

export function PluginChangelogPanel(props: PluginChangelogPanelProps) {
  const { t, i18n } = useTranslation()
  const locale = changelogLocale(i18n.language)
  const entry = findMarketplaceVersion(props.plugin, props.version)
  const query = useQuery({
    queryKey: [
      'task-plugin-marketplace-changelog',
      props.indexUrl,
      props.plugin.key,
      props.version,
      entry?.path,
      entry?.sha256,
      locale,
    ],
    queryFn: ({ signal }) =>
      fetchPluginChangelog({ ...props, language: locale, signal }),
    staleTime: 15 * 60 * 1000,
    retry: false,
    meta: { errorToast: false },
  })

  if (query.isPending) return <LoadingState />
  if (query.isError && !query.data) {
    return (
      <ErrorState
        className='min-h-48'
        title={t('Could not load the changelog')}
        description={t(
          'The changelog could not be fetched or parsed. Please try again.'
        )}
        onRetry={() => void query.refetch()}
      />
    )
  }
  if (!query.data) {
    return (
      <EmptyState
        icon={History}
        className='min-h-48'
        title={t('No changelog for this version.')}
      />
    )
  }

  const changelog = query.data
  const categoryLabels = {
    Added: t('Added'),
    Changed: t('Changed'),
    Deprecated: t('Deprecated'),
    Removed: t('Removed'),
    Fixed: t('Fixed'),
    Security: t('Security'),
    Migration: t('Migration notes'),
  }
  return (
    <div className='min-w-0 space-y-4'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <span className='text-muted-foreground font-mono text-xs'>
          v{changelog.version}
        </span>
        <a
          href={changelog.sourceUrl}
          target='_blank'
          rel='noopener noreferrer'
          className='text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs'
        >
          <ExternalLink className='size-3.5' aria-hidden='true' />
          {t('View changelog source')}
        </a>
      </div>
      {locale !== 'en' && changelog.locale === 'en' && (
        <p role='status' className='text-muted-foreground text-xs'>
          {t(
            'This changelog is not available in your language. Showing English.'
          )}
        </p>
      )}
      {changelog.sections.map((section) => (
        <section key={section.category} className='space-y-2'>
          <h3 className='font-medium'>{categoryLabels[section.category]}</h3>
          <div lang={changelog.locale}>
            <Markdown baseUrl={changelog.sourceUrl}>
              {section.markdown}
            </Markdown>
          </div>
        </section>
      ))}
    </div>
  )
}
