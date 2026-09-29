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
import type { ComponentProps, ReactNode } from 'react'

import { FormItem } from '@/components/ui/form'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { cn } from '@/lib/utils'

type SettingsFormGridProps = {
  children: ReactNode
  className?: string
}

type SettingsFormGridItemProps = SettingsFormGridProps & {
  span?: 'default' | 'full'
}

type SettingsSwitchItemProps = ComponentProps<typeof FormItem>
type SettingsSwitchRowProps = ComponentProps<'div'>
type SettingsControlGroupProps = ComponentProps<'div'>
type SettingsControlChildrenProps = ComponentProps<'div'>
type SettingsSwitchFieldProps = SettingsSwitchRowProps & {
  controlId?: string
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  label: ReactNode
  description?: ReactNode
  disabled?: boolean
}

const settingsSwitchRowClassName =
  'flex min-w-0 flex-row items-center justify-between gap-4 py-2.5'

export function SettingsFormGrid(props: SettingsFormGridProps) {
  return (
    <div
      data-settings-form-span='full'
      className={cn(
        'grid min-w-0 gap-x-5 gap-y-6 lg:grid-cols-2',
        'lg:[&>[data-settings-form-span=full]]:col-span-2',
        '[&>[data-slot=form-item]]:min-w-0',
        'lg:[&>[data-slot=form-item]:has(textarea)]:col-span-2',
        'lg:[&>[data-slot=form-item]:has([data-slot=switch])]:col-span-2',
        props.className
      )}
    >
      {props.children}
    </div>
  )
}

export function SettingsFormGridItem(props: SettingsFormGridItemProps) {
  return (
    <div
      data-settings-form-span={props.span === 'full' ? 'full' : undefined}
      className={cn(
        'min-w-0',
        props.span === 'full' && 'lg:col-span-2',
        props.className
      )}
    >
      {props.children}
    </div>
  )
}

export function SettingsSwitchItem({
  className,
  ...props
}: SettingsSwitchItemProps) {
  return (
    <FormItem
      data-settings-form-span='full'
      className={cn(settingsSwitchRowClassName, className)}
      {...props}
    />
  )
}

export function SettingsSwitchRow({
  className,
  ...props
}: SettingsSwitchRowProps) {
  return (
    <div
      data-settings-form-span='full'
      className={cn(settingsSwitchRowClassName, className)}
      {...props}
    />
  )
}

export function SettingsSwitchField({
  controlId,
  checked,
  onCheckedChange,
  label,
  description,
  disabled,
  className,
  ...props
}: SettingsSwitchFieldProps) {
  return (
    <SettingsSwitchRow className={className} {...props}>
      <SettingsSwitchContent>
        <Label htmlFor={controlId} className='text-sm font-medium'>
          {label}
        </Label>
        {description ? (
          <p
            id={controlId ? `${controlId}-description` : undefined}
            className='text-muted-foreground text-xs'
          >
            {description}
          </p>
        ) : null}
      </SettingsSwitchContent>
      <Switch
        id={controlId}
        aria-describedby={
          controlId && description ? `${controlId}-description` : undefined
        }
        checked={checked}
        onCheckedChange={onCheckedChange}
        disabled={disabled}
      />
    </SettingsSwitchRow>
  )
}

export function SettingsSwitchContent(props: SettingsFormGridProps) {
  return (
    <div className={cn('min-w-0 space-y-0.5', props.className)}>
      {props.children}
    </div>
  )
}

export function SettingsControlGroup({
  className,
  ...props
}: SettingsControlGroupProps) {
  return (
    <div
      data-settings-form-span='full'
      className={cn(
        'bg-muted/20 min-w-0 space-y-3 rounded-xl border px-3 py-2.5',
        className
      )}
      {...props}
    />
  )
}

export function SettingsControlChildren({
  className,
  ...props
}: SettingsControlChildrenProps) {
  return (
    <div
      className={cn('border-border/70 ml-2 min-w-0 border-l pl-3', className)}
      {...props}
    />
  )
}

export function SettingsForm({ className, ...props }: ComponentProps<'form'>) {
  return (
    <form
      className={cn(
        'grid min-w-0 gap-x-5 gap-y-6 lg:grid-cols-2',
        'lg:[&>*:not([data-slot=form-item])]:col-span-2',
        'lg:[&>[data-settings-form-span=full]]:col-span-2',
        'lg:[&>[data-slot=alert]]:col-span-2',
        '[&>[data-slot=form-item]]:min-w-0',
        'lg:[&>[data-slot=form-item]:has(textarea)]:col-span-2',
        'lg:[&>[data-slot=form-item]:has([data-slot=switch])]:col-span-2',
        className
      )}
      {...props}
    />
  )
}
