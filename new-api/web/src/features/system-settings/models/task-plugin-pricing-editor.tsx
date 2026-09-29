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
import {
  useId,
  useState,
  type Dispatch,
  type ReactNode,
  type SetStateAction,
} from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { ModelPricingPluginVariant } from '@/features/model-pricing/api'
import type { PricingCurrency } from '@/features/model-pricing/currency'
import {
  combineBillingExpr,
  splitBillingExprAndRequestRules,
} from '@/features/pricing/lib/billing-expr'
import { taskExpressionCompatible } from '@/features/pricing/lib/plugin-pricing'
import {
  createDefaultTaskVisualConfig,
  generateTaskExprFromConfig,
} from '@/features/pricing/lib/task-expr'
import { PluginIcon } from '@/features/task-plugins/components/plugin-icon'

import { SettingsSwitchField } from '../components/settings-form-layout'
import { TaskUsagePricingEditor } from './task-usage-pricing-editor'

type TaskPluginPricingEditorProps = {
  variants: ModelPricingPluginVariant[]
  expressions: Record<string, string>
  onChange: Dispatch<SetStateAction<Record<string, string>>>
  modelExpression: string
  modelBillingMode?: 'ratio' | 'tiered_expr'
  currency: PricingCurrency
  children: ReactNode
}

export function TaskPluginPricingEditor(props: TaskPluginPricingEditorProps) {
  const { t } = useTranslation()
  const id = useId()
  const [selected, setSelected] = useState('__model__')
  const usesExpression = props.modelBillingMode !== 'ratio'
  const incompatible = props.variants.filter((variant) => {
    if (
      variant.stale ||
      !usesExpression ||
      Object.hasOwn(props.expressions, variant.plugin_key)
    ) {
      return false
    }
    const compatible = taskExpressionCompatible(
      props.modelExpression,
      variant.usage_schema
    )
    if (compatible !== undefined) return !compatible
    return props.modelExpression === variant.effective && !variant.compatible
  })

  if (props.variants.length === 0) return props.children

  return (
    <Tabs
      value={selected}
      onValueChange={(value) => setSelected(String(value))}
    >
      <TabsList
        aria-label={t('Provider')}
        className='max-w-full flex-wrap justify-start group-data-horizontal/tabs:h-auto'
      >
        <TabsTrigger value='__model__'>{t('Default')}</TabsTrigger>
        {props.variants.map((variant) => (
          <TabsTrigger
            key={variant.plugin_key}
            value={variant.plugin_key}
            className='max-w-full min-w-0'
          >
            <PluginIcon
              plugin={{
                key: variant.plugin_key,
                name: variant.plugin_name,
                icon: variant.icon,
              }}
              size={16}
            />
            <span className='truncate' title={variant.plugin_name}>
              {variant.plugin_name}
            </span>
          </TabsTrigger>
        ))}
      </TabsList>
      <TabsContent value='__model__' keepMounted className='space-y-3'>
        {incompatible.length > 0 && (
          <Alert>
            <AlertDescription>
              <p>
                {t(
                  'The model-level expression cannot be evaluated by: {{plugins}}',
                  {
                    plugins: incompatible
                      .map((variant) => variant.plugin_name)
                      .join(', '),
                  }
                )}
              </p>
              <div className='flex flex-wrap gap-1'>
                {incompatible.map((variant) => (
                  <Button
                    key={variant.plugin_key}
                    type='button'
                    variant='link'
                    size='sm'
                    onClick={() => setSelected(variant.plugin_key)}
                  >
                    {variant.plugin_name}
                  </Button>
                ))}
              </div>
            </AlertDescription>
          </Alert>
        )}
        {props.children}
      </TabsContent>
      {props.variants.map((variant) => {
        const key = variant.plugin_key
        const separate = Object.hasOwn(props.expressions, key)
        if (variant.stale) {
          return (
            <TabsContent
              key={key}
              value={key}
              keepMounted
              className='space-y-3'
            >
              <Alert>
                <AlertDescription>
                  {t('This provider is unavailable for this model.')}
                </AlertDescription>
              </Alert>
              {separate && (
                <>
                  <code className='bg-muted/30 block max-h-40 overflow-auto rounded-md border p-3 text-xs break-all whitespace-pre-wrap'>
                    {props.expressions[key]}
                  </code>
                  <Button
                    type='button'
                    variant='outline'
                    onClick={() => {
                      props.onChange((current) => {
                        const next = { ...current }
                        delete next[key]
                        return next
                      })
                      setSelected('__model__')
                    }}
                  >
                    {t('Remove saved price')}
                  </Button>
                </>
              )}
            </TabsContent>
          )
        }
        const expression = separate
          ? props.expressions[key]
          : props.modelExpression
        const split = splitBillingExprAndRequestRules(expression)
        let compatible = taskExpressionCompatible(
          expression,
          variant.usage_schema
        )
        if (!separate && !usesExpression) compatible = true
        else if (compatible === undefined && expression === variant.effective) {
          compatible = variant.compatible
        }
        let modelCompatible = taskExpressionCompatible(
          props.modelExpression,
          variant.usage_schema
        )
        if (
          modelCompatible === undefined &&
          props.modelExpression === variant.effective
        ) {
          modelCompatible = variant.compatible
        }
        let inheritDescription = t('Use model-level expression')
        if (!usesExpression) inheritDescription = t('Use model-level pricing')
        return (
          <TabsContent key={key} value={key} keepMounted className='space-y-3'>
            <SettingsSwitchField
              controlId={`${id}-${key}`}
              checked={separate}
              label={t('Set separately for this provider')}
              description={!separate ? inheritDescription : undefined}
              onCheckedChange={(checked) =>
                props.onChange((current) => {
                  const next = { ...current }
                  if (!checked) {
                    delete next[key]
                    return next
                  }
                  next[key] =
                    usesExpression && modelCompatible
                      ? props.modelExpression
                      : generateTaskExprFromConfig(
                          createDefaultTaskVisualConfig(variant.usage_schema),
                          variant.usage_schema
                        )
                  return next
                })
              }
            />
            {compatible === false && (
              <Alert>
                <AlertDescription>
                  {t('Not configured for this provider')}
                </AlertDescription>
              </Alert>
            )}
            {compatible === undefined && (
              <Alert>
                <AlertDescription>
                  {t('Expression compatibility will be checked when saving.')}
                </AlertDescription>
              </Alert>
            )}
            {separate ? (
              <TaskUsagePricingEditor
                currency={props.currency}
                billingExpr={split.billingExpr}
                requestRuleExpr={split.requestRuleExpr}
                usageSchema={variant.usage_schema}
                usageExamples={variant.usage_examples}
                onBillingExprChange={(value) =>
                  props.onChange((current) => ({
                    ...current,
                    [key]: combineBillingExpr(
                      value,
                      splitBillingExprAndRequestRules(current[key] ?? '')
                        .requestRuleExpr
                    ),
                  }))
                }
                onRequestRuleExprChange={(value) =>
                  props.onChange((current) => ({
                    ...current,
                    [key]: combineBillingExpr(
                      splitBillingExprAndRequestRules(current[key] ?? '')
                        .billingExpr,
                      value
                    ),
                  }))
                }
              />
            ) : (
              usesExpression && (
                <code className='bg-muted/30 block max-h-40 overflow-auto rounded-md border p-3 text-xs break-all whitespace-pre-wrap'>
                  {expression}
                </code>
              )
            )}
          </TabsContent>
        )
      })}
    </Tabs>
  )
}
