import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';

import { App } from '../App';
import { createServices } from '../services';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  api.reset();
  vi.restoreAllMocks();
});
afterAll(() => server.close());

async function mount(claimId: string) {
  const services = createServices({ baseUrl: 'http://mock.test' });
  const history = createMemoryHistory({ initialEntries: [`/claims/${claimId}`] });
  render(<App services={services} history={history} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'billing.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return { services, history, user };
}

function amount() {
  const input = document.querySelector<HTMLInputElement>('[name="lines.0.lineAmount"]');
  expect(input).not.toBeNull();
  return input!;
}

function cloneDraft() {
  const a = api.world.claims.find((row) => row.status === 'DRAFT')!;
  const version = api.world.claimVersions.find(
    (row) => row.claimId === a.id && row.versionNo === a.currentVersionNo,
  )!;
  const line = api.world.claimLines.find((row) => row.versionId === version.id)!;
  const bId = api.world.nextId();
  const bVersionId = api.world.nextId();
  api.world.claims.push({ ...a, id: bId, reference: 'CL-B-ONLY' });
  api.world.claimVersions.push({ ...version, id: bVersionId, claimId: bId });
  api.world.claimLines.push({
    ...line,
    id: api.world.nextId(),
    versionId: bVersionId,
    lineAmount: '321.00',
  });
  api.world.claimLines.push({
    ...line,
    id: api.world.nextId(),
    versionId: bVersionId,
    lineNo: 2,
    lineAmount: '22.00',
  });
  return { a, bId, line };
}

function claimQueryKey(services: ReturnType<typeof createServices>, claimId: string) {
  const key = services.queryClient
    .getQueryCache()
    .findAll()
    .find(
      (query) =>
        query.queryKey.length === 4 &&
        query.queryKey[2] === 'claim' &&
        query.queryKey[3] === claimId,
    )?.queryKey;
  expect(key).toBeDefined();
  return key!;
}

it('keeps cached same-version claim drafts separate and saves the selected claim lines', async () => {
  const { a, bId, line } = cloneDraft();

  const { services, history, user } = await mount(bId);
  expect(await screen.findByDisplayValue('321.00')).toBeInTheDocument();
  await act(async () => history.push(`/claims/${a.id}`));
  await screen.findByDisplayValue(line.lineAmount);
  await user.clear(amount());
  await user.type(amount(), '999.00');

  await act(async () => history.push(`/claims/${bId}`));
  expect(amount()).toHaveValue('321.00');
  expect(document.querySelectorAll('[name$="lineAmount"]')).toHaveLength(2);
  const put = vi.spyOn(services.ops.claims, 'putLines');
  await user.click(screen.getByRole('button', { name: 'Satırları kaydet' }));
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
  expect(put.mock.calls[0]![1]).toBe(bId);
  expect(put.mock.calls[0]![3].lines[0]?.lineAmount).toBe('321.00');
  expect(await screen.findByText('Satırlar kaydedildi.')).toBeInTheDocument();
});

it('closes and clears cancellation on a claim switch and on a changed version', async () => {
  const { a, bId } = cloneDraft();
  const { services, history, user } = await mount(a.id);
  await screen.findByTestId('claim-lines-editor');
  await user.click(screen.getByRole('button', { name: 'İptal et' }));
  const dialog = screen.getByRole('dialog');
  await user.type(within(dialog).getByLabelText(/Gerekçe kodu/), 'A_REASON');
  await act(async () => history.push(`/claims/${bId}`));
  await screen.findByRole('heading', { name: 'CL-B-ONLY' });
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  await user.click(screen.getByRole('button', { name: 'İptal et' }));
  expect(within(screen.getByRole('dialog')).getByLabelText(/Gerekçe kodu/)).toHaveValue('');
  await user.type(within(screen.getByRole('dialog')).getByLabelText(/Gerekçe kodu/), 'B_REASON');

  const key = claimQueryKey(services, bId);
  await act(async () => {
    services.queryClient.setQueryData(key, (old: { data: { rowVersion: number } }) => ({
      ...old,
      data: { ...old.data, rowVersion: old.data.rowVersion + 1 },
    }));
  });
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  await user.click(screen.getByRole('button', { name: 'İptal et' }));
  expect(within(screen.getByRole('dialog')).getByLabelText(/Gerekçe kodu/)).toHaveValue('');
  await user.type(
    within(screen.getByRole('dialog')).getByLabelText(/Gerekçe kodu/),
    'VERSION_REASON',
  );
  await act(async () => {
    services.queryClient.setQueryData(key, (old: { data: { projection: string } }) => ({
      ...old,
      data: { ...old.data, projection: 'CLINICAL' },
    }));
  });
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  await user.click(screen.getByRole('button', { name: 'İptal et' }));
  expect(within(screen.getByRole('dialog')).getByLabelText(/Gerekçe kodu/)).toHaveValue('');
  await act(async () => {
    services.queryClient.setQueryData(key, (old: { data: { status: string } }) => ({
      ...old,
      data: { ...old.data, status: 'CANCELLED' },
    }));
  });
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(screen.queryByRole('button', { name: 'İptal et' })).toBeNull();
});

it('keeps an unsaved draft through an unchanged same-claim refresh', async () => {
  const a = api.world.claims.find((row) => row.status === 'DRAFT')!;
  const { services, user } = await mount(a.id);
  await screen.findByTestId('claim-lines-editor');
  await user.clear(amount());
  await user.type(amount(), '987.00');
  const key = claimQueryKey(services, a.id);
  await act(async () => {
    services.queryClient.setQueryData(key, (old: { data: object }) => ({
      ...old,
      data: { ...old.data },
    }));
  });
  expect(amount()).toHaveValue('987.00');
});

it('discards an open cancel reason when the claim read fails', async () => {
  const a = api.world.claims.find((row) => row.status === 'DRAFT')!;
  const { services, user } = await mount(a.id);
  await screen.findByTestId('claim-lines-editor');
  await user.click(screen.getByRole('button', { name: 'İptal et' }));
  await user.type(within(screen.getByRole('dialog')).getByLabelText(/Gerekçe kodu/), 'OLD_REASON');
  const query = services.queryClient
    .getQueryCache()
    .find({ queryKey: claimQueryKey(services, a.id) })!;
  await act(async () => query.setState({ error: new Error('read failed'), status: 'error' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  await act(async () => query.setState({ error: null, status: 'success' }));
  await screen.findByTestId('claim-lines-editor');
  await user.click(screen.getByRole('button', { name: 'İptal et' }));
  expect(within(screen.getByRole('dialog')).getByLabelText(/Gerekçe kodu/)).toHaveValue('');
});

it('ignores a late line-save completion after moving to another claim', async () => {
  const { a, bId } = cloneDraft();
  const { services, history, user } = await mount(a.id);
  await screen.findByTestId('claim-lines-editor');
  const original = services.ops.claims.putLines.bind(services.ops.claims);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const put = vi.spyOn(services.ops.claims, 'putLines').mockImplementation(async (...args) => {
    await held;
    return original(...args);
  });
  await user.click(screen.getByRole('button', { name: 'Satırları kaydet' }));
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
  await act(async () => history.push(`/claims/${bId}`));
  await screen.findByRole('heading', { name: 'CL-B-ONLY' });
  await act(async () => release());
  await waitFor(() =>
    expect(
      services.queryClient
        .getMutationCache()
        .getAll()
        .every((mutation) => mutation.state.status !== 'pending'),
    ).toBe(true),
  );
  expect(screen.queryByText('Satırlar kaydedildi.')).toBeNull();
});

it('ignores a late submit completion after moving to another claim', async () => {
  const { a, bId } = cloneDraft();
  const { services, history, user } = await mount(a.id);
  await screen.findByTestId('claim-lines-editor');
  const original = services.ops.claims.submit.bind(services.ops.claims);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const submit = vi.spyOn(services.ops.claims, 'submit').mockImplementation(async (...args) => {
    await held;
    return original(...args);
  });
  await user.click(screen.getByRole('button', { name: 'Gönder' }));
  await waitFor(() => expect(submit).toHaveBeenCalledTimes(1));
  await act(async () => history.push(`/claims/${bId}`));
  await screen.findByRole('heading', { name: 'CL-B-ONLY' });
  await act(async () => release());
  await waitFor(() =>
    expect(
      services.queryClient
        .getMutationCache()
        .getAll()
        .every((mutation) => mutation.state.status !== 'pending'),
    ).toBe(true),
  );
  expect(screen.queryByText('Claim gönderildi.')).toBeNull();
});

it('ignores a late cancel completion after moving to another claim', async () => {
  const { a, bId } = cloneDraft();
  const { services, history, user } = await mount(a.id);
  await screen.findByTestId('claim-lines-editor');
  const original = services.ops.claims.cancel.bind(services.ops.claims);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const cancel = vi.spyOn(services.ops.claims, 'cancel').mockImplementation(async (...args) => {
    await held;
    return original(...args);
  });
  await user.click(screen.getByRole('button', { name: 'İptal et' }));
  await user.type(within(screen.getByRole('dialog')).getByLabelText(/Gerekçe kodu/), 'A_REASON');
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'İptal et' }));
  await waitFor(() => expect(cancel).toHaveBeenCalledTimes(1));
  await act(async () => history.push(`/claims/${bId}`));
  await screen.findByRole('heading', { name: 'CL-B-ONLY' });
  await act(async () => release());
  await waitFor(() =>
    expect(
      services.queryClient
        .getMutationCache()
        .getAll()
        .every((mutation) => mutation.state.status !== 'pending'),
    ).toBe(true),
  );
  expect(screen.queryByRole('dialog')).toBeNull();
  expect(screen.queryByText('Claim iptal edildi.')).toBeNull();
});

it('keeps other commands blocked when a pending cancel dialog is closed', async () => {
  const a = api.world.claims.find((row) => row.status === 'DRAFT')!;
  const { services, user } = await mount(a.id);
  await screen.findByTestId('claim-lines-editor');
  const original = services.ops.claims.cancel.bind(services.ops.claims);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const cancel = vi.spyOn(services.ops.claims, 'cancel').mockImplementation(async (...args) => {
    await held;
    return original(...args);
  });
  const submit = vi.spyOn(services.ops.claims, 'submit');
  const put = vi.spyOn(services.ops.claims, 'putLines');
  await user.click(screen.getByRole('button', { name: 'İptal et' }));
  await user.type(within(screen.getByRole('dialog')).getByLabelText(/Gerekçe kodu/), 'TEST_REASON');
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'İptal et' }));
  await waitFor(() => expect(cancel).toHaveBeenCalledTimes(1));
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Vazgeç' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(screen.getByRole('button', { name: 'Gönder' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Satırları kaydet' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'İptal et' })).toBeDisabled();
  expect(submit).not.toHaveBeenCalled();
  expect(put).not.toHaveBeenCalled();
  await act(async () => release());
  await waitFor(() =>
    expect(
      services.queryClient
        .getMutationCache()
        .getAll()
        .every((mutation) => mutation.state.status !== 'pending'),
    ).toBe(true),
  );
});

it('confirms a successful cancel on the current claim', async () => {
  const a = api.world.claims.find((row) => row.status === 'DRAFT')!;
  const { user } = await mount(a.id);
  await screen.findByTestId('claim-lines-editor');
  await user.click(screen.getByRole('button', { name: 'İptal et' }));
  await user.type(within(screen.getByRole('dialog')).getByLabelText(/Gerekçe kodu/), 'TEST_REASON');
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'İptal et' }));
  expect(await screen.findByText('Claim iptal edildi.')).toBeInTheDocument();
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
});

it('confirms a successful submit on the current claim', async () => {
  const a = api.world.claims.find((row) => row.status === 'DRAFT')!;
  const { user } = await mount(a.id);
  await screen.findByTestId('claim-lines-editor');
  await user.click(screen.getByRole('button', { name: 'Gönder' }));
  expect(await screen.findByText('Claim gönderildi.')).toBeInTheDocument();
});
