// Pure-function tests for the privacy toggle round-trip in PolicyForm.
import test from 'node:test';
import assert from 'node:assert/strict';
import { policyToForm, formToPolicy } from './PolicyForm.jsx';

test('policyToForm: defaults store_request_bodies to false', () => {
  const form = policyToForm(undefined);
  assert.equal(form.store_request_bodies, false);
});

test('policyToForm: reads an explicit true', () => {
  const form = policyToForm({ store_request_bodies: true });
  assert.equal(form.store_request_bodies, true);
});

test('formToPolicy: emits store_request_bodies as a boolean', () => {
  const form = policyToForm({});
  form.store_request_bodies = true;
  const policy = formToPolicy(form, 'key-1');
  assert.equal(policy.store_request_bodies, true);
  assert.equal(policy.api_key_id, 'key-1');
});

test('formToPolicy: coerces falsy to false', () => {
  const form = policyToForm({});
  delete form.store_request_bodies;
  const policy = formToPolicy(form, 'key-1');
  assert.equal(policy.store_request_bodies, false);
});
