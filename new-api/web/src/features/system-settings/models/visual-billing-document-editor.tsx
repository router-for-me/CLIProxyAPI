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
import { ChevronDown } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { DataTableRowActionMenu } from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { DropdownMenuItem } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import {
  formatPricingAmount,
  type PricingCurrency,
} from '@/features/model-pricing/currency'
import { BILLING_VARS } from '@/features/pricing/lib/billing-expr'
import { formatBillingCondition } from '@/features/pricing/lib/billing-expression/condition-display'
import {
  visualConditionExpression,
  visualNodeId,
  createEmptyVisualCondition,
  type VisualBillingDocument,
  type VisualBillingIssue,
  type VisualPricingNode,
  type VisualCondition,
} from '@/features/pricing/lib/billing-expression/visual'

import { TierPriceFields } from './tier-price-fields'
import { VisualConditionTree } from './visual-condition-tree'

type PricingNodeProps = {
  node: VisualPricingNode
  source: string
  currency: PricingCurrency
  issues: VisualBillingIssue[]
  onChange: (node: VisualPricingNode) => void
}

function PricingTierFields(
  props: PricingNodeProps & {
    node: Extract<VisualPricingNode, { kind: 'tier' }>
  }
) {
  const { t } = useTranslation()
  const node = props.node
  const issues = props.issues.filter(
    (issue) => issue.id === node.id || issue.id.startsWith(`${node.id}:`)
  )
  return (
    <div className='min-w-0 space-y-3'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='flex min-w-0 items-center gap-2'>
          <Badge variant='secondary'>{t('Tier')}</Badge>
          <Input
            aria-label={t('Tier name')}
            value={node.label}
            onChange={(event) =>
              props.onChange({ ...node, label: event.target.value })
            }
            className='w-40 max-w-full'
          />
        </div>
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={() =>
            props.onChange({
              id: visualNodeId(),
              kind: 'branch',
              condition: createEmptyVisualCondition(),
              yes: {
                ...node,
                id: visualNodeId(),
                origin: undefined,
                prices: node.prices.map(({ variable, value }) => ({
                  variable,
                  value,
                })),
              },
              no: node,
            })
          }
        >
          {t('Add pricing branch')}
        </Button>
      </div>
      <TierPriceFields
        currency={props.currency}
        billingUnit={node.billingUnit}
        fixedPrice={node.fixedPrice}
        onBillingUnitChange={(billingUnit) =>
          props.onChange({
            ...node,
            billingUnit,
            prices:
              billingUnit === 'token' && node.prices.length === 0
                ? [
                    { variable: 'p', value: '0' },
                    { variable: 'c', value: '0' },
                  ]
                : node.prices,
          })
        }
        onFixedPriceChange={(fixedPrice) =>
          props.onChange({ ...node, fixedPrice })
        }
        prices={Object.fromEntries(
          node.prices.map((price) => [price.variable, price.value])
        )}
        invalidVariables={issues.map((issue) =>
          issue.id.slice(node.id.length + 1)
        )}
        onChange={(variable, value) =>
          props.onChange({
            ...node,
            prices: node.prices.map((price) =>
              price.variable === variable ? { ...price, value } : price
            ),
          })
        }
        onInclude={(variable, included) =>
          props.onChange({
            ...node,
            prices: included
              ? [...node.prices, { variable, value: '0' }]
              : node.prices.filter((price) => price.variable !== variable),
          })
        }
      />
      {issues.map((issue) => (
        <p
          role='alert'
          key={`${issue.id}:${issue.message}`}
          className='text-destructive text-xs'
        >
          {t(issue.message)}
        </p>
      ))}
    </div>
  )
}

function PricingRuleCard(
  props: PricingNodeProps & {
    number: string
    first: boolean
    fallback: boolean
  }
) {
  const { t, i18n } = useTranslation()
  const node = props.node
  const [open, setOpen] = useState(props.first)
  const tier = node.kind === 'tier' ? node : node.yes
  const name =
    tier.kind === 'tier'
      ? tier.label
      : t('Pricing rule {{number}}', { number: props.number })
  const condition =
    node.kind === 'branch'
      ? visualConditionExpression(node.condition, props.source)
      : null
  const description =
    condition && formatBillingCondition(condition, t, i18n.language)
  let matchDescription = description || t('Tier conditions')
  if (node.kind === 'tier') {
    matchDescription = props.fallback
      ? t('No preceding rule matched')
      : t('Always matches (default tier).')
  } else if (!props.first) {
    matchDescription = t('If no preceding rule matched: {{condition}}', {
      condition: matchDescription,
    })
  }

  // A later rule owns its conditions and matched subtree, but not the following fallback rules.
  const ids = new Set<string>()
  const pending: (VisualPricingNode | VisualCondition)[] =
    node.kind === 'tier' ? [node] : [node.condition, node.yes]
  for (let index = 0; index < pending.length; index++) {
    const current = pending[index]
    ids.add(current.id)
    if (current.kind === 'branch') {
      pending.push(current.condition, current.yes, current.no)
    }
    if (current.kind === 'all' || current.kind === 'any') {
      pending.push(...current.children)
    }
    if (current.kind === 'not') pending.push(current.child)
  }
  const hasIssues = props.issues.some((issue) =>
    ids.has(issue.id.split(':')[0])
  )
  useEffect(() => {
    if (hasIssues) setOpen(true)
  }, [hasIssues])

  return (
    <Collapsible
      open={open || hasIssues}
      onOpenChange={setOpen}
      render={
        <section
          role='group'
          aria-label={t('Pricing tier {{name}}', { name })}
        />
      }
      className='bg-background min-w-0 overflow-hidden rounded-xl border data-open:border-blue-300 dark:data-open:border-blue-800'
    >
      <div className='flex items-start gap-1 p-3'>
        <CollapsibleTrigger
          aria-label={t('Edit pricing rule {{name}}', { name })}
          className='flex min-w-0 flex-1 items-start gap-3 rounded-md text-left focus-visible:outline-2 focus-visible:outline-offset-2'
        >
          <Badge variant='secondary' className='mt-0.5 shrink-0 tabular-nums'>
            {props.number}
          </Badge>
          <span className='min-w-0 flex-1 space-y-2'>
            <span className='flex flex-wrap items-center gap-2 font-medium'>
              <span className='break-all'>{name}</span>
              {props.fallback && (
                <Badge variant='outline'>{t('Fallback tier')}</Badge>
              )}
            </span>
            <span className='text-muted-foreground block text-sm break-words'>
              {matchDescription}
            </span>
            {tier.kind === 'tier' && (
              <span className='flex flex-wrap gap-x-4 gap-y-1 text-xs tabular-nums'>
                {tier.billingUnit === 'request' ? (
                  <span>
                    {t('Price per request')}:{' '}
                    {formatPricingAmount(tier.fixedPrice, props.currency) ||
                      '—'}
                    /{t('request')}
                  </span>
                ) : (
                  <>
                    {tier.prices.map((price) => (
                      <span key={price.variable}>
                        {t(
                          BILLING_VARS.find(
                            (variable) => variable.key === price.variable
                          )?.shortLabel ?? price.variable
                        )}
                        :{' '}
                        {formatPricingAmount(price.value, props.currency) ||
                          '—'}
                      </span>
                    ))}
                    <span className='text-muted-foreground'>
                      /{t('1M token')}
                    </span>
                  </>
                )}
              </span>
            )}
          </span>
          <ChevronDown
            aria-hidden='true'
            className={`mt-1 size-4 shrink-0 transition-transform ${open || hasIssues ? 'rotate-180' : ''}`}
          />
        </CollapsibleTrigger>
        {node.kind === 'branch' && (
          <DataTableRowActionMenu
            ariaLabel={t('Branch actions {{path}}', { path: props.number })}
          >
            <DropdownMenuItem
              variant='destructive'
              onClick={() => props.onChange(node.no)}
            >
              {t('Remove branch')}
            </DropdownMenuItem>
          </DataTableRowActionMenu>
        )}
      </div>
      <CollapsibleContent keepMounted className='border-t p-3'>
        <div className='space-y-4'>
          {node.kind === 'branch' && (
            <div className='space-y-2'>
              <p className='text-sm font-medium'>{t('Tier conditions')}</p>
              <VisualConditionTree
                node={node.condition}
                path={props.number}
                issues={props.issues}
                onChange={(condition) => props.onChange({ ...node, condition })}
              />
            </div>
          )}
          {tier.kind === 'tier' ? (
            <PricingTierFields
              {...props}
              node={tier}
              onChange={(next) =>
                props.onChange(
                  node.kind === 'tier' ? next : { ...node, yes: next }
                )
              }
            />
          ) : (
            <div className='space-y-3 border-l-2 pl-3'>
              <p className='text-sm font-medium'>
                {t('When conditions match')}
              </p>
              <PricingRuleList
                {...props}
                node={tier}
                prefix={`${props.number}.`}
                onChange={(yes) =>
                  node.kind === 'branch' && props.onChange({ ...node, yes })
                }
              />
            </div>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}

function PricingRuleList(props: PricingNodeProps & { prefix: string }) {
  const { t } = useTranslation()
  const rules: VisualPricingNode[] = []
  let current = props.node
  while (current.kind === 'branch') {
    rules.push(current)
    current = current.no
  }
  rules.push(current)
  return (
    <ol aria-label={t('Pricing rules')} className='min-w-0 space-y-3'>
      {rules.map((node, index) => (
        <li key={node.id} className='min-w-0'>
          <PricingRuleCard
            {...props}
            node={node}
            number={`${props.prefix}${index + 1}`}
            first={index === 0}
            fallback={index > 0 && node.kind === 'tier'}
            onChange={(next) => {
              let root = next
              for (let previous = index - 1; previous >= 0; previous--) {
                const branch = rules[previous]
                if (branch.kind === 'branch') root = { ...branch, no: root }
              }
              props.onChange(root)
            }}
          />
        </li>
      ))}
    </ol>
  )
}

export function VisualBillingDocumentEditor(props: {
  document: VisualBillingDocument
  currency: PricingCurrency
  issues: VisualBillingIssue[]
  onChange: (document: VisualBillingDocument) => void
}) {
  const { t } = useTranslation()
  return (
    <div className='space-y-3'>
      {props.document.root.kind === 'branch' && (
        <p className='text-muted-foreground text-xs'>
          {t(
            'Rules are checked in order. The first match determines the price.'
          )}
        </p>
      )}
      <PricingRuleList
        node={props.document.root}
        prefix=''
        source={props.document.source}
        currency={props.currency}
        issues={props.issues}
        onChange={(root) => props.onChange({ ...props.document, root })}
      />
    </div>
  )
}
