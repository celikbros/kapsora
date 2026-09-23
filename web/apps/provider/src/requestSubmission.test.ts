import { expect, it, vi } from 'vitest';
import type {
  CreateServiceRequest,
  Operations,
  ServiceRequest,
  Versioned,
} from '@kapsora/api-client';
import { createRequestSubmission } from './requestSubmission';

const body: CreateServiceRequest = {
  personId: 'person',
  enrollmentId: 'enrollment',
  requestType: 'DIRECT_SERVICE',
  channel: 'PROVIDER_PORTAL',
  serviceDate: '2026-09-22',
  items: [{ serviceDefinitionId: 'physio', unitType: 'SESSION', requestedQuantity: '1' }],
};
const draft: Versioned<ServiceRequest> = {
  etag: '"1"',
  data: {
    id: 'request',
    reference: 'SR-TEST',
    personId: 'person',
    personDisplayName: 'Test Member',
    enrollmentId: 'enrollment',
    programId: 'program',
    requestType: 'DIRECT_SERVICE',
    channel: 'PROVIDER_PORTAL',
    serviceDate: body.serviceDate,
    status: 'DRAFT',
    currentVersionNo: 1,
    rowVersion: 1,
    items: [],
    createdAt: '2026-09-22T10:00:00Z',
  },
};
const submitted: Versioned<ServiceRequest> = {
  ...draft,
  etag: '"2"',
  data: { ...draft.data, status: 'PENDING_REVIEW', rowVersion: 2 },
};
function fixture() {
  const create = vi.fn<Operations['requests']['create']>().mockResolvedValue(draft);
  const submit = vi.fn<Operations['requests']['submit']>().mockResolvedValue(submitted);
  return { create, submit, send: createRequestSubmission({ create, submit }) };
}

it('retries a failed submission against the same draft, ETag and key', async () => {
  const f = fixture();
  f.submit.mockRejectedValueOnce(new Error('connection lost'));
  await expect(f.send('tenant', body)).rejects.toThrow('connection lost');
  await expect(f.send('tenant', structuredClone(body))).resolves.toEqual(submitted);
  expect(f.create).toHaveBeenCalledTimes(1);
  expect(f.submit.mock.calls[1]).toEqual(f.submit.mock.calls[0]);
  expect(f.submit.mock.calls[0]?.slice(0, 3)).toEqual(['tenant', 'request', '"1"']);
});

it('replays an uncertain create with the same key and immutable body', async () => {
  const f = fixture();
  f.create.mockRejectedValueOnce(new Error('response lost'));
  await expect(f.send('tenant', body)).rejects.toThrow('response lost');
  await f.send('tenant', structuredClone(body));
  expect(f.create.mock.calls[1]).toEqual(f.create.mock.calls[0]);
  expect(f.create.mock.calls[0]?.[1]).not.toBe(body);
  expect(f.submit).toHaveBeenCalledTimes(1);
  expect(f.create.mock.calls[0]?.[2]).not.toBe(f.submit.mock.calls[0]?.[3]);
});

it('coalesces concurrent clicks and retains the confirmed result', async () => {
  const f = fixture();
  const first = f.send('tenant', body),
    second = f.send('tenant', body);
  expect(second).toBe(first);
  await Promise.all([first, second]);
  await expect(f.send('tenant', body)).resolves.toEqual(submitted);
  expect(f.create).toHaveBeenCalledTimes(1);
  expect(f.submit).toHaveBeenCalledTimes(1);
});

it('keeps attempts separate by tenant and form content, including returning to earlier input', async () => {
  const f = fixture();
  f.submit.mockRejectedValueOnce(new Error('first uncertain'));
  await expect(f.send('tenant', body)).rejects.toThrow('first uncertain');
  await f.send('tenant', { ...body, serviceDate: '2026-09-23' });
  await f.send('tenant', body);
  await f.send('other-tenant', body);
  expect(f.create).toHaveBeenCalledTimes(3);
  expect(new Set(f.create.mock.calls.map((c) => c[2])).size).toBe(3);
  expect(f.submit.mock.calls[2]).toEqual(f.submit.mock.calls[0]);
});
