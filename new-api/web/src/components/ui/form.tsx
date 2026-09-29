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
import { useRender } from '@base-ui/react/use-render'
import * as React from 'react'
import {
  Controller,
  FormProvider,
  useFormContext,
  useFormState,
  type ControllerProps,
  type FieldPath,
  type FieldValues,
} from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'

type FormRootContextValue = {
  id: string
}

const FormRootContext = React.createContext<FormRootContextValue | null>(null)

function getFormScopedSelector(formId: string, selector: string): string {
  return `[data-form-root="${formId}"]${selector}`
}

function hasFormErrors(errors: unknown): boolean {
  return (
    typeof errors === 'object' &&
    errors !== null &&
    Object.keys(errors).length > 0
  )
}

function getFirstFormErrorTarget(
  invalidControl: HTMLElement | null,
  errorMessage: HTMLElement | null
): HTMLElement | null {
  if (!invalidControl) return errorMessage
  if (!errorMessage) return invalidControl

  const position = invalidControl.compareDocumentPosition(errorMessage)
  return position & Node.DOCUMENT_POSITION_PRECEDING
    ? errorMessage
    : invalidControl
}

function FormValidationFocus() {
  const formContext = React.useContext(FormRootContext)
  const { control } = useFormContext()
  const { errors, submitCount } = useFormState({ control })
  const handledSubmitCountRef = React.useRef(0)

  React.useEffect(() => {
    if (!formContext || submitCount === 0 || !hasFormErrors(errors)) return
    if (handledSubmitCountRef.current === submitCount) return

    handledSubmitCountRef.current = submitCount

    const animationFrameId = window.requestAnimationFrame(() => {
      const invalidControl = document.querySelector<HTMLElement>(
        getFormScopedSelector(formContext.id, '[aria-invalid="true"]')
      )
      const errorMessage = document.querySelector<HTMLElement>(
        getFormScopedSelector(formContext.id, '[data-slot="form-message"]')
      )
      const target = getFirstFormErrorTarget(invalidControl, errorMessage)
      if (!target) return

      const formItem = target.closest<HTMLElement>(
        getFormScopedSelector(formContext.id, '[data-slot="form-item"]')
      )
      const scrollTarget = formItem ?? target
      const focusTarget =
        target === invalidControl
          ? invalidControl
          : (formItem?.querySelector<HTMLElement>(
              '[aria-invalid="true"], input, textarea, select, button, [tabindex]:not([tabindex="-1"])'
            ) ?? null)

      scrollTarget.scrollIntoView({ block: 'center', behavior: 'smooth' })
      focusTarget?.focus({ preventScroll: true })
    })

    return () => window.cancelAnimationFrame(animationFrameId)
  }, [errors, formContext, submitCount])

  return null
}

function Form<TFieldValues extends FieldValues = FieldValues>({
  children,
  ...props
}: React.ComponentProps<typeof FormProvider<TFieldValues>>) {
  const reactId = React.useId()
  const id = React.useMemo(
    () => `form-${reactId.replaceAll(/[^a-zA-Z0-9_-]/g, '_')}`,
    [reactId]
  )

  return (
    <FormRootContext.Provider value={{ id }}>
      <FormProvider {...props}>
        <FormValidationFocus />
        {children}
      </FormProvider>
    </FormRootContext.Provider>
  )
}

type FormFieldContextValue<
  TFieldValues extends FieldValues = FieldValues,
  TName extends FieldPath<TFieldValues> = FieldPath<TFieldValues>,
> = {
  name: TName
}

const FormFieldContext = React.createContext<FormFieldContextValue>(
  {} as FormFieldContextValue
)

const FormField = <
  TFieldValues extends FieldValues = FieldValues,
  TName extends FieldPath<TFieldValues> = FieldPath<TFieldValues>,
>({
  ...props
}: ControllerProps<TFieldValues, TName>) => {
  return (
    <FormFieldContext.Provider value={{ name: props.name }}>
      <Controller {...props} />
    </FormFieldContext.Provider>
  )
}

const useFormField = () => {
  const fieldContext = React.useContext(FormFieldContext)
  const itemContext = React.useContext(FormItemContext)
  const { getFieldState } = useFormContext()
  const formState = useFormState({ name: fieldContext.name })
  const fieldState = getFieldState(fieldContext.name, formState)

  if (!fieldContext) {
    throw new Error('useFormField should be used within <FormField>')
  }

  const { id } = itemContext

  return {
    id,
    name: fieldContext.name,
    formItemId: `${id}-form-item`,
    formDescriptionId: `${id}-form-item-description`,
    formMessageId: `${id}-form-item-message`,
    ...fieldState,
  }
}

type FormItemContextValue = {
  id: string
}

const FormItemContext = React.createContext<FormItemContextValue>(
  {} as FormItemContextValue
)

function FormItem({ className, ...props }: React.ComponentProps<'div'>) {
  const id = React.useId()
  const formContext = React.useContext(FormRootContext)

  return (
    <FormItemContext.Provider value={{ id }}>
      <div
        data-slot='form-item'
        data-form-root={formContext?.id}
        className={cn('grid gap-2', className)}
        {...props}
      />
    </FormItemContext.Provider>
  )
}

function FormLabel({
  className,
  ...props
}: React.ComponentProps<typeof Label>) {
  const { error, formItemId } = useFormField()

  return (
    <Label
      data-slot='form-label'
      data-error={!!error}
      className={cn('data-[error=true]:text-destructive', className)}
      htmlFor={formItemId}
      {...props}
    />
  )
}

function FormControl({
  children,
  ...props
}: { children: React.ReactElement } & Record<string, unknown>) {
  const { error, formItemId, formDescriptionId, formMessageId } = useFormField()
  const formContext = React.useContext(FormRootContext)

  return useRender({
    render: children,
    props: {
      'data-slot': 'form-control',
      'data-form-root': formContext?.id,
      id: formItemId,
      'aria-describedby': !error
        ? `${formDescriptionId}`
        : `${formDescriptionId} ${formMessageId}`,
      'aria-invalid': !!error,
      ...props,
    },
  })
}

function FormDescription({ className, ...props }: React.ComponentProps<'p'>) {
  const { formDescriptionId } = useFormField()

  return (
    <p
      data-slot='form-description'
      id={formDescriptionId}
      className={cn('text-muted-foreground text-sm', className)}
      {...props}
    />
  )
}

function FormMessage({ className, ...props }: React.ComponentProps<'p'>) {
  const { error, formMessageId } = useFormField()
  const formContext = React.useContext(FormRootContext)
  const { t } = useTranslation()
  const body = error ? String(error?.message ?? '') : props.children

  if (!body) {
    return null
  }

  const translatedBody = typeof body === 'string' ? t(body) : body

  return (
    <p
      data-slot='form-message'
      data-form-root={formContext?.id}
      id={formMessageId}
      className={cn('text-destructive text-sm', className)}
      {...props}
    >
      {translatedBody}
    </p>
  )
}

export {
  useFormField,
  Form,
  FormItem,
  FormLabel,
  FormControl,
  FormDescription,
  FormMessage,
  FormField,
}
