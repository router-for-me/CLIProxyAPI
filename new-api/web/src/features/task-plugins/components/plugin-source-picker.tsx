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
import { FileCode2, FolderOpen, Upload } from 'lucide-react'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

type PluginSourcePickerProps = {
  /** Name of the file whose text currently fills the source field, if any. */
  fileName: string
  onSelect: (file: File) => void
}

/**
 * Drop zone for the plugin's `.js` file. The native file input is kept in the
 * DOM (labelled, visually hidden) rather than rendered directly: the unstyled
 * "Choose file / no file selected" control is the single most off-brand element
 * in this dialog, and hiding it behind a real button matches how every other
 * upload in the app looks.
 */
export function PluginSourcePicker(props: PluginSourcePickerProps) {
  const { t } = useTranslation()
  const inputRef = useRef<HTMLInputElement>(null)
  const [isDragActive, setIsDragActive] = useState(false)

  const selectFile = (file?: File) => {
    if (!file) return
    props.onSelect(file)
  }

  return (
    <div
      className={cn(
        'flex flex-col items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-center transition-colors',
        isDragActive ? 'border-primary bg-primary/5' : 'bg-muted/20'
      )}
      onDragOver={(event) => {
        event.preventDefault()
        setIsDragActive(true)
      }}
      onDragLeave={() => setIsDragActive(false)}
      onDrop={(event) => {
        event.preventDefault()
        setIsDragActive(false)
        selectFile(event.dataTransfer.files[0])
      }}
    >
      {props.fileName ? (
        <FileCode2
          aria-hidden='true'
          className='text-muted-foreground size-5'
        />
      ) : (
        <Upload aria-hidden='true' className='text-muted-foreground size-5' />
      )}
      <div className='space-y-0.5'>
        <p className='text-sm font-medium'>
          {props.fileName || t('Drop a JavaScript plugin file here')}
        </p>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Single .js file, up to 8 MiB. Its source is shown below before upload.'
          )}
        </p>
      </div>
      <Button
        type='button'
        variant='outline'
        size='sm'
        onClick={() => inputRef.current?.click()}
      >
        <FolderOpen aria-hidden='true' />
        {props.fileName ? t('Choose another file') : t('Choose file')}
      </Button>
      {/* sr-only rather than hidden so the label stays reachable to a
        screen reader and to userEvent.upload in tests. */}
      <label className='sr-only' htmlFor='task-plugin-file'>
        {t('JavaScript file')}
      </label>
      <input
        ref={inputRef}
        id='task-plugin-file'
        type='file'
        accept='.js,text/javascript'
        className='sr-only'
        onChange={(event) => {
          selectFile(event.target.files?.[0])
          // Allow re-picking the same file after an error.
          event.target.value = ''
        }}
      />
    </div>
  )
}
