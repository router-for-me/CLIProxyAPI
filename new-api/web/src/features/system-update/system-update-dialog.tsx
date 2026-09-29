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
import { lazy, Suspense, type ReactElement } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { formatTimestampToDate } from '@/lib/format'

import { getSystemReleaseUrl, parseSystemVersion } from './releases'
import {
  getUpdateErrorMessage,
  type useSystemUpdate,
} from './use-system-update'

const ReleaseMarkdown = lazy(() =>
  import('@/components/ui/markdown').then((module) => ({
    default: module.Markdown,
  }))
)

type SystemUpdateDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  trigger: ReactElement
  update: ReturnType<typeof useSystemUpdate>
}

export function SystemUpdateDialog(props: SystemUpdateDialogProps) {
  const { t } = useTranslation()
  const update = props.update
  const release = update.release
  const snapshot = update.snapshot
  const releaseUrl = release ? getSystemReleaseUrl(release) : null
  const isPrerelease =
    release &&
    (release.prerelease ||
      (parseSystemVersion(release.tag_name)?.stage ?? 3) < 3)
  const canIgnore = release && (update.hasUpdate || update.comparison === null)
  let statusText = t('Updates have not been checked yet.')
  if (update.checking) {
    statusText = t('Checking updates...')
  } else if (release) {
    if (update.isIgnored) {
      statusText = t('This version is ignored')
    } else if (update.comparison === null) {
      statusText = ''
    } else if (update.hasUpdate) {
      statusText = t('New version available: {{version}}', {
        version: release.tag_name,
      })
    } else {
      statusText = t('No newer version available.')
    }
  } else if (snapshot?.lastCheckedAt) {
    statusText = t('No releases found.')
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      trigger={props.trigger}
      title={t('System updates')}
      bodyClassName='space-y-4'
      footerClassName='sm:flex-wrap'
      footer={
        <>
          {(canIgnore || update.isIgnored) && (
            <Button
              type='button'
              variant='ghost'
              className='sm:me-auto'
              onClick={() => update.setIgnored(!update.isIgnored)}
            >
              {update.isIgnored
                ? t('Restore notifications')
                : t('Ignore this version')}
            </Button>
          )}
          <Button
            type='button'
            variant='outline'
            disabled={update.checking || !update.online}
            onClick={() => void update.checkNow()}
          >
            {update.checking ? t('Checking updates...') : t('Check again')}
          </Button>
          {releaseUrl && (
            <Button
              nativeButton={false}
              role='link'
              render={
                <a
                  href={releaseUrl}
                  target='_blank'
                  rel='noopener noreferrer'
                />
              }
            >
              {t('Go to GitHub')}
            </Button>
          )}
        </>
      }
    >
      <dl className='grid min-w-0 gap-3 text-sm sm:grid-cols-2'>
        <div className='min-w-0'>
          <dt className='text-muted-foreground'>{t('Current version')}</dt>
          <dd className='font-medium break-all'>
            {update.currentVersion || t('Unknown version')}
          </dd>
        </div>
        <div className='min-w-0'>
          <dt className='text-muted-foreground'>{t('Latest version')}</dt>
          <dd className='flex flex-wrap items-center gap-2 font-medium break-all'>
            {release?.tag_name || t('Unknown')}
            {isPrerelease && (
              <Badge variant='warning'>{t('Pre-release')}</Badge>
            )}
          </dd>
        </div>
        {release?.published_at && (
          <div>
            <dt className='text-muted-foreground'>{t('Published at')}</dt>
            <dd>
              {formatTimestampToDate(
                Date.parse(release.published_at),
                'milliseconds'
              )}
            </dd>
          </div>
        )}
      </dl>
      {statusText && (
        <p role='status' aria-live='polite' className='text-sm'>
          {statusText}
        </p>
      )}
      {!update.online && (
        <Alert>
          <AlertDescription>
            {t('You are offline. Updates will be checked when you reconnect.')}
          </AlertDescription>
        </Alert>
      )}
      {snapshot?.error && (
        <Alert variant='destructive'>
          <AlertDescription>
            {getUpdateErrorMessage(snapshot.error, t)}
            {release && (
              <p>{t('Showing the last successfully checked release.')}</p>
            )}
          </AlertDescription>
        </Alert>
      )}
      {props.open && release && (
        <Suspense fallback={<Skeleton className='h-24 w-full' />}>
          {release.body ? (
            <ReleaseMarkdown baseUrl={releaseUrl ?? undefined}>
              {release.body}
            </ReleaseMarkdown>
          ) : (
            <p className='text-muted-foreground text-sm'>
              {t('No release notes provided.')}
            </p>
          )}
        </Suspense>
      )}
    </Dialog>
  )
}
