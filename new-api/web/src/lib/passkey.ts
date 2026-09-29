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
/**
 * Passkey helper utilities for WebAuthn credential handling.
 *
 * These helpers convert between ArrayBuffer and Base64URL encodings and
 * normalise server-provided credential options into browser-compatible types.
 */

/* eslint-disable @typescript-eslint/no-explicit-any */

/**
 * Convert a base64url string to an ArrayBuffer.
 */
type NodeBufferCtor = {
  from(input: string, encoding: string): { toString(encoding: string): string }
}

export function base64UrlToArrayBuffer(value?: string | null): ArrayBuffer {
  if (!value) return new ArrayBuffer(0)

  const padding = '='.repeat((4 - (value.length % 4)) % 4)
  const base64 = (value + padding).replaceAll('-', '+').replaceAll('_', '/')

  const globalRef = globalThis as typeof globalThis & {
    Buffer?: NodeBufferCtor
  }

  const decode =
    typeof globalRef.atob === 'function'
      ? globalRef.atob.bind(globalRef)
      : (input: string) => {
          if (typeof globalRef.Buffer !== 'undefined') {
            return globalRef.Buffer.from(input, 'base64').toString('binary')
          }
          throw new Error(
            'Base64 decoding is not supported in this environment'
          )
        }

  const binary = decode(base64)
  const buffer = new ArrayBuffer(binary.length)
  const bytes = new Uint8Array(buffer)

  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i)
  }

  return buffer
}

/**
 * Convert an ArrayBuffer to a base64url string.
 */
export function arrayBufferToBase64Url(
  buffer?: ArrayBuffer | ArrayBufferLike | null
): string {
  if (!buffer) return ''

  const globalRef = globalThis as typeof globalThis & {
    Buffer?: NodeBufferCtor
  }

  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (let i = 0; i < bytes.byteLength; i += 1) {
    binary += String.fromCharCode(bytes[i])
  }

  const encode =
    typeof globalRef.btoa === 'function'
      ? globalRef.btoa.bind(globalRef)
      : (input: string) => {
          if (typeof globalRef.Buffer !== 'undefined') {
            return globalRef.Buffer.from(input, 'binary').toString('base64')
          }
          throw new Error(
            'Base64 encoding is not supported in this environment'
          )
        }

  return encode(binary)
    .replaceAll('+', '-')
    .replaceAll('/', '_')
    .replaceAll(/=+$/g, '')
}

/**
 * Prepare credential creation options returned by the backend.
 */
export function prepareCredentialCreationOptions(
  payload: any
): PublicKeyCredentialCreationOptions {
  const options =
    payload?.publicKey ??
    payload?.PublicKey ??
    payload?.response ??
    payload?.Response

  if (!options) {
    throw new Error(
      'Unable to parse Passkey registration options from response'
    )
  }

  const publicKey: PublicKeyCredentialCreationOptions & Record<string, any> = {
    ...options,
    challenge: base64UrlToArrayBuffer(options.challenge),
    user: {
      ...options.user,
      id: base64UrlToArrayBuffer(options.user?.id),
    },
  }

  if (Array.isArray(options.excludeCredentials)) {
    publicKey.excludeCredentials = options.excludeCredentials.map(
      (item: any) => ({
        ...item,
        id: base64UrlToArrayBuffer(item.id),
      })
    )
  }

  if (
    Array.isArray(options.attestationFormats) &&
    options.attestationFormats.length === 0
  ) {
    delete publicKey.attestationFormats
  }

  return publicKey
}

/**
 * Prepare credential request options returned by the backend.
 */
export function prepareCredentialRequestOptions(
  payload: any
): PublicKeyCredentialRequestOptions {
  const options =
    payload?.publicKey ??
    payload?.PublicKey ??
    payload?.response ??
    payload?.Response

  if (!options) {
    throw new Error('Unable to parse Passkey login options from response')
  }

  const publicKey: PublicKeyCredentialRequestOptions & Record<string, any> = {
    ...options,
    challenge: base64UrlToArrayBuffer(options.challenge),
  }

  if (Array.isArray(options.allowCredentials)) {
    publicKey.allowCredentials = options.allowCredentials.map((item: any) => ({
      ...item,
      id: base64UrlToArrayBuffer(item.id),
    }))
  }

  return publicKey
}

/**
 * Build payload for registering a new credential.
 */
export function buildRegistrationResult(
  credential: PublicKeyCredential | null
): Record<string, any> | null {
  if (!credential) return null

  const response = credential.response as AuthenticatorAttestationResponse & {
    getTransports?: () => string[]
  }

  const transports =
    typeof response.getTransports === 'function'
      ? response.getTransports()
      : undefined

  return {
    id: credential.id,
    rawId: arrayBufferToBase64Url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    response: {
      attestationObject: arrayBufferToBase64Url(response.attestationObject),
      clientDataJSON: arrayBufferToBase64Url(response.clientDataJSON),
      transports,
    },
    clientExtensionResults: credential.getClientExtensionResults?.() ?? {},
  }
}

/**
 * Build payload for verifying an existing credential.
 */
export function buildAssertionResult(
  credential: PublicKeyCredential | null
): Record<string, any> | null {
  if (!credential) return null

  const response = credential.response as AuthenticatorAssertionResponse

  return {
    id: credential.id,
    rawId: arrayBufferToBase64Url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    response: {
      authenticatorData: arrayBufferToBase64Url(response.authenticatorData),
      clientDataJSON: arrayBufferToBase64Url(response.clientDataJSON),
      signature: arrayBufferToBase64Url(response.signature),
      userHandle: response.userHandle
        ? arrayBufferToBase64Url(response.userHandle)
        : null,
    },
    clientExtensionResults: credential.getClientExtensionResults?.() ?? {},
  }
}

/**
 * Check if current environment supports Passkey/WebAuthn.
 */
export async function isPasskeySupported(): Promise<boolean> {
  // A missing platform authenticator does not exclude USB/NFC security keys or
  // cross-device Passkeys. Let the browser select an eligible authenticator.
  return typeof window !== 'undefined' && Boolean(window.PublicKeyCredential)
}

/**
 * Execute an async Passkey credential creation flow.
 */
export async function createCredential(
  options: PublicKeyCredentialCreationOptions,
  signal?: AbortSignal
) {
  return navigator.credentials.create({ publicKey: options, signal })
}

/**
 * Execute an async Passkey credential request flow.
 */
export async function getCredential(
  options: PublicKeyCredentialRequestOptions
) {
  return navigator.credentials.get({ publicKey: options })
}
