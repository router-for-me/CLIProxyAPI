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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Download } from 'lucide-react'
import { lazy, Suspense, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Spinner } from '@/components/ui/spinner'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import {
  activateTaskPlugin,
  getTaskPlugin,
  installMarketplacePlugin,
} from '../api'
import {
  findMarketplaceVersion,
  resolvePluginSourceUrl,
  type InstallState,
} from '../lib/marketplace'
import { fetchPluginIconDataUri } from '../lib/plugin-icon-file'
import {
  computeSourceSha256,
  fetchPluginSourceText,
  PluginSourceFetchError,
} from '../lib/plugin-url'
import type { MarketplacePlugin, MarketplaceSource } from '../types'
import { JavaScriptViewer } from './javascript-viewer'
import { MarketplaceCapabilities } from './marketplace-capabilities'
import { PluginIcon } from './plugin-icon'
import { PluginIntegrityCheck } from './plugin-integrity-check'
import { SourceDiff } from './source-diff'

const PluginChangelogPanel = lazy(() =>
  import('./plugin-changelog-panel').then((module) => ({
    default: module.PluginChangelogPanel,
  }))
)

export type MarketplaceInstallTarget = {
  source: MarketplaceSource
  plugin: MarketplacePlugin
  version: string
  installState: InstallState
}

type MarketplaceInstallDialogProps = {
  target: MarketplaceInstallTarget | null
  onOpenChange: (open: boolean) => void
}

export function MarketplaceInstallDialog(props: MarketplaceInstallDialogProps) {
  if (!props.target) return null
  return (
    <MarketplaceInstallContent
      key={`${props.target.source.index_url}-${props.target.plugin.key}-${props.target.version}`}
      target={props.target}
      onOpenChange={props.onOpenChange}
    />
  )
}

function MarketplaceInstallContent(
  props: MarketplaceInstallDialogProps & { target: MarketplaceInstallTarget }
) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const target = props.target
  const [selectedVersion, setSelectedVersion] = useState(target.version)
  const pluginKey = target.plugin.key
  const entry = findMarketplaceVersion(target.plugin, selectedVersion)
  const hasInstalledPlugin = target.installState.status !== 'not_installed'
  const [reviewTab, setReviewTab] = useState('details')

  const sourceQuery = useQuery({
    queryKey: [
      'task-plugin-marketplace-source',
      target.source.index_url,
      pluginKey,
      selectedVersion,
      entry?.path,
      entry?.sha256,
    ],
    enabled: Boolean(entry),
    retry: false,
    meta: { errorToast: false },
    queryFn: async () => {
      if (!entry) throw new Error('missing marketplace entry')
      const url = resolvePluginSourceUrl(target.source.index_url, entry.path)
      if (!url) {
        throw new Error(
          t('This plugin path does not resolve within the source repository.')
        )
      }
      const text = await fetchPluginSourceText(url)
      // Computed for display only. The upload request carries the index hash and
      // the server re-hashes what it received, so a tampered browser cannot pass
      // a mismatched source off as verified.
      const digest = await computeSourceSha256(text)
      // The logo is cosmetic: a missing or malformed icon file must never block
      // the install, so a failed fetch simply yields no icon.
      const iconUrl = target.plugin.iconFile
        ? resolvePluginSourceUrl(
            target.source.index_url,
            target.plugin.iconFile.path
          )
        : null
      const icon = iconUrl
        ? await fetchPluginIconDataUri(iconUrl, {
            sha256: target.plugin.iconFile?.sha256,
          })
        : null
      return { url, text, digest, icon }
    },
  })

  // The installed source is the diff baseline for an upgrade.
  const installedQuery = useQuery({
    queryKey: ['task-plugin', pluginKey],
    queryFn: () => getTaskPlugin(pluginKey),
    enabled: hasInstalledPlugin,
  })

  const installMutation = useMutation({
    mutationFn: async () => {
      if (
        !entry ||
        !sourceQuery.data ||
        sourceQuery.isFetching ||
        sourceQuery.isError ||
        digestMismatch
      ) {
        throw new Error('source not fetched')
      }
      const detail = await installMarketplacePlugin({
        source: sourceQuery.data.text,
        sourceSha256: entry.sha256,
        remark: `${target.source.name} v${selectedVersion}`,
        icon: sourceQuery.data.icon ?? undefined,
      })
      if (detail.plugin && !detail.plugin.active) {
        await activateTaskPlugin(detail.meta.key, detail.meta.version)
      }
      return detail
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['task-plugins'] })
      queryClient.invalidateQueries({ queryKey: ['task-plugin', pluginKey] })
      queryClient.invalidateQueries({
        queryKey: ['task-plugin-versions', pluginKey],
      })
    },
    onSuccess: (detail) => {
      toast.success(
        t('Installed {{name}} v{{version}}', {
          name: detail.meta.name,
          version: detail.meta.version,
        })
      )
      props.onOpenChange(false)
    },
  })

  const fetchError = sourceQuery.error
  let fetchErrorMessage = ''
  if (fetchError instanceof PluginSourceFetchError) {
    fetchErrorMessage =
      fetchError.reason === 'too_large'
        ? t('Plugin source exceeds the 8 MiB limit.')
        : t(
            'Could not fetch the plugin source from this browser. The host may block cross-origin requests or be unreachable.'
          )
  } else if (fetchError) {
    fetchErrorMessage = fetchError.message
  }

  const digestMismatch = Boolean(
    entry?.sha256 &&
    sourceQuery.data?.digest &&
    sourceQuery.data.digest.toLowerCase() !== entry.sha256.toLowerCase()
  )

  let confirmLabel = t('Install and enable')
  if (installMutation.isPending) confirmLabel = t('Installing...')

  const sourcePreview = sourceQuery.data ? (
    <JavaScriptViewer
      value={sourceQuery.data.text}
      className='bg-muted/30 h-[min(22rem,35vh)] min-w-0 overflow-hidden rounded-lg border text-xs'
    />
  ) : null

  const sourceStatus = (
    <>
      {sourceQuery.isFetching && (
        <div className='flex items-center gap-2 text-sm'>
          <Spinner />
          {t('Fetching plugin source...')}
        </div>
      )}
      {fetchErrorMessage && (
        <p role='alert' className='text-destructive text-sm'>
          {fetchErrorMessage}
        </p>
      )}
    </>
  )

  return (
    <Dialog
      open
      initialFocus={false}
      onOpenChange={props.onOpenChange}
      contentClassName='sm:max-w-3xl'
      headerClassName='gap-1.5 pr-6'
      bodyClassName='space-y-3'
      descriptionClassName='pl-12 text-xs break-words'
      title={
        <span className='flex min-w-0 items-center gap-3'>
          <PluginIcon
            plugin={{
              ...target.plugin,
              iconSrc: sourceQuery.data?.icon ?? undefined,
            }}
            size={36}
          />
          <span className='min-w-0 text-lg font-semibold break-words'>
            {t('Install {{name}}', { name: target.plugin.name })}
          </span>
        </span>
      }
      description={t('{{key}} · from {{source}}', {
        key: pluginKey,
        source: target.source.name,
      })}
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button
            disabled={
              !entry ||
              !sourceQuery.data ||
              sourceQuery.isFetching ||
              sourceQuery.isError ||
              digestMismatch ||
              installMutation.isPending
            }
            onClick={() => installMutation.mutate()}
          >
            <Download />
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className='space-y-2'>
        <Combobox
          aria-label={t('Select version')}
          value={selectedVersion}
          disabled={
            target.plugin.versions.length <= 1 || installMutation.isPending
          }
          options={target.plugin.versions.map((version) => {
            const labels = [version.version]
            if (version.version === target.plugin.latest) {
              labels.push(t('Latest version'))
            }
            if (
              target.installState.status !== 'not_installed' &&
              version.version === target.installState.installedVersion
            ) {
              labels.push(t('Active version'))
            }
            return { value: version.version, label: labels.join(' · ') }
          })}
          onValueChange={(version) => {
            if (
              !version ||
              version === selectedVersion ||
              installMutation.isPending
            ) {
              return
            }
            installMutation.reset()
            setSelectedVersion(version)
            if (
              target.installState.status !== 'not_installed' &&
              reviewTab !== 'changelog'
            ) {
              setReviewTab(
                version === target.installState.installedVersion
                  ? 'details'
                  : 'diff'
              )
            }
          }}
        />
        {target.installState.status !== 'not_installed' && (
          <p className='text-muted-foreground text-xs'>
            {t('Current v{{from}} → install v{{to}}', {
              from: target.installState.installedVersion,
              to: selectedVersion,
            })}
          </p>
        )}
      </div>
      <Tabs
        value={reviewTab}
        onValueChange={setReviewTab}
        className='min-w-0 gap-3'
      >
        <div className='min-w-0 overflow-x-auto'>
          <TabsList
            className='w-full min-w-max'
            aria-label={t('Plugin details')}
          >
            <TabsTrigger value='details'>{t('Plugin details')}</TabsTrigger>
            {hasInstalledPlugin && (
              <TabsTrigger value='diff'>{t('Version differences')}</TabsTrigger>
            )}
            <TabsTrigger value='source'>{t('Full source')}</TabsTrigger>
            <TabsTrigger value='changelog'>{t('Changelog')}</TabsTrigger>
          </TabsList>
        </div>
        {hasInstalledPlugin && (
          <TabsContent value='diff' className='min-w-0 space-y-2'>
            {sourceStatus}
            {installedQuery.isLoading && (
              <div className='flex items-center gap-2 text-sm'>
                <Spinner />
                {t('Loading installed source...')}
              </div>
            )}
            {installedQuery.error && (
              <p role='alert' className='text-destructive text-sm'>
                {installedQuery.error.message}
              </p>
            )}
            {sourceQuery.data &&
              !sourceQuery.isError &&
              installedQuery.data &&
              !installedQuery.isError && (
                <SourceDiff
                  before={installedQuery.data.source}
                  after={sourceQuery.data.text}
                  className='bg-muted/30 max-h-[min(22rem,35vh)]'
                />
              )}
          </TabsContent>
        )}
        <TabsContent value='details' className='min-w-0 space-y-2'>
          {sourceStatus}
          <MarketplaceCapabilities
            plugin={target.plugin}
            version={entry}
            source={sourceQuery.data?.text}
            onRetry={() => {
              void sourceQuery.refetch()
            }}
          />
        </TabsContent>
        <TabsContent value='changelog' className='min-w-0 space-y-2'>
          {reviewTab === 'changelog' && (
            <Suspense fallback={<LoadingState />}>
              <PluginChangelogPanel
                indexUrl={target.source.index_url}
                plugin={target.plugin}
                version={selectedVersion}
              />
            </Suspense>
          )}
        </TabsContent>
        <TabsContent value='source' className='min-w-0 space-y-2'>
          {sourceStatus}
          {!sourceQuery.isError && sourcePreview}
        </TabsContent>
      </Tabs>

      <Alert className='border-amber-500/20 bg-amber-500/5 py-2 [&>svg]:text-amber-600 dark:[&>svg]:text-amber-400'>
        <AlertTriangle />
        <AlertDescription className='text-foreground/80 text-xs leading-relaxed'>
          {t(
            'Plugins can access channel credentials. Review the source and differences before installing.'
          )}
        </AlertDescription>
      </Alert>
      <PluginIntegrityCheck
        compact
        sourceAvailable={Boolean(sourceQuery.data)}
        expected={entry?.sha256}
        digest={sourceQuery.data?.digest}
        isLoading={sourceQuery.isFetching}
      />

      {target.installState.status === 'diverged' && (
        <Alert>
          <AlertTitle>{t('Installed version is not in this index')}</AlertTitle>
          <AlertDescription>
            {t(
              'v{{installed}} is installed but this source does not list it. Installing replaces it with v{{target}}.',
              {
                installed: target.installState.installedVersion,
                target: selectedVersion,
              }
            )}
          </AlertDescription>
        </Alert>
      )}

      {installMutation.error && (
        <div className='space-y-1'>
          <p className='text-destructive text-sm font-medium'>
            {t('Plugin installation failed')}
          </p>
          {/* Verbatim: preflight rejections name the conflicting plugin. */}
          <p className='text-destructive text-sm whitespace-pre-wrap'>
            {installMutation.error.message}
          </p>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Marketplace installs never force past a conflict. Resolve it on the task plugins page, then install again.'
            )}
          </p>
        </div>
      )}
    </Dialog>
  )
}
