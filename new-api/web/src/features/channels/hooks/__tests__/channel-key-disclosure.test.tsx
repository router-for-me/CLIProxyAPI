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
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import { api } from '@/lib/api'

import { useChannelKeyDisclosure } from '../use-channel-key-disclosure'

function Harness(props: { open: boolean; channelId: number }) {
  const disclosure = useChannelKeyDisclosure(props.open, props.channelId)
  return (
    <>
      <button type='button' onClick={disclosure.handleRevealKey}>
        Reveal
      </button>
      <output aria-label='Channel key'>
        {disclosure.channelKey ?? 'Hidden'}
      </output>
      <SecureVerificationDialog {...disclosure.verification.dialogProps} />
    </>
  )
}

function deferredResponse<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((finish) => {
    resolve = finish
  })
  return { promise, resolve }
}

function channelVerification() {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        scope: 'channel.key.read',
        methods: [{ method: '2fa', available: true }],
        oauth_providers: [],
        password_encryption_enabled: false,
      },
    },
  })
  return {
    data: {
      success: true,
      data: {
        proof_token: 'channel-proof',
        method: '2fa',
        scope: 'channel.key.read',
        expires_at: Math.floor(Date.now() / 1000) + 60,
      },
    },
  }
}

afterEach(() => vi.restoreAllMocks())

it.each(['switch', 'close'] as const)(
  'discards a pending channel key response after %s',
  async (change) => {
    const proof = channelVerification()
    const keyReply = deferredResponse<{
      data: { success: boolean; data: { key: string } }
    }>()
    const post = vi.spyOn(api, 'post').mockImplementation((url) => {
      if (url === '/api/verify') return Promise.resolve(proof)
      if (url === '/api/channel/123/key') return keyReply.promise
      throw new Error(`Unexpected POST ${url}`)
    })
    const user = userEvent.setup()
    const view = render(<Harness open channelId={123} />)
    await user.click(screen.getByRole('button', { name: 'Reveal' }))
    await user.type(
      await screen.findByLabelText('Authenticator code or backup code'),
      '123456'
    )
    await user.click(screen.getByRole('button', { name: 'Verify' }))
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        '/api/channel/123/key',
        undefined,
        expect.anything()
      )
    )
    view.rerender(
      <Harness
        open={change !== 'close'}
        channelId={change === 'switch' ? 456 : 123}
      />
    )
    await act(async () => {
      keyReply.resolve({
        data: { success: true, data: { key: 'CHANNEL_A_SECRET' } },
      })
      await keyReply.promise
    })
    expect(screen.getByLabelText('Channel key')).toHaveTextContent('Hidden')
    expect(screen.queryByText('CHANNEL_A_SECRET')).not.toBeInTheDocument()
  }
)

it('cancels a pending verification when the selected channel changes', async () => {
  channelVerification()
  const reply = deferredResponse<{
    data: { success: boolean; data: Record<string, unknown> }
  }>()
  const post = vi.spyOn(api, 'post').mockReturnValue(reply.promise)
  const user = userEvent.setup()
  const view = render(<Harness open channelId={123} />)
  await user.click(screen.getByRole('button', { name: 'Reveal' }))
  await user.type(
    await screen.findByLabelText('Authenticator code or backup code'),
    '123456'
  )
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  view.rerender(<Harness open channelId={456} />)
  await act(async () => {
    reply.resolve({
      data: {
        success: true,
        data: {
          proof_token: 'late-proof',
          scope: 'channel.key.read',
          method: '2fa',
          expires_at: Math.floor(Date.now() / 1000) + 60,
        },
      },
    })
    await reply.promise
  })
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  expect(post.mock.calls.map(([url]) => url)).toEqual(['/api/verify'])
})
