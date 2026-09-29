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
import { useEffect, useState } from 'react'

import { api } from '@/lib/api'
import { getLobeIcon } from '@/lib/lobe-icon'
import { cn } from '@/lib/utils'

import {
  pluginIconUrl,
  resolvePluginIcon,
  textAvatarClass,
  type PluginIconInput,
} from '../lib/plugin-icon'

type PluginIconProps = {
  plugin: PluginIconInput
  size?: number
}

function GatewayPluginIcon(props: PluginIconProps) {
  const query = useQuery({
    queryKey: ['task-plugin', props.plugin.key, 'icon'],
    queryFn: async ({ signal }) => {
      // An img request cannot carry the dashboard Bearer token. Fetch through
      // the shared client, then keep SVG in the browser's inert image mode.
      const response = await api.get<Blob>(pluginIconUrl(props.plugin.key), {
        responseType: 'blob',
        signal,
        // React Query owns deduplication and cancellation for this key. The
        // HTTP client's shared promise can still belong to an aborted mount.
        disableDuplicate: true,
        skipErrorHandler: true,
        skipBusinessError: true,
      })
      return response.data.type.startsWith('image/') ? response.data : null
    },
    staleTime: 5 * 60 * 1000,
    retry: false,
    meta: { errorToast: false },
  })
  const [image, setImage] = useState<{ blob: Blob; src: string } | null>(null)
  useEffect(() => {
    if (!query.data) return
    const src = URL.createObjectURL(query.data)
    setImage({ blob: query.data, src })
    return () => URL.revokeObjectURL(src)
  }, [query.data])

  return (
    <PluginIcon
      plugin={{
        ...props.plugin,
        hasIcon: false,
        iconSrc: image?.blob === query.data ? image?.src : undefined,
      }}
      size={props.size}
    />
  )
}

export function PluginIcon(props: PluginIconProps) {
  const size = props.size ?? 20
  const [failedSrc, setFailedSrc] = useState<string | null>(null)
  if (props.plugin.hasIcon && !props.plugin.iconSrc) {
    return <GatewayPluginIcon key={props.plugin.key} {...props} />
  }
  let descriptor = resolvePluginIcon(props.plugin)
  if (descriptor.kind === 'image' && descriptor.src === failedSrc) {
    // The image did not load (missing file, blocked content type): show what
    // the manifest declares instead of a broken-image glyph.
    descriptor = resolvePluginIcon({
      ...props.plugin,
      iconSrc: undefined,
      hasIcon: false,
    })
  }
  if (descriptor.kind === 'lobe') {
    return <>{getLobeIcon(descriptor.name, size)}</>
  }
  if (descriptor.kind === 'image') {
    const src = descriptor.src
    // Logos are drawn only through <img>: the browser's image mode runs SVG
    // without scripts, external loads, or DOM access, so no sanitizer is
    // needed and the bytes must never reach dangerouslySetInnerHTML.
    return (
      <span
        aria-hidden='true'
        className='bg-muted/40 inline-flex shrink-0 items-center justify-center overflow-hidden rounded-md'
        style={{ width: size, height: size }}
      >
        <img
          src={src}
          alt=''
          width={size}
          height={size}
          draggable={false}
          className='h-full w-full object-contain'
          onError={() => setFailedSrc(src)}
        />
      </span>
    )
  }
  return (
    <div
      aria-hidden='true'
      className={cn(
        'flex items-center justify-center rounded-md font-semibold select-none',
        textAvatarClass(descriptor.colorSeed)
      )}
      style={{
        width: size,
        height: size,
        fontSize: Math.max(8, Math.floor(size * 0.42)),
      }}
    >
      {descriptor.label}
    </div>
  )
}
