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
import { FolderPlus, Plus, Ungroup, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { DataTableRowActionMenu } from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  createEmptyVisualCondition,
  visualNodeId,
  type VisualBillingIssue,
  type VisualComparison,
  type VisualCondition,
} from '@/features/pricing/lib/billing-expression/visual'

import {
  BillingConditionValueInput,
  BillingTimeProbeFields,
  BillingTimeRangeFields,
} from './billing-time-fields'

import './visual-condition-tree.css'

const OPERATORS: VisualComparison['operator'][] = [
  '<',
  '<=',
  '>',
  '>=',
  '==',
  '!=',
]

function conditionRange(
  node: VisualCondition
): [VisualComparison, VisualComparison] | null {
  if (node.kind !== 'all' && node.kind !== 'any') return null
  if (node.children.length !== 2) return null
  const [start, end] = node.children
  if (
    start.kind !== 'comparison' ||
    end.kind !== 'comparison' ||
    start.probe !== end.probe ||
    start.timezone !== end.timezone ||
    !['>', '>='].includes(start.operator) ||
    !['<', '<='].includes(end.operator)
  ) {
    return null
  }
  return [start, end]
}

/** Combine adjacent bounds for presentation; retain the original nodes and grouping when editing. */
function conditionRows(
  node: Extract<VisualCondition, { kind: 'all' | 'any' }>
): { node: VisualCondition; index: number; count: number }[] {
  const rows: { node: VisualCondition; index: number; count: number }[] = []
  for (let index = 0; index < node.children.length; index++) {
    const child = node.children[index]
    const next = node.children[index + 1]
    const pair: VisualCondition = {
      id: `range-${child.id}`,
      kind: 'all',
      children: next ? [child, next] : [child],
    }
    if (node.kind === 'all' && next && conditionRange(pair)) {
      rows.push({ node: pair, index, count: 2 })
      index++
    } else rows.push({ node: child, index, count: 1 })
  }
  return rows
}

function ComparisonOperator(props: {
  value: VisualComparison['operator']
  onChange: (value: VisualComparison['operator']) => void
}) {
  const { t } = useTranslation()
  return (
    <Select
      items={OPERATORS.map((value) => ({ value, label: value }))}
      value={props.value}
      onValueChange={(value) => value && props.onChange(value)}
    >
      <SelectTrigger
        size='sm'
        className='w-20'
        aria-label={t('Comparison operator')}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent alignItemWithTrigger={false}>
        {OPERATORS.map((operator) => (
          <SelectItem key={operator} value={operator}>
            {operator}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

type ConditionProps = {
  node: VisualCondition
  path: string
  issues: VisualBillingIssue[]
  onChange: (node: VisualCondition) => void
  onRemove?: () => void
  implicitRange?: boolean
}

function ConditionActions(props: ConditionProps & { group: boolean }) {
  const { t } = useTranslation()
  const node = props.node
  const range = conditionRange(node)
  const addCondition = (group: boolean) => {
    const child: VisualCondition = group
      ? { id: visualNodeId(), kind: 'any', children: [] }
      : createEmptyVisualCondition()
    props.onChange(
      node.kind === 'all' || node.kind === 'any'
        ? { ...node, children: [...node.children, child] }
        : { id: visualNodeId(), kind: 'all', children: [node, child] }
    )
  }
  return (
    <div className='ml-auto flex shrink-0 items-center gap-1 self-start'>
      {props.group && node.kind !== 'not' && (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                type='button'
                variant='ghost'
                size='sm'
                aria-label={t('Add to group {{path}}', { path: props.path })}
              />
            }
          >
            <Plus aria-hidden='true' className='size-3.5' />
            {t('Add')}
          </DropdownMenuTrigger>
          <DropdownMenuContent align='end' className='w-48'>
            <DropdownMenuItem onClick={() => addCondition(false)}>
              <Plus aria-hidden='true' />
              {t('Add condition')}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => addCondition(true)}>
              <FolderPlus aria-hidden='true' />
              {t('Add condition group')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
      <DataTableRowActionMenu
        ariaLabel={t('Condition actions {{path}}', { path: props.path })}
      >
        {!props.group && (
          <>
            <DropdownMenuItem onClick={() => addCondition(false)}>
              <Plus aria-hidden='true' />
              {t('Add condition')}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => addCondition(true)}>
              <FolderPlus aria-hidden='true' />
              {t('Add condition group')}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
          </>
        )}
        <DropdownMenuItem
          onClick={() =>
            props.onChange(
              node.kind === 'not'
                ? node.child
                : { id: visualNodeId(), kind: 'not', child: node }
            )
          }
        >
          <Ungroup aria-hidden='true' />
          {node.kind === 'not' ? t('Remove negation') : t('Negate condition')}
        </DropdownMenuItem>
        {range && (node.kind === 'all' || node.kind === 'any') && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={() => props.onChange({ ...node, children: [range[1]] })}
            >
              {t('Remove start condition')}
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => props.onChange({ ...node, children: [range[0]] })}
            >
              {t('Remove end condition')}
            </DropdownMenuItem>
          </>
        )}
        {props.onRemove && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant='destructive' onClick={props.onRemove}>
              <Trash2 aria-hidden='true' />
              {t('Remove condition')}
            </DropdownMenuItem>
          </>
        )}
      </DataTableRowActionMenu>
    </div>
  )
}

function ConditionFields(props: ConditionProps) {
  const { t } = useTranslation()
  const node = props.node
  const range = conditionRange(node)
  const comparison = node.kind === 'comparison' ? node : range?.[0]
  if (!comparison) return null
  const ids = range ? range.map((bound) => bound.id) : [comparison.id]
  const errors = props.issues.filter((issue) => ids.includes(issue.id))
  return (
    <>
      <div className='flex min-w-0 flex-1 flex-wrap items-center gap-2'>
        <BillingTimeProbeFields
          includeTokens
          probe={comparison.probe}
          timezone={comparison.timezone}
          invalidTimezone={errors.some(
            (issue) => issue.message === 'Choose a valid IANA timezone.'
          )}
          onChange={(probe, timezone) => {
            if (node.kind === 'comparison') {
              props.onChange({ ...node, probe, timezone })
            } else if (range && (node.kind === 'all' || node.kind === 'any')) {
              props.onChange({
                ...node,
                children: range.map((bound) => ({ ...bound, probe, timezone })),
              })
            }
          }}
        />
        {node.kind === 'comparison' && (
          <>
            <ComparisonOperator
              value={node.operator}
              onChange={(operator) => props.onChange({ ...node, operator })}
            />
            <BillingConditionValueInput
              probe={node.probe}
              value={node.value}
              invalid={errors.some(
                (issue) => issue.message === 'Enter a valid condition value.'
              )}
              onChange={(value) => props.onChange({ ...node, value })}
            />
          </>
        )}
        {range && (node.kind === 'all' || node.kind === 'any') && (
          <BillingTimeRangeFields
            probe={comparison.probe}
            start={range[0].value}
            end={range[1].value}
            invalidStart={errors.some(
              (issue) =>
                issue.id === range[0].id &&
                issue.message === 'Enter a valid condition value.'
            )}
            invalidEnd={errors.some(
              (issue) =>
                issue.id === range[1].id &&
                issue.message === 'Enter a valid condition value.'
            )}
            startOperator={
              <ComparisonOperator
                value={range[0].operator}
                onChange={(operator) =>
                  props.onChange({
                    ...node,
                    children: [{ ...range[0], operator }, range[1]],
                  })
                }
              />
            }
            endOperator={
              <ComparisonOperator
                value={range[1].operator}
                onChange={(operator) =>
                  props.onChange({
                    ...node,
                    children: [range[0], { ...range[1], operator }],
                  })
                }
              />
            }
            onChange={(start, end) =>
              props.onChange({
                ...node,
                children: [
                  { ...range[0], value: start },
                  { ...range[1], value: end },
                ],
              })
            }
          />
        )}
      </div>
      {[...new Map(errors.map((issue) => [issue.message, issue])).values()].map(
        (issue) => (
          <p
            key={`${issue.id}:${issue.message}`}
            role='alert'
            className='text-destructive w-full text-xs'
          >
            {t(issue.message)}
          </p>
        )
      )}
    </>
  )
}

export function VisualConditionTree(props: ConditionProps) {
  const { t } = useTranslation()
  const node = props.node
  const range = conditionRange(node)
  const group = node.kind !== 'comparison' && !props.implicitRange
  if (!group) {
    return (
      <div
        role='group'
        aria-label={t('Condition {{path}}', { path: props.path })}
        className='billing-condition-row flex flex-wrap items-center gap-2'
      >
        <ConditionFields {...props} />
        <ConditionActions {...props} group={false} />
      </div>
    )
  }
  return (
    <section
      role='group'
      aria-label={t('Condition group {{path}}', { path: props.path })}
      data-condition-kind={node.kind}
      className='billing-condition-group min-w-0'
    >
      <div className='billing-condition-header flex flex-wrap items-center gap-2 px-3 py-2'>
        {node.kind === 'not' ? (
          <Badge className='billing-condition-label' variant='outline'>
            {t('Negated group')}
          </Badge>
        ) : (
          <Select
            items={[
              { value: 'all', label: t('All conditions') },
              { value: 'any', label: t('Any condition') },
            ]}
            value={node.kind}
            onValueChange={(kind) => kind && props.onChange({ ...node, kind })}
          >
            <SelectTrigger
              aria-label={t('Condition group')}
              size='sm'
              className='bg-background w-36'
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent alignItemWithTrigger={false}>
              <SelectItem value='all'>{t('All conditions')}</SelectItem>
              <SelectItem value='any'>{t('Any condition')}</SelectItem>
            </SelectContent>
          </Select>
        )}
        <span className='billing-condition-label text-xs font-medium'>
          {t('Group {{path}}', { path: props.path })}
        </span>
        <ConditionActions {...props} group />
      </div>
      {range ? (
        <div className='billing-condition-range'>
          <div className='billing-condition-row flex flex-wrap items-center gap-2'>
            <ConditionFields {...props} />
          </div>
        </div>
      ) : (
        <ol className='billing-condition-children'>
          {node.kind === 'not' ? (
            <li>
              <VisualConditionTree
                {...props}
                node={node.child}
                path={`${props.path}.1`}
                implicitRange={false}
                onRemove={undefined}
                onChange={(child) => props.onChange({ ...node, child })}
              />
            </li>
          ) : (
            conditionRows(node).map((row, rowIndex) => (
              <li key={row.node.id}>
                <VisualConditionTree
                  {...props}
                  node={row.node}
                  path={`${props.path}.${rowIndex + 1}`}
                  implicitRange={row.count === 2}
                  onChange={(next) => {
                    const replacement =
                      row.count === 2 && next.kind === 'all'
                        ? next.children
                        : [next]
                    props.onChange({
                      ...node,
                      children: [
                        ...node.children.slice(0, row.index),
                        ...replacement,
                        ...node.children.slice(row.index + row.count),
                      ],
                    })
                  }}
                  onRemove={() =>
                    props.onChange({
                      ...node,
                      children: node.children.filter(
                        (_, index) =>
                          index < row.index || index >= row.index + row.count
                      ),
                    })
                  }
                />
              </li>
            ))
          )}
        </ol>
      )}
      {props.issues
        .filter((issue) => issue.id === node.id)
        .map((issue) => (
          <p
            key={`${issue.id}:${issue.message}`}
            role='alert'
            className='text-destructive px-3 pb-2 text-xs'
          >
            {t(issue.message)}
          </p>
        ))}
    </section>
  )
}
