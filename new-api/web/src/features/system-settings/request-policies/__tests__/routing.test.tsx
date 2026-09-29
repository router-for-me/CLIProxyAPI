import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createRouter,
  createRootRoute,
  createMemoryHistory,
  RouterContextProvider,
} from '@tanstack/react-router'
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  cleanup,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { useState } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { RoutingPolicySection } from '../routing-section'

const options = {
  RetryTimes: '2',
  'channel_affinity_setting.enabled': 'true',
  'channel_affinity_setting.session_mode': '',
  'channel_affinity_setting.switch_on_success': 'false',
  'channel_affinity_setting.keep_on_channel_disabled': 'false',
  'channel_affinity_setting.max_entries': '100000',
  'channel_affinity_setting.default_ttl_seconds': '3600',
  'channel_affinity_setting.rules': JSON.stringify([
    {
      name: 'Session rule',
      model_regex: ['.*'],
      path_regex: [],
      key_sources: [{ type: 'request_header', key: 'X-Session' }],
      ttl_seconds: 0,
      session_mode: 'prefer',
      skip_retry_on_failure: false,
      include_using_group: false,
      include_rule_name: true,
      include_model_name: false,
      param_override_template: { temperature: 0 },
      future_field: { retained: true },
    },
  ]),
  AutomaticRetryStatusCodes: '429,500-503',
}
let currentOptions: Record<string, string>
let client: QueryClient
beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  currentOptions = { ...options }
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/option/request_policy'
          ? {
              options: currentOptions,
            }
          : {
              enabled: true,
              total: 2,
              unknown: 0,
              by_rule_name: { 'Session rule': 2 },
              cache_capacity: 100000,
              cache_algo: 'lru',
            },
    },
  }))
  vi.spyOn(api, 'patch').mockImplementation(async (_url, request) => {
    currentOptions = {
      ...currentOptions,
      ...(request as { options: Record<string, string> }).options,
    }
    return {
      data: {
        success: true,
        data: { options: currentOptions },
      },
    }
  })
})
afterEach(async () => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
  await i18next.changeLanguage('en')
})
function Workspace() {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <RoutingPolicySection />
      </SettingsPageProvider>
    </>
  )
}
function show() {
  const router = createRouter({
    routeTree: createRootRoute(),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterContextProvider router={router}>
        <Workspace />
      </RouterContextProvider>
    </QueryClientProvider>
  )
}
it('opens global affinity settings and the complete rules table without switching views', async () => {
  show()
  expect(await screen.findByRole('table')).toBeVisible()
  expect(
    screen.getByRole('radio', {
      name: 'Prefer the original channel, allow switching',
    })
  ).toBeChecked()
  expect(
    screen.getByRole('radio', { name: 'Do not keep sessions' })
  ).toBeVisible()
  expect(
    screen.getByRole('radio', {
      name: 'Prefer the original channel, allow switching',
    })
  ).toBeVisible()
  expect(
    screen.getByRole('radio', {
      name: 'Prefer the original channel, allow switching',
    })
  ).toHaveAccessibleDescription('Reduces cache hit rate')
  expect(
    screen.getByRole('radio', { name: 'Require the original channel' })
  ).toBeVisible()
  expect(
    screen.getByRole('spinbutton', {
      name: 'Maximum retries',
    })
  ).toBeVisible()
  expect(
    screen.queryByRole('switch', { name: 'Switch affinity on success' })
  ).not.toBeInTheDocument()
  await userEvent.click(
    screen.getByRole('button', { name: 'Affinity cache settings' })
  )
  expect(
    screen.getByRole('spinbutton', { name: 'Maximum cached sessions' })
  ).toBeVisible()
  expect(
    screen.getByRole('spinbutton', {
      name: 'Default session lifetime (seconds)',
    })
  ).toBeVisible()
  expect(
    screen.getByRole('switch', {
      name: 'Update the session binding after a successful switch',
    })
  ).toBeVisible()
  expect(
    screen.getByRole('switch', {
      name: 'Keep the session binding when the channel is unavailable',
    })
  ).toBeVisible()
  expect(screen.getAllByRole('button', { name: 'Save Changes' })).toHaveLength(
    1
  )
  expect(
    screen.queryByRole('button', { name: 'Manage all session rules' })
  ).not.toBeInTheDocument()
})
it('session rules sit between the session defaults and the retry budget', async () => {
  show()
  const table = await screen.findByRole('table')
  const defaults = screen.getByRole('switch', {
    name: 'Enable session affinity',
  })
  const retries = screen.getByRole('spinbutton', { name: 'Maximum retries' })
  const follows = (first: Element, second: Element) =>
    Boolean(
      first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING
    )
  expect(follows(defaults, table)).toBe(true)
  expect(follows(table, retries)).toBe(true)
  expect(
    screen.queryByRole('complementary', { name: 'Policy preview' })
  ).not.toBeInTheDocument()
})
it('editing a rule and retries saves one draft without dropping transforms or extension fields', async () => {
  show()
  const retries = await screen.findByRole('spinbutton', {
    name: 'Maximum retries',
  })
  fireEvent.change(retries, { target: { value: '5' } })
  await userEvent.click(screen.getByRole('button', { name: 'Edit Rule' }))
  const dialog = screen.getByRole('dialog')
  const behavior = within(dialog).getByRole('combobox', {
    name: 'Session behavior',
  })
  expect(behavior).toHaveValue('prefer')
  await userEvent.selectOptions(behavior, 'strict')
  fireEvent.change(within(dialog).getByRole('textbox', { name: /^Name/ }), {
    target: { value: 'Renamed session' },
  })
  await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
  expect(api.patch).not.toHaveBeenCalled()
  expect(screen.getByRole('table')).toHaveTextContent('Renamed session')
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalledTimes(1))
  const payload = vi.mocked(api.patch).mock.calls[0][1] as {
    options: Record<string, string>
  }
  expect(payload.options.RetryTimes).toBe('5')
  expect(Object.keys(payload.options)).toEqual([
    'RetryTimes',
    'channel_affinity_setting.rules',
  ])
  expect(
    JSON.parse(payload.options['channel_affinity_setting.rules'])[0]
  ).toMatchObject({
    name: 'Renamed session',
    session_mode: 'strict',
    skip_retry_on_failure: false,
    include_using_group: false,
    param_override_template: { temperature: 0 },
    future_field: { retained: true },
  })
})

it('keeps binding options available for rule overrides regardless of the global default', async () => {
  show()
  const children = await screen.findByRole('group', {
    name: 'Channel switching options',
  })
  const update = within(children).getByRole('switch', {
    name: 'Update the session binding after a successful switch',
  })
  const keep = screen.getByRole('switch', {
    name: 'Keep the session binding when the channel is unavailable',
  })
  expect(children).not.toContainElement(keep)
  await userEvent.click(update)
  await userEvent.click(
    screen.getByRole('radio', { name: 'Require the original channel' })
  )
  expect(children).toBeVisible()
  expect(keep).toBeVisible()
  expect(keep).not.toHaveAttribute('aria-disabled', 'true')
  await userEvent.click(keep)
  await userEvent.click(
    screen.getByRole('radio', { name: 'Do not keep sessions' })
  )
  expect(keep).toBeVisible()
  expect(keep).not.toHaveAttribute('aria-disabled', 'true')
  expect(update).not.toHaveAttribute('aria-disabled', 'true')
  await userEvent.click(
    screen.getByRole('radio', {
      name: 'Prefer the original channel, allow switching',
    })
  )
  expect(children).toBeVisible()
  expect(update).toBeChecked()
  expect(keep).toBeChecked()
  await userEvent.click(
    screen.getByRole('switch', {
      name: 'Enable session affinity',
    })
  )
  expect(update).toHaveAttribute('aria-disabled', 'true')
  expect(keep).toHaveAttribute('aria-disabled', 'true')
  await userEvent.click(
    screen.getByRole('switch', {
      name: 'Enable session affinity',
    })
  )
  expect(api.patch).not.toHaveBeenCalled()
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalledTimes(1))
  expect(vi.mocked(api.patch).mock.calls[0][1]).toEqual({
    options: {
      'channel_affinity_setting.session_mode': 'prefer',
      'channel_affinity_setting.switch_on_success': 'true',
      'channel_affinity_setting.keep_on_channel_disabled': 'true',
    },
  })
})

it('a failed save retains the edited retries and rules for another save', async () => {
  vi.mocked(api.patch).mockResolvedValue({
    data: { success: false, message: 'Save rejected' },
  })
  show()
  const retries = await screen.findByRole('spinbutton', {
    name: 'Maximum retries',
  })
  fireEvent.change(retries, { target: { value: '5' } })
  await userEvent.click(screen.getByRole('button', { name: 'Edit Rule' }))
  const dialog = screen.getByRole('dialog')
  fireEvent.change(within(dialog).getByRole('textbox', { name: /^Name/ }), {
    target: { value: 'Unsaved rule' },
  })
  await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(await screen.findByText('Save rejected')).toBeVisible()
  expect(retries).toHaveValue(5)
  expect(screen.getByRole('table')).toHaveTextContent('Unsaved rule')
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalledTimes(2))
})

it.each([
  ['off', 'Do not keep sessions'],
  ['prefer', 'Prefer the original channel, allow switching'],
  ['strict', 'Require the original channel'],
])(
  'global default %s updates only inheriting rules without rewriting rule JSON',
  async (mode, label) => {
    const original = JSON.parse(
      currentOptions['channel_affinity_setting.rules']
    )[0]
    const rulesJson = JSON.stringify([
      { ...original, name: 'Inherited session', session_mode: 'inherit' },
      { ...original, name: 'Strict session', session_mode: 'strict' },
      { ...original, name: 'Prefer session', session_mode: 'prefer' },
      { ...original, name: 'Off session', session_mode: 'off' },
      {
        ...original,
        name: 'Legacy session',
        session_mode: undefined,
        skip_retry_on_failure: true,
      },
    ])
    currentOptions['channel_affinity_setting.rules'] = rulesJson
    show()
    await userEvent.click(await screen.findByRole('radio', { name: label }))
    const inherited = screen.getByRole('row', {
      name: /Inherited session.*X-Session/,
    })
    expect(inherited).toHaveTextContent(label)
    expect(inherited).toHaveTextContent('Inherit global default')
    expect(
      screen.getByRole('row', { name: /Strict session.*X-Session/ })
    ).toHaveTextContent('Require the original channel')
    expect(
      screen.getByRole('row', { name: /Prefer session.*X-Session/ })
    ).toHaveTextContent('Prefer the original channel, allow switching')
    expect(
      screen.getByRole('row', { name: /Off session.*X-Session/ })
    ).toHaveTextContent('Do not keep sessions')
    expect(
      screen.getByRole('row', { name: /Legacy session.*X-Session/ })
    ).toHaveTextContent('Require the original channel')
    expect(
      within(screen.getByRole('table')).getAllByText('Rule override')
    ).toHaveLength(4)
    expect(
      screen.queryByRole('radio', { name: 'Use per-rule settings' })
    ).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() => expect(api.patch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.patch).mock.calls[0][1]).toEqual({
      options: { 'channel_affinity_setting.session_mode': mode },
    })
    expect(currentOptions['channel_affinity_setting.rules']).toBe(rulesJson)
    expect(screen.getByRole('radio', { name: label })).toBeChecked()
    await userEvent.click(
      within(inherited).getByRole('button', { name: 'Edit Rule' })
    )
    const dialog = screen.getByRole('dialog')
    expect(
      within(dialog).getByRole('combobox', { name: 'Session behavior' })
    ).toHaveValue('inherit')
    expect(dialog).toHaveTextContent(`Global default: ${label}`)
  }
)

it('switching between visual and JSON editing preserves the shared draft', async () => {
  show()
  fireEvent.change(
    await screen.findByRole('spinbutton', {
      name: 'Maximum retries',
    }),
    { target: { value: '7' } }
  )
  await userEvent.click(screen.getByRole('tab', { name: 'JSON' }))
  const editor = await screen.findByRole('textbox', { name: 'Rules JSON' })
  const updated = JSON.parse(options['channel_affinity_setting.rules'])
  updated[0].name = 'JSON rule'
  fireEvent.input(editor, { target: { value: JSON.stringify(updated) } })
  await userEvent.click(screen.getByRole('tab', { name: 'Visual' }))
  expect(screen.getByRole('table')).toHaveTextContent('JSON rule')
  expect(
    screen.getByRole('spinbutton', {
      name: 'Maximum retries',
    })
  ).toHaveValue(7)
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalledTimes(1))
  expect(currentOptions.RetryTimes).toBe('7')
  expect(
    JSON.parse(currentOptions['channel_affinity_setting.rules'])[0]
  ).toMatchObject({ name: 'JSON rule', future_field: { retained: true } })
})

it('editing a rule without a session mode saves the chosen override with the retries', async () => {
  const legacyRules = JSON.parse(options['channel_affinity_setting.rules'])
  delete legacyRules[0].session_mode
  currentOptions = {
    ...options,
    'channel_affinity_setting.rules': JSON.stringify(legacyRules),
  }
  show()
  await screen.findByRole('table')
  fireEvent.change(
    await screen.findByRole('spinbutton', {
      name: 'Maximum retries',
    }),
    { target: { value: '4' } }
  )
  await userEvent.click(screen.getByRole('button', { name: 'Edit Rule' }))
  const dialog = screen.getByRole('dialog')
  const behavior = within(dialog).getByRole('combobox', {
    name: 'Session behavior',
  })
  expect(behavior).toHaveValue('prefer')
  await userEvent.selectOptions(behavior, 'strict')
  fireEvent.change(within(dialog).getByRole('textbox', { name: /^Name/ }), {
    target: { value: 'Renamed rule' },
  })
  await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalledTimes(1))
  expect(currentOptions.RetryTimes).toBe('4')
  const savedRule = JSON.parse(
    currentOptions['channel_affinity_setting.rules']
  )[0]
  expect(savedRule).toHaveProperty('session_mode', 'strict')
  expect(savedRule).toMatchObject({
    name: 'Renamed rule',
    skip_retry_on_failure: false,
    include_using_group: false,
    param_override_template: { temperature: 0 },
    future_field: { retained: true },
  })
})

it('switches a rule between inheritance and override while preserving other rules', async () => {
  const rules = JSON.parse(options['channel_affinity_setting.rules'])
  const second = {
    ...rules[0],
    name: 'Strict session',
    session_mode: 'strict',
    skip_retry_on_failure: true,
  }
  rules.push(second)
  currentOptions['channel_affinity_setting.rules'] = JSON.stringify(rules)
  currentOptions['channel_affinity_setting.session_mode'] = 'prefer'
  show()
  await screen.findByRole('table')
  await userEvent.click(
    within(
      screen.getByRole('row', { name: /Session rule.*X-Session/ })
    ).getByRole('button', { name: 'Edit Rule' })
  )
  let dialog = screen.getByRole('dialog')
  await userEvent.selectOptions(
    within(dialog).getByRole('combobox', { name: 'Session behavior' }),
    'inherit'
  )
  await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
  expect(
    screen.getByRole('row', { name: /Session rule.*X-Session/ })
  ).toHaveTextContent('Inherit global default')
  await userEvent.click(
    screen.getByRole('radio', { name: 'Require the original channel' })
  )
  expect(
    screen.getByRole('row', { name: /Session rule.*X-Session/ })
  ).toHaveTextContent('Require the original channel')
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalledTimes(1))
  expect(
    JSON.parse(currentOptions['channel_affinity_setting.rules'])[0].session_mode
  ).toBe('inherit')
  await userEvent.click(
    within(
      screen.getByRole('row', { name: /Session rule.*X-Session/ })
    ).getByRole('button', { name: 'Edit Rule' })
  )
  dialog = screen.getByRole('dialog')
  expect(
    within(dialog).getByRole('combobox', { name: 'Session behavior' })
  ).toHaveValue('inherit')
  await userEvent.selectOptions(
    within(dialog).getByRole('combobox', { name: 'Session behavior' }),
    'off'
  )
  await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
  expect(
    screen.getByRole('row', { name: /Session rule.*X-Session/ })
  ).toHaveTextContent('Do not keep sessions')
  expect(
    screen.getByRole('row', { name: /Strict session.*X-Session/ })
  ).toHaveTextContent('Require the original channel')
  expect(
    screen.getByRole('radio', { name: 'Require the original channel' })
  ).toBeChecked()
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalledTimes(2))
  expect(currentOptions['channel_affinity_setting.session_mode']).toBe('strict')
  const savedRules = JSON.parse(
    currentOptions['channel_affinity_setting.rules']
  )
  expect(savedRules[0]).toMatchObject({
    session_mode: 'off',
    param_override_template: { temperature: 0 },
    future_field: { retained: true },
  })
  expect(savedRules[1]).toEqual(second)
})

it('a new blank rule inherits the global default', async () => {
  show()
  await userEvent.click(await screen.findByRole('button', { name: 'Add Rule' }))
  await userEvent.click(screen.getByRole('menuitem', { name: 'Blank Rule' }))
  expect(
    within(screen.getByRole('dialog')).getByRole('combobox', {
      name: 'Session behavior',
    })
  ).toHaveValue('inherit')
})

it('refreshing settings preserves unsaved rule and retry edits', async () => {
  show()
  fireEvent.change(
    await screen.findByRole('spinbutton', {
      name: 'Maximum retries',
    }),
    { target: { value: '7' } }
  )
  await userEvent.click(screen.getByRole('button', { name: 'More actions' }))
  await userEvent.click(screen.getByRole('menuitem', { name: 'Delete Rule' }))
  await userEvent.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Delete Rule',
    })
  )
  currentOptions = {
    ...currentOptions,
    'channel_affinity_setting.default_ttl_seconds': '120',
  }
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['request-policy'] })
  })
  expect(
    screen.getByRole('spinbutton', {
      name: 'Maximum retries',
    })
  ).toHaveValue(7)
  await userEvent.click(
    screen.getByRole('button', { name: 'Affinity cache settings' })
  )
  await waitFor(() =>
    expect(
      screen.getByRole('spinbutton', {
        name: 'Default session lifetime (seconds)',
      })
    ).toHaveValue(120)
  )
  expect(screen.getByText('No rules yet')).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalledTimes(1))
  expect(vi.mocked(api.patch).mock.calls[0][1]).toEqual({
    options: { RetryTimes: '7', 'channel_affinity_setting.rules': '[]' },
  })
})

it('disabling global affinity preserves every rule and saves zero retries as zero', async () => {
  show()
  fireEvent.change(
    await screen.findByRole('spinbutton', {
      name: 'Maximum retries',
    }),
    { target: { value: '0' } }
  )
  await userEvent.click(
    screen.getByRole('switch', {
      name: 'Enable session affinity',
    })
  )
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.patch).toHaveBeenCalledTimes(1))
  expect(vi.mocked(api.patch).mock.calls[0][1]).toEqual({
    options: { RetryTimes: '0', 'channel_affinity_setting.enabled': 'false' },
  })
  expect(screen.getByRole('table')).toHaveTextContent('Session rule')
  expect(screen.getByRole('table')).toHaveTextContent('Disabled globally')
  expect(screen.getByRole('table')).toHaveTextContent('Do not keep sessions')
})

it('invalid JSON stays editable and prevents saving', async () => {
  show()
  await userEvent.click(await screen.findByRole('tab', { name: 'JSON' }))
  const editor = await screen.findByRole('textbox', { name: 'Rules JSON' })
  fireEvent.input(editor, { target: { value: '[' } })
  expect(screen.getByRole('button', { name: 'Add Rule' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Fill Templates' })).toBeDisabled()
  await userEvent.click(screen.getByRole('tab', { name: 'Visual' }))
  expect(screen.getByRole('tab', { name: 'JSON' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(editor).toHaveValue('[')
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(await screen.findByText('Invalid rules JSON format')).toBeVisible()
  expect(editor).toHaveValue('[')
  expect(api.patch).not.toHaveBeenCalled()
})

it('cache cleanup uses the existing cache API without saving the draft', async () => {
  vi.spyOn(api, 'delete').mockResolvedValue({ data: { success: true } })
  show()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Clear All Cache' })
  )
  await userEvent.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Continue',
    })
  )
  await waitFor(() =>
    expect(api.delete).toHaveBeenCalledWith(
      '/api/option/channel_affinity_cache',
      { params: { all: true } }
    )
  )
  expect(api.patch).not.toHaveBeenCalled()
})

it('keyboard navigation switches editor tabs and restores the visual draft', async () => {
  const user = userEvent.setup()
  show()
  const visual = await screen.findByRole('tab', { name: 'Visual' })
  visual.focus()
  await user.keyboard('{ArrowRight}{Enter}')
  expect(screen.getByRole('tab', { name: 'JSON' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(
    await screen.findByRole('textbox', { name: 'Rules JSON' })
  ).toBeVisible()
  await user.keyboard('{ArrowLeft}{Enter}')
  expect(visual).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByRole('table')).toHaveTextContent('Session rule')
  expect(screen.getByRole('button', { name: 'Add Rule' })).toBeEnabled()
  expect(api.patch).not.toHaveBeenCalled()
})

it('cancelling deletion from the rule menu leaves the rule in the draft', async () => {
  show()
  await userEvent.click(
    await screen.findByRole('button', { name: 'More actions' })
  )
  await userEvent.click(screen.getByRole('menuitem', { name: 'Delete Rule' }))
  const dialog = screen.getByRole('alertdialog')
  expect(dialog).toHaveTextContent('Session rule')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(screen.getByRole('table')).toHaveTextContent('Session rule')
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(api.patch).not.toHaveBeenCalled()
})

it('the empty state can fill templates without saving them to the server', async () => {
  currentOptions['channel_affinity_setting.rules'] = '[]'
  show()
  const panel = await screen.findByRole('tabpanel', { name: 'Visual' })
  expect(within(panel).getByText('No rules yet')).toBeVisible()
  await userEvent.click(
    within(panel).getByRole('button', { name: 'Fill Templates' })
  )
  expect(screen.getByRole('table')).toHaveTextContent('codex cli trace')
  expect(screen.getByRole('table')).toHaveTextContent('claude cli trace')
  expect(screen.queryByText('No rules yet')).not.toBeInTheDocument()
  expect(api.patch).not.toHaveBeenCalled()
})

it('long rule fields stay in a keyboard-scrollable table and reveal the full name on focus', async () => {
  const rule = JSON.parse(options['channel_affinity_setting.rules'])[0]
  const name =
    'Production Codex CLI session rule for shared multi-region inference channels'
  currentOptions['channel_affinity_setting.rules'] = JSON.stringify([
    {
      ...rule,
      name,
      key_sources: [
        {
          type: 'gjson',
          path: 'metadata.session.conversation.trace.identifiers.primary_cache_key',
        },
      ],
    },
  ])
  show()
  const region = await screen.findByRole('region', {
    name: 'Session rules table',
  })
  expect(region).toHaveAttribute('tabindex', '0')
  expect(region).toHaveClass('overflow-x-auto')
  expect(within(region).getByRole('table')).toHaveClass('table-fixed')
  const nameCell = within(region).getByText(name)
  expect(nameCell).toHaveClass('truncate')
  act(() => region.focus())
  await userEvent.tab()
  expect(nameCell.closest('[tabindex]')).toHaveFocus()
  await waitFor(() => expect(screen.getAllByText(name)).toHaveLength(2))
  expect(
    within(region).getByRole('button', { name: 'More actions' })
  ).toBeEnabled()
})

it('unavailable cache statistics show an unknown count and refresh recovers the summary', async () => {
  const get = vi.mocked(api.get).getMockImplementation()
  if (!get) throw new Error('API fixture is not initialized')
  let cacheAvailable = false
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/option/channel_affinity_cache' && !cacheAvailable) {
      return { data: { success: false, message: 'Cache service unavailable' } }
    }
    return get(url, config)
  })
  show()
  const section = await screen.findByRole('region', {
    name: 'Session rules',
  })
  const status = within(section).getByRole('status')
  await waitFor(() => expect(status).toHaveTextContent('Unavailable'))
  const row = within(section).getByRole('row', {
    name: /Session rule.*X-Session/,
  })
  expect(within(row).getByRole('cell', { name: '—' })).toBeVisible()
  cacheAvailable = true
  await userEvent.click(
    within(section).getByRole('button', { name: 'Refresh Cache' })
  )
  await waitFor(() => expect(status).toHaveTextContent(/2\s*\/\s*100,000/))
  expect(within(row).getByRole('cell', { name: '2' })).toBeVisible()
  expect(api.patch).not.toHaveBeenCalled()
})

it.each([
  ['zhCN', /100,000/, '3,600 seconds'],
  ['zhTW', /100,000/, '3,600 seconds'],
  ['en', /100,000/, '3,600 seconds'],
  ['ja', /100,000/, '3,600 seconds'],
  ['fr', /100\s000/, /3\s600 seconds/],
  ['ru', /100\s000/, /3\s600 seconds/],
  ['vi', /100\.000/, '3.600 seconds'],
  ['invalid_locale', /100,000/, '3,600 seconds'],
] as const)(
  'renders cache statistics and TTL with the project language code %s and updates formatting after a language switch',
  async (language, capacity, ttl) => {
    const rules = JSON.parse(options['channel_affinity_setting.rules'])
    rules[0].ttl_seconds = 3600
    currentOptions['channel_affinity_setting.rules'] = JSON.stringify(rules)
    await i18next.changeLanguage(language)

    show()

    const section = await screen.findByRole('region', { name: 'Session rules' })
    const status = within(section).getByRole('status')
    await waitFor(() => expect(status).toHaveTextContent(capacity))
    const table = within(section).getByRole('table')
    expect(within(table).getByRole('cell', { name: ttl })).toBeVisible()
    expect(within(table).getByRole('cell', { name: '2' })).toBeVisible()

    await act(async () => {
      await i18next.changeLanguage('fr')
    })

    await waitFor(() => expect(status).toHaveTextContent(/2\s*\/\s*100\s000/))
    expect(
      within(table).getByRole('cell', { name: /3\s600 seconds/ })
    ).toBeVisible()
    expect(api.patch).not.toHaveBeenCalled()
  }
)
