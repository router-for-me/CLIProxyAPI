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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type { ChannelUpstreamUpdateState } from '../../hooks/use-channel-upstream-updates'
import { UpstreamModelSelection } from '../upstream-model-selection'

type UpstreamUpdateDialogProps = {
  upstream: ChannelUpstreamUpdateState
}

function ModelChangeList(props: { title: string; models: string[] }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  return (
    <section aria-label={props.title} className='min-w-0 space-y-2'>
      <h3 className='text-sm font-medium'>
        {props.title} ({formatNumber(props.models.length, locale)})
      </h3>
      {props.models.length ? (
        <ul className='max-h-40 space-y-1 overflow-y-auto rounded-md border p-3 font-mono text-xs break-all'>
          {props.models.map((model) => (
            <li key={model}>{model}</li>
          ))}
        </ul>
      ) : (
        <p className='text-muted-foreground text-sm'>{t('None')}</p>
      )}
    </section>
  )
}

export function UpstreamUpdateDialog(props: UpstreamUpdateDialogProps) {
  if (!props.upstream.showModal || !props.upstream.channel) return null
  // Each preview gets fresh selections, including when detection finishes or
  // another channel is opened. Keeping a hidden dialog mounted retained stale state.
  return (
    <UpstreamUpdateSession
      key={`${props.upstream.previewVersion}-${props.upstream.result ? 'result' : 'preview'}`}
      upstream={props.upstream}
    />
  )
}

function UpstreamUpdateSession(props: UpstreamUpdateDialogProps) {
  const { t } = useTranslation()
  const [activeTab, setActiveTab] = useState(props.upstream.preferredTab)
  const [selectedAdd, setSelectedAdd] = useState(props.upstream.addModels)
  const [selectedRemove, setSelectedRemove] = useState<string[]>([])
  const [confirmOpen, setConfirmOpen] = useState(false)
  const result = props.upstream.result
  const hasChanges =
    props.upstream.addModels.length > 0 ||
    props.upstream.removeModels.length > 0
  const isPreview =
    !props.upstream.detectLoading && !props.upstream.detectError && !result
  const channelLabel = `#${props.upstream.channel?.id} ${props.upstream.channel?.name ?? ''}`
  const summary = t('Add {{added}} models and remove {{removed}} models', {
    added: selectedAdd.length,
    removed: selectedRemove.length,
  })

  return (
    <>
      <Dialog
        open
        onOpenChange={(open) => !open && props.upstream.closeModal()}
        showCloseButton={!props.upstream.applyLoading}
        title={
          result ? t('Update results') : t('Preview upstream model changes')
        }
        description={<span className='break-all'>{channelLabel}</span>}
        bodyClassName='space-y-4'
        titleClassName='pr-6 leading-snug'
        footerClassName='sm:flex-wrap'
        footer={
          <>
            <Button
              variant='outline'
              onClick={props.upstream.closeModal}
              disabled={props.upstream.applyLoading}
            >
              {isPreview && hasChanges ? t('Cancel') : t('Close')}
            </Button>
            {isPreview && (
              <>
                <Button
                  className='h-auto min-h-8 max-w-full whitespace-normal'
                  variant='outline'
                  onClick={() =>
                    props.upstream.detectChannelUpdates(props.upstream.channel)
                  }
                  disabled={props.upstream.applyLoading}
                >
                  {t('Refresh preview')}
                </Button>
                <Button
                  className='h-auto min-h-8 max-w-full whitespace-normal'
                  onClick={() => setConfirmOpen(true)}
                  disabled={
                    props.upstream.applyLoading ||
                    !!props.upstream.applyError ||
                    (!selectedAdd.length && !selectedRemove.length)
                  }
                >
                  {t('Review selected changes')}
                </Button>
              </>
            )}
          </>
        }
      >
        {props.upstream.detectLoading && (
          <LoadingState
            message={t(
              'Checking upstream models. Channel models will only change after confirmation.'
            )}
          />
        )}
        {props.upstream.detectError && (
          <ErrorState
            title={t('Detection failed')}
            description={props.upstream.detectError}
            onRetry={() =>
              props.upstream.detectChannelUpdates(props.upstream.channel)
            }
          />
        )}
        {result && (
          <>
            <Alert>
              <AlertTitle>{t('Update completed')}</AlertTitle>
              <AlertDescription>
                {t(
                  'The server returned the following result. Unselected models remain pending.'
                )}
              </AlertDescription>
            </Alert>
            <ModelChangeList
              title={t('Added models')}
              models={result.addedModels}
            />
            <ModelChangeList
              title={t('Removed models')}
              models={result.removedModels}
            />
            <ModelChangeList
              title={t('Still pending addition')}
              models={result.remainingModels}
            />
            <ModelChangeList
              title={t('Still pending removal')}
              models={result.remainingRemoveModels}
            />
          </>
        )}
        {isPreview && (
          <>
            <Alert>
              <AlertDescription>
                {t(
                  'Review the model changes before applying. Unselected models stay unchanged and will not be ignored.'
                )}
              </AlertDescription>
            </Alert>
            {props.upstream.applyError && (
              <Alert variant='destructive'>
                <AlertTitle>
                  {t('Unable to confirm the update result')}
                </AlertTitle>
                <AlertDescription>
                  <p>{props.upstream.applyError}</p>
                  <p>
                    {t(
                      'Refresh the preview before trying again; some changes may already have been saved.'
                    )}
                  </p>
                </AlertDescription>
              </Alert>
            )}
            {hasChanges && (
              <p className='text-sm font-medium' aria-live='polite'>
                {summary}
              </p>
            )}
            {hasChanges ? (
              <Tabs
                value={activeTab}
                onValueChange={(value) =>
                  setActiveTab(value as 'add' | 'remove')
                }
              >
                <TabsList className='grid w-full grid-cols-2 group-data-horizontal/tabs:h-auto'>
                  <TabsTrigger
                    value='add'
                    className='h-auto min-h-7 whitespace-normal'
                  >
                    {t('Add Models')} ({props.upstream.addModels.length})
                  </TabsTrigger>
                  <TabsTrigger
                    value='remove'
                    className='h-auto min-h-7 whitespace-normal'
                  >
                    {t('Remove Models')} ({props.upstream.removeModels.length})
                  </TabsTrigger>
                </TabsList>
                <TabsContent value='add'>
                  <UpstreamModelSelection
                    models={props.upstream.addModels}
                    selected={selectedAdd}
                    onChange={(models) =>
                      !props.upstream.applyLoading && setSelectedAdd(models)
                    }
                    existingModels={[]}
                    summaryText={t('Add Models')}
                    showChanges={false}
                  />
                </TabsContent>
                <TabsContent value='remove' className='space-y-3'>
                  <Alert variant='destructive'>
                    <AlertDescription>
                      {t(
                        'Removed models will no longer be available through this channel. Select removals explicitly.'
                      )}
                    </AlertDescription>
                  </Alert>
                  <UpstreamModelSelection
                    models={props.upstream.removeModels}
                    selected={selectedRemove}
                    onChange={(models) =>
                      !props.upstream.applyLoading && setSelectedRemove(models)
                    }
                    existingModels={[]}
                    summaryText={t('Remove Models')}
                    showChanges={false}
                  />
                </TabsContent>
              </Tabs>
            ) : (
              <EmptyState
                title={t(
                  'No processable upstream model updates for this channel'
                )}
              />
            )}
          </>
        )}
      </Dialog>
      <ConfirmDialog
        open={confirmOpen && !result}
        onOpenChange={(open) =>
          !props.upstream.applyLoading && setConfirmOpen(open)
        }
        title={t('Confirm model changes')}
        desc={
          <>
            <span className='break-all'>{channelLabel}</span>
            <p>{summary}</p>
          </>
        }
        confirmText={
          props.upstream.applyLoading
            ? t('Applying...')
            : t('Apply selected changes')
        }
        destructive={selectedRemove.length > 0}
        isLoading={props.upstream.applyLoading}
        className='max-h-(--dialog-available-height) overflow-y-auto [&_button]:h-auto [&_button]:min-h-8 [&_button]:whitespace-normal'
        handleConfirm={async () => {
          await props.upstream.applyUpdates({
            addModels: selectedAdd,
            removeModels: selectedRemove,
          })
          setConfirmOpen(false)
        }}
      >
        <ModelChangeList title={t('Add Models')} models={selectedAdd} />
        <ModelChangeList title={t('Remove Models')} models={selectedRemove} />
      </ConfirmDialog>
    </>
  )
}
