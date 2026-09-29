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
import { ExternalLink, Copy, Music } from 'lucide-react'
import { useState, useRef, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { IconBadge } from '@/components/ui/icon-badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Spinner } from '@/components/ui/spinner'

import type { AudioClip } from '../../types'

export type { AudioClip }

interface AudioPreviewDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  clips: AudioClip[]
  loading?: boolean
}

function formatDuration(seconds?: number): string {
  if (!seconds || seconds <= 0) return '--:--'
  const m = Math.floor(seconds / 60)
  const s = Math.floor(seconds % 60)
  return `${m}:${s.toString().padStart(2, '0')}`
}

function AudioClipCard({ clip }: { clip: AudioClip }) {
  const { t } = useTranslation()
  const [hasError, setHasError] = useState(false)
  const audioRef = useRef<HTMLAudioElement>(null)

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setHasError(false)
  }, [clip.audio_url])

  const title = clip.title || t('Untitled')
  const tags = clip.tags || clip.metadata?.tags || ''
  const duration = clip.duration || clip.metadata?.duration
  const imageUrl = clip.image_url || clip.image_large_url
  const audioUrl = clip.audio_url

  if (!audioUrl) return null

  return (
    <div className='bg-card flex gap-4 rounded-lg border p-4'>
      {imageUrl && (
        <img
          src={imageUrl}
          alt={title}
          className='h-20 w-20 shrink-0 rounded-lg object-cover'
          onError={(e) => {
            ;(e.target as HTMLElement).style.display = 'none'
          }}
        />
      )}
      <div className='min-w-0 flex-1'>
        <div className='mb-1 flex items-center gap-2'>
          <span className='truncate text-sm font-medium'>{title}</span>
          {duration != null && duration > 0 && (
            <StatusBadge
              label={formatDuration(duration)}
              variant='neutral'
              className='shrink-0'
              copyable={false}
            />
          )}
        </div>

        {tags && (
          <p className='text-muted-foreground mb-2 truncate text-xs'>{tags}</p>
        )}

        {hasError ? (
          <div className='flex flex-wrap items-center gap-2'>
            <span className='text-destructive text-xs'>
              {t('Audio playback failed')}
            </span>
            <Button
              variant='outline'
              size='sm'
              className='h-7 gap-1 text-xs'
              onClick={() => window.open(audioUrl, '_blank')}
            >
              <ExternalLink className='h-3 w-3' />
              {t('Open in new tab')}
            </Button>
            <Button
              variant='outline'
              size='sm'
              className='h-7 gap-1 text-xs'
              onClick={() => {
                navigator.clipboard.writeText(audioUrl)
                toast.success(t('Copied'))
              }}
            >
              <Copy className='h-3 w-3' />
              {t('Copy Link')}
            </Button>
          </div>
        ) : (
          <audio
            ref={audioRef}
            src={audioUrl}
            controls
            preload='none'
            onError={() => setHasError(true)}
            className='h-9 w-full'
          />
        )}
      </div>
    </div>
  )
}

export function AudioPreviewDialog(props: AudioPreviewDialogProps) {
  const { t } = useTranslation()
  const clips = Array.isArray(props.clips) ? props.clips : []

  let body = (
    <ScrollArea className='max-h-[60vh]'>
      <div className='space-y-3 pr-2'>
        {clips.map((clip, idx) => (
          <AudioClipCard key={clip.clip_id || clip.id || idx} clip={clip} />
        ))}
      </div>
    </ScrollArea>
  )
  if (props.loading) {
    body = (
      <div className='flex justify-center py-4' aria-label={t('Loading...')}>
        <Spinner />
      </div>
    )
  } else if (clips.length === 0) {
    body = (
      <p className='text-muted-foreground py-4 text-center text-sm'>
        {t('None')}
      </p>
    )
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={
        <>
          <IconBadge tone='chart-4' size='sm'>
            <Music />
          </IconBadge>
          {t('Audio Preview')}
        </>
      }
      contentClassName='sm:max-w-lg'
      titleClassName='flex items-center gap-2'
      contentHeight='auto'
      bodyClassName='space-y-4'
    >
      {body}
    </Dialog>
  )
}
