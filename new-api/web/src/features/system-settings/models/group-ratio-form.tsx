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
import { Code2, Eye, HelpCircle } from 'lucide-react'
import { memo, useCallback, useState, type ReactNode } from 'react'
import { useWatch, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  sideDrawerContentClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { JsonCodeEditor } from '@/components/json-code-editor'
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from '@/components/ui/accordion'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageActionsPortal } from '../components/settings-page-context'
import { safeNumberFieldProps } from '../utils/numeric-field'
import {
  GroupRatioVisualEditor,
  type GroupSettingsSection,
} from './group-ratio-visual-editor'

type GroupFormValues = {
  GroupRatio: string
  TopupGroupRatio: string
  UserUsableGroups: string
  GroupGroupRatio: string
  AutoGroups: string
  MaxTokenAutoGroups: number
  DefaultUseAutoGroup: boolean
  GroupSpecialUsableGroup: string
}

type GroupRatioFormProps = {
  form: UseFormReturn<GroupFormValues>
  onSave: (values: GroupFormValues) => Promise<void>
  isSaving: boolean
}

export const GroupRatioForm = memo(function GroupRatioForm({
  form,
  onSave,
  isSaving,
}: GroupRatioFormProps) {
  const { t } = useTranslation()
  const values = useWatch({ control: form.control })
  const [editMode, setEditMode] = useState<'visual' | 'json'>('visual')
  const [guideOpen, setGuideOpen] = useState(false)
  const [section, setSection] = useState<GroupSettingsSection>('pricing')

  const handleFieldChange = useCallback(
    (field: keyof GroupFormValues, value: string) => {
      form.setValue(field, value, {
        shouldValidate: true,
        shouldDirty: true,
      })
    },
    [form]
  )

  const toggleEditMode = useCallback(() => {
    setEditMode((prev) => (prev === 'visual' ? 'json' : 'visual'))
  }, [])

  return (
    <div className='space-y-6'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <p className='text-muted-foreground text-sm'>
          {editMode === 'visual'
            ? t(
                'Manage pricing, visibility and automatic routing for your groups.'
              )
            : t(
                'Edit group settings as JSON. Changes are shared with the visual editor.'
              )}
        </p>
        <div className='flex shrink-0 gap-2'>
          <Button
            variant='outline'
            size='sm'
            onClick={() => setGuideOpen(true)}
          >
            <HelpCircle className='mr-2 h-4 w-4' />
            {t('Usage guide')}
          </Button>
          <Button variant='outline' size='sm' onClick={toggleEditMode}>
            {editMode === 'visual' ? (
              <>
                <Code2 className='mr-2 h-4 w-4' />
                {t('Switch to JSON')}
              </>
            ) : (
              <>
                <Eye className='mr-2 h-4 w-4' />
                {t('Switch to Visual')}
              </>
            )}
          </Button>
        </div>
      </div>

      <GroupPricingGuide open={guideOpen} onOpenChange={setGuideOpen} />

      <Form {...form}>
        <SettingsPageActionsPortal>
          <Button
            type='button'
            size='sm'
            onClick={form.handleSubmit(onSave, (errors) => {
              if (errors.MaxTokenAutoGroups) {
                setEditMode('visual')
                setSection('auto')
                requestAnimationFrame(() => form.setFocus('MaxTokenAutoGroups'))
              }
            })}
            disabled={isSaving}
          >
            {isSaving ? t('Saving...') : t('Save group settings')}
          </Button>
        </SettingsPageActionsPortal>
        {editMode === 'visual' ? (
          <GroupRatioVisualEditor
            section={section}
            onSectionChange={setSection}
            groupRatio={values.GroupRatio ?? ''}
            topupGroupRatio={values.TopupGroupRatio ?? ''}
            userUsableGroups={values.UserUsableGroups ?? ''}
            groupGroupRatio={values.GroupGroupRatio ?? ''}
            autoGroups={values.AutoGroups ?? ''}
            maxTokenAutoGroupsField={
              <FormField
                control={form.control}
                name='MaxTokenAutoGroups'
                render={({ field, fieldState }) => (
                  <FormItem data-invalid={fieldState.invalid}>
                    <FormLabel>
                      {t('Maximum custom groups per token')}
                    </FormLabel>
                    <FormControl>
                      <Input
                        {...safeNumberFieldProps(field)}
                        type='number'
                        min={1}
                        step={1}
                        aria-invalid={fieldState.invalid}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Limits only token-specific Auto snapshots. Global Auto inheritance remains unlimited.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            }
            defaultUseAutoGroupField={
              <FormField
                control={form.control}
                name='DefaultUseAutoGroup'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Default to auto groups')}</FormLabel>
                      <FormDescription>
                        {t(
                          'If default auto group is enabled, newly created tokens start with auto instead of an empty group.'
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />
            }
            groupSpecialUsableGroup={values.GroupSpecialUsableGroup ?? ''}
            onChange={(field, value) =>
              handleFieldChange(field as keyof GroupFormValues, value)
            }
          />
        ) : (
          <SettingsForm onSubmit={form.handleSubmit(onSave)}>
            <FormField
              control={form.control}
              name='GroupRatio'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Group ratios')}</FormLabel>
                  <FormControl>
                    <JsonCodeEditor
                      value={field.value}
                      onChange={field.onChange}
                      name={field.name}
                      onBlur={field.onBlur}
                      textareaRef={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'JSON map of group → ratio applied when the user selects the group explicitly.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='TopupGroupRatio'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Top-up group ratios')}</FormLabel>
                  <FormControl>
                    <JsonCodeEditor
                      value={field.value}
                      onChange={field.onChange}
                      name={field.name}
                      onBlur={field.onBlur}
                      textareaRef={field.ref}
                      heightClassName='h-40 min-h-40 max-h-40'
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Optional multiplier per user group used when calculating recharge pricing. Provide a JSON object such as'
                    )}
                    {` { "default": 1, "vip": 1.2 }`}.
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='UserUsableGroups'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Selectable groups')}</FormLabel>
                  <FormControl>
                    <JsonCodeEditor
                      value={field.value}
                      onChange={field.onChange}
                      name={field.name}
                      onBlur={field.onBlur}
                      textareaRef={field.ref}
                      heightClassName='h-40 min-h-40 max-h-40'
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'JSON map of group → description exposed when users create API keys.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='GroupGroupRatio'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Inter-group overrides')}</FormLabel>
                  <FormControl>
                    <JsonCodeEditor
                      value={field.value}
                      onChange={field.onChange}
                      name={field.name}
                      onBlur={field.onBlur}
                      textareaRef={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t('Nested JSON: source group →')}{' '}
                    {`{ targetGroup: ratio }`}{' '}
                    {t(
                      'to override billing when a user in one group uses a token of another group.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='AutoGroups'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Auto group order')}</FormLabel>
                  <FormControl>
                    <JsonCodeEditor
                      value={field.value}
                      onChange={field.onChange}
                      name={field.name}
                      onBlur={field.onBlur}
                      textareaRef={field.ref}
                      heightClassName='h-40 min-h-40 max-h-40'
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Priority order for tokens in the auto group. The system tries groups from top to bottom.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='MaxTokenAutoGroups'
              render={({ field, fieldState }) => (
                <FormItem data-invalid={fieldState.invalid}>
                  <FormLabel>{t('Maximum custom groups per token')}</FormLabel>
                  <FormControl>
                    <Input
                      {...safeNumberFieldProps(field)}
                      type='number'
                      min={1}
                      step={1}
                      aria-invalid={fieldState.invalid}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Limits only token-specific Auto snapshots. Global Auto inheritance remains unlimited.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='GroupSpecialUsableGroup'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Special usable group rules')}</FormLabel>
                  <FormControl>
                    <JsonCodeEditor
                      value={field.value}
                      onChange={field.onChange}
                      name={field.name}
                      onBlur={field.onBlur}
                      textareaRef={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Nested JSON defining per-group rules for adding (+:), removing (-:), or appending usable groups.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='DefaultUseAutoGroup'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Default to auto groups')}</FormLabel>
                    <FormDescription>
                      {t(
                        'If default auto group is enabled, newly created tokens start with auto instead of an empty group.'
                      )}
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
          </SettingsForm>
        )}
      </Form>
    </div>
  )
})

type GroupPricingGuideProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

function GuideCodeBlock({ children }: { children: string }) {
  return (
    <pre className='bg-muted/60 overflow-x-auto rounded-lg border px-3 py-2 text-xs leading-6 whitespace-pre-wrap'>
      {children}
    </pre>
  )
}

function GuideStepRow({
  chip,
  children,
}: {
  chip: string
  children: ReactNode
}) {
  return (
    <div className='flex items-start gap-2.5 text-sm leading-6'>
      <span className='bg-muted text-muted-foreground mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-xs font-medium'>
        {chip}
      </span>
      <span className='text-muted-foreground min-w-0'>{children}</span>
    </div>
  )
}

function GroupPricingGuide({ open, onOpenChange }: GroupPricingGuideProps) {
  const { t } = useTranslation()

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side='right'
        className={sideDrawerContentClassName('sm:max-w-2xl')}
      >
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle>{t('Group pricing usage guide')}</SheetTitle>
          <SheetDescription>
            {t(
              'Understand how user groups, token groups, ratios, and special rules work together.'
            )}
          </SheetDescription>
        </SheetHeader>

        <div className={sideDrawerFormClassName('gap-5')}>
          <section className='space-y-2'>
            <h3 className='text-sm font-semibold'>
              {t('The two roles of a group')}
            </h3>
            <div className='text-muted-foreground space-y-2 text-sm leading-6'>
              <p>
                {t(
                  'Every group name in the pricing table can be used in two places: on a user (the user group, assigned by admins) and on a token (the token group, chosen when creating the token). Same name pool, two different jobs.'
                )}
              </p>
              <p>
                <span className='text-foreground font-medium'>
                  {t('Token group')}
                </span>
                {': '}
                {t(
                  'decides which channels are used and which base ratio applies.'
                )}
              </p>
              <p>
                <span className='text-foreground font-medium'>
                  {t('User group')}
                </span>
                {': '}
                {t(
                  'decides the top-up ratio, which groups the user can pick for tokens, and whether an override ratio applies.'
                )}
              </p>
            </div>
          </section>

          <section className='space-y-2'>
            <h3 className='text-sm font-semibold'>
              {t('How a call is priced')}
            </h3>
            <ol className='text-muted-foreground list-decimal space-y-2 pl-5 text-sm leading-6'>
              <li>
                <span className='text-foreground font-medium'>
                  {t('Find the billing group.')}
                </span>{' '}
                {t(
                  'Use the group set on the token. If the token has no group, use the user group. The auto group tries the configured groups from top to bottom.'
                )}
              </li>
              <li>
                <span className='text-foreground font-medium'>
                  {t('Find the ratio.')}
                </span>{' '}
                {t(
                  'Look for a special ratio rule matching this user group and this billing group. If one exists, use its ratio. Otherwise use the billing group base ratio from the pricing table.'
                )}
              </li>
              <li>
                <span className='text-foreground font-medium'>
                  {t('Charge.')}
                </span>{' '}
                {t(
                  'Cost = model price × that one ratio. Nothing else from the group settings enters the formula.'
                )}
              </li>
            </ol>
            <p className='text-muted-foreground text-sm leading-6'>
              {t(
                'Common pitfall: the user group base ratio is NOT a personal discount. It only applies when the user group itself is the billing group.'
              )}
            </p>
          </section>

          <section className='space-y-3'>
            <h3 className='text-sm font-semibold'>{t('Worked example')}</h3>
            <p className='text-muted-foreground text-sm leading-6'>
              {t(
                'The admin configured three groups and one special ratio rule:'
              )}
            </p>

            <div className='overflow-hidden rounded-lg border'>
              <div className='bg-muted/40 border-b px-3 py-1.5 text-xs font-medium'>
                {t('Pricing groups')}
              </div>
              <table className='w-full text-sm'>
                <thead>
                  <tr className='text-muted-foreground border-b text-xs'>
                    <th className='px-3 py-1.5 text-left font-medium'>
                      {t('Group name')}
                    </th>
                    <th className='px-3 py-1.5 text-right font-medium'>
                      {t('Ratio')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  <tr className='border-b'>
                    <td className='px-3 py-1.5'>default</td>
                    <td className='px-3 py-1.5 text-right'>1.0</td>
                  </tr>
                  <tr className='border-b'>
                    <td className='px-3 py-1.5'>premium</td>
                    <td className='px-3 py-1.5 text-right'>0.5</td>
                  </tr>
                  <tr>
                    <td className='px-3 py-1.5'>vip</td>
                    <td className='px-3 py-1.5 text-right'>0.8</td>
                  </tr>
                </tbody>
              </table>
            </div>

            <div className='overflow-hidden rounded-lg border'>
              <div className='bg-muted/40 border-b px-3 py-1.5 text-xs font-medium'>
                {t('Special ratio rules')}
              </div>
              <div className='p-3 text-sm leading-6'>
                {t('Users of vip, when billed as premium, pay ratio')}{' '}
                <span className='bg-primary/10 ring-primary/40 rounded px-1.5 py-0.5 font-semibold ring-1'>
                  0.3
                </span>{' '}
                <span className='text-muted-foreground text-xs'>
                  {t('(instead of {{ratio}})', { ratio: 0.5 })}
                </span>
              </div>
            </div>

            <p className='text-muted-foreground text-sm leading-6'>
              {t(
                'Three calls made by the same vip user. Assume the base price of one call is 10.'
              )}
            </p>

            <div className='space-y-3'>
              <div className='overflow-hidden rounded-lg border'>
                <div className='bg-muted/40 border-b px-3 py-2 text-sm font-medium'>
                  {t('Call 1: the token group is premium')}
                </div>
                <div className='space-y-2 p-3'>
                  <GuideStepRow chip='1'>
                    {t(
                      'Billing group = premium (the token has a group, so use it)'
                    )}
                  </GuideStepRow>
                  <GuideStepRow chip='2'>
                    {t(
                      'There is a rule for vip billed as premium → use its ratio 0.3'
                    )}
                  </GuideStepRow>
                  <GuideStepRow chip='='>
                    <span className='text-foreground font-medium'>
                      {t('Cost = 10 × 0.3 = 3')}
                    </span>
                  </GuideStepRow>
                </div>
              </div>

              <div className='overflow-hidden rounded-lg border'>
                <div className='bg-muted/40 border-b px-3 py-2 text-sm font-medium'>
                  {t('Call 2: the token group is default')}
                </div>
                <div className='space-y-2 p-3'>
                  <GuideStepRow chip='1'>
                    {t(
                      'Billing group = default (the token has a group, so use it)'
                    )}
                  </GuideStepRow>
                  <GuideStepRow chip='2'>
                    {t(
                      'No rule for vip billed as default → use the base ratio of default, 1.0 (the 0.8 of vip is not used)'
                    )}
                  </GuideStepRow>
                  <GuideStepRow chip='='>
                    <span className='text-foreground font-medium'>
                      {t('Cost = 10 × 1.0 = 10')}
                    </span>
                  </GuideStepRow>
                </div>
              </div>

              <div className='overflow-hidden rounded-lg border'>
                <div className='bg-muted/40 border-b px-3 py-2 text-sm font-medium'>
                  {t('Call 3: the token has no group')}
                </div>
                <div className='space-y-2 p-3'>
                  <GuideStepRow chip='1'>
                    {t(
                      'Billing group = vip (the token has no group, so use the user group)'
                    )}
                  </GuideStepRow>
                  <GuideStepRow chip='2'>
                    {t(
                      'No rule for vip billed as vip → use the base ratio of vip, 0.8'
                    )}
                  </GuideStepRow>
                  <GuideStepRow chip='='>
                    <span className='text-foreground font-medium'>
                      {t('Cost = 10 × 0.8 = 8')}
                    </span>
                  </GuideStepRow>
                </div>
              </div>
            </div>
          </section>

          <Accordion className='rounded-lg border px-3'>
            <AccordionItem value='groups'>
              <AccordionTrigger>{t('Pricing group example')}</AccordionTrigger>
              <AccordionContent className='space-y-3'>
                <p className='text-muted-foreground text-sm leading-6'>
                  {t(
                    'Use the pricing group table to manage the ratio and whether the group appears in the token creation dropdown.'
                  )}
                </p>
                <GuideCodeBlock>
                  {`${t('Group name')}   ${t('Ratio')}   ${t('User selectable')}   ${t('Description')}
standard     1.0     ${t('Yes')}               ${t('Standard price')}
premium      0.5     ${t('Yes')}               ${t('Premium plan, half price')}
vip          0.5     ${t('No')}                ${t('Assigned by administrator only')}`}
                </GuideCodeBlock>
                <p className='text-muted-foreground text-sm leading-6'>
                  {t(
                    'Users only see groups marked as user selectable. Non-selectable groups can still be assigned by administrators.'
                  )}
                </p>
              </AccordionContent>
            </AccordionItem>

            <AccordionItem value='auto'>
              <AccordionTrigger>{t('Auto group behavior')}</AccordionTrigger>
              <AccordionContent className='space-y-3'>
                <p className='text-muted-foreground text-sm leading-6'>
                  {t(
                    'When a token uses the auto group, the system tries groups from top to bottom until it finds an available group.'
                  )}
                </p>
                <GuideCodeBlock>{`["default", "vip"]`}</GuideCodeBlock>
                <p className='text-muted-foreground text-sm leading-6'>
                  {t(
                    'If default auto group is enabled, newly created tokens start with auto instead of an empty group.'
                  )}
                </p>
              </AccordionContent>
            </AccordionItem>

            <AccordionItem value='special-ratio'>
              <AccordionTrigger>{t('Special ratio rules')}</AccordionTrigger>
              <AccordionContent className='space-y-3'>
                <p className='text-muted-foreground text-sm leading-6'>
                  {t(
                    'In JSON, the user group is the outer key and the billing group is the inner key. The example below means: vip users pay 0.8 when billed as standard, and 0.3 when billed as premium.'
                  )}
                </p>
                <GuideCodeBlock>{`{
  "vip": {
    "standard": 0.8,
    "premium": 0.3
  }
}`}</GuideCodeBlock>
                <p className='text-muted-foreground text-sm leading-6'>
                  {t(
                    'Only configured combinations are overridden. All other calls keep the billing group base ratio.'
                  )}
                </p>
              </AccordionContent>
            </AccordionItem>

            <AccordionItem value='usable'>
              <AccordionTrigger>
                {t('Special usable group rules')}
              </AccordionTrigger>
              <AccordionContent className='space-y-3'>
                <p className='text-muted-foreground text-sm leading-6'>
                  {t(
                    'Special usable group rules make extra token groups visible to, or hide default ones from, users of a specific user group.'
                  )}
                </p>
                <GuideCodeBlock>{`{
  "vip": {
    "+:premium": "${t('Premium plan, half price')}",
    "-:default": "remove",
    "special": "${t('Special group')}"
  }
}`}</GuideCodeBlock>
                <p className='text-muted-foreground text-sm leading-6'>
                  {t(
                    'In the visual editor these appear as Extra visible and Hidden. In JSON, +: (or no prefix) adds a group and -: removes one.'
                  )}
                </p>
              </AccordionContent>
            </AccordionItem>
          </Accordion>
        </div>
      </SheetContent>
    </Sheet>
  )
}
