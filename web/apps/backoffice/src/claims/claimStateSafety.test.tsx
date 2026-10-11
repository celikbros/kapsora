import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode } from 'react';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';

import { App } from '../App';
import { createServices } from '../api';
import { resetAccessMemory } from '../health/access';

const { api, server } = createMockServer();

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  vi.restoreAllMocks();
  api.reset();
  resetAccessMemory();
});
afterAll(() => server.close());

function claims() {
  const b = api.world.claims.find((row) => row.status === 'PENDING_FINANCIAL')!;
  const version = api.world.claimVersions.find((row) => row.claimId === b.id)!;
  const original = api.world.claimLines.find((row) => row.versionId === version.id)!;
  original.lineNo = 3;
  api.world.claimLines.push({ ...original, id: api.world.nextId(), lineNo: 9 });
  const a = { ...b, id: api.world.nextId(), reference: 'CLM-REVIEW-A' };
  const aVersion = { ...version, id: api.world.nextId(), claimId: a.id };
  api.world.claims.push(a);
  api.world.claimVersions.push(aVersion);
  api.world.claimLines.push(
    { ...original, id: api.world.nextId(), versionId: aVersion.id },
    { ...original, id: api.world.nextId(), versionId: aVersion.id, lineNo: 7 },
  );
  return { a, b };
}

async function mount(claimId: string, strict = false, username = 'financial.reviewer') {
  const services = createServices({ baseUrl: 'http://mock.test' });
  const history = createMemoryHistory({ initialEntries: [`/claims/${claimId}`] });
  const app = <App services={services} history={history} />;
  render(strict ? <StrictMode>{app}</StrictMode> : app);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return { services, history, user };
}

async function onClaim(reference: string) {
  await waitFor(() => expect(document.querySelector('main')?.textContent).toContain(reference));
}

function financialClaim() {
  return api.world.claims.find((row) => row.status === 'PENDING_FINANCIAL')!;
}

async function openReason(user: ReturnType<typeof userEvent.setup>, action: 'Reddet' | 'İade et') {
  await user.click(screen.getByRole('button', { name: action }));
  return screen.getByRole('dialog');
}

it('keeps cached same-version claim decisions on their own claim and line numbers', async () => {
  const { a, b } = claims();
  const { services, history, user } = await mount(b.id);
  await onClaim(b.reference);
  await act(async () => history.push(`/claims/${a.id}`));
  await onClaim(a.reference);
  await user.type(screen.getByRole('textbox', { name: /Gerekçe kodu 3/ }), 'A_ONLY');
  await user.type(screen.getByRole('textbox', { name: /Onaylanan tutar 3/ }), '123');
  await act(async () => history.push(`/claims/${b.id}`));
  await onClaim(b.reference);
  expect(screen.getByRole('textbox', { name: /Gerekçe kodu 3/ })).toHaveValue('');
  expect(screen.getByRole('textbox', { name: /Onaylanan tutar 3/ })).toHaveValue('450');
  expect(screen.getByRole('textbox', { name: /Gerekçe kodu 9/ })).toHaveValue('');
  const decide = vi.spyOn(services.ops.claims, 'decideLines');
  await user.type(screen.getByRole('textbox', { name: /Gerekçe kodu 3/ }), 'B_ONLY');
  await user.type(screen.getByRole('textbox', { name: /Gerekçe kodu 9/ }), 'B_NINE');
  await user.click(screen.getByTestId('save-decisions'));
  await waitFor(() => expect(decide).toHaveBeenCalledTimes(1));
  expect(decide.mock.calls[0]![1]).toBe(b.id);
  expect(decide.mock.calls[0]![3].decisions.map((line) => [line.lineNo, line.reasonCode])).toEqual([
    [3, 'B_ONLY'],
    [9, 'B_NINE'],
  ]);
  expect(await screen.findByText('Satır kararları kaydedildi.')).toBeInTheDocument();
});

it('closes a command on a changed version, status, or failed read and requires reopening', async () => {
  const claim = financialClaim();
  const { services, user } = await mount(claim.id);
  await onClaim(claim.reference);
  let dialog = await openReason(user, 'Reddet');
  await user.type(within(dialog).getByRole('textbox', { name: /Gerekçe kodu/ }), 'OLD');
  await act(async () => services.queryClient.invalidateQueries({ queryKey: ['claims'] }));
  expect(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /Gerekçe kodu/ }),
  ).toHaveValue('OLD');
  claim.rowVersion += 1;
  await act(async () => services.queryClient.invalidateQueries({ queryKey: ['claims'] }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  claim.rowVersion -= 1;
  await act(async () => services.queryClient.invalidateQueries({ queryKey: ['claims'] }));
  expect(screen.queryByRole('dialog')).toBeNull();
  dialog = await openReason(user, 'Reddet');
  expect(within(dialog).getByRole('textbox', { name: /Gerekçe kodu/ })).toHaveValue('');
  claim.status = 'APPROVED';
  await act(async () => services.queryClient.invalidateQueries({ queryKey: ['claims'] }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  claim.status = 'PENDING_FINANCIAL';
  await act(async () => services.queryClient.invalidateQueries({ queryKey: ['claims'] }));
  expect(screen.queryByRole('dialog')).toBeNull();
  await openReason(user, 'Reddet');
  const originalGet = services.ops.claims.get.bind(services.ops.claims);
  let fail = true;
  vi.spyOn(services.ops.claims, 'get').mockImplementation((...args) =>
    fail ? Promise.reject(new Error('temporary read failure')) : originalGet(...args),
  );
  await act(async () => services.queryClient.invalidateQueries({ queryKey: ['claims'] }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  fail = false;
  await user.click(await screen.findByRole('button', { name: 'Yeniden dene' }));
  await onClaim(claim.reference);
  expect(screen.queryByRole('dialog')).toBeNull();
});

it('keeps another claim dialog open when an earlier rejection settles', async () => {
  const { a, b } = claims();
  const { services, history, user } = await mount(a.id);
  const originalReject = services.ops.claims.reject.bind(services.ops.claims);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const reject = vi.spyOn(services.ops.claims, 'reject').mockImplementation(async (...args) => {
    if (args[1] === a.id) await held;
    return originalReject(...args);
  });
  await onClaim(a.reference);
  let dialog = await openReason(user, 'Reddet');
  await user.type(within(dialog).getByRole('textbox', { name: /Gerekçe kodu/ }), 'A_ONLY');
  await user.click(within(dialog).getByRole('button', { name: 'Reddet' }));
  await waitFor(() => expect(reject).toHaveBeenCalledTimes(1));
  await act(async () => history.push(`/claims/${b.id}`));
  await onClaim(b.reference);
  expect(screen.queryByRole('dialog')).toBeNull();
  dialog = await openReason(user, 'Reddet');
  await user.type(within(dialog).getByRole('textbox', { name: /Gerekçe kodu/ }), 'B_ONLY');
  await act(async () => release());
  await waitFor(() => expect(a.status).toBe('REJECTED'));
  expect(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /Gerekçe kodu/ }),
  ).toHaveValue('B_ONLY');
  expect(screen.queryByText('Claim reddedildi.')).toBeNull();
});

it('does not announce a late line save after navigating to another claim', async () => {
  const { a, b } = claims();
  const { services, history, user } = await mount(a.id);
  const originalDecide = services.ops.claims.decideLines.bind(services.ops.claims);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const decide = vi
    .spyOn(services.ops.claims, 'decideLines')
    .mockImplementation(async (...args) => {
      if (args[1] === a.id) await held;
      return originalDecide(...args);
    });
  await onClaim(a.reference);
  await user.type(screen.getByRole('textbox', { name: /Gerekçe kodu 3/ }), 'A_THREE');
  await user.type(screen.getByRole('textbox', { name: /Gerekçe kodu 7/ }), 'A_SEVEN');
  await user.click(screen.getByTestId('save-decisions'));
  await waitFor(() => expect(decide).toHaveBeenCalledTimes(1));
  await act(async () => history.push(`/claims/${b.id}`));
  await onClaim(b.reference);
  await act(async () => release());
  await waitFor(() =>
    expect(
      services.queryClient
        .getMutationCache()
        .getAll()
        .every((mutation) => mutation.state.status !== 'pending'),
    ).toBe(true),
  );
  expect(screen.queryByText('Satır kararları kaydedildi.')).toBeNull();
});

it('approves after deciding the current claim and shows feedback', async () => {
  const claim = financialClaim();
  const { services, user } = await mount(claim.id);
  const approve = vi.spyOn(services.ops.claims, 'approve');
  await onClaim(claim.reference);
  await user.type(screen.getByRole('textbox', { name: /Gerekçe kodu 1/ }), 'COVERED');
  await user.click(screen.getByTestId('save-decisions'));
  expect(await screen.findByText('Satır kararları kaydedildi.')).toBeInTheDocument();
  await user.click(await screen.findByRole('button', { name: 'Onayla' }));
  const dialog = screen.getByRole('dialog');
  await user.type(within(dialog).getByRole('textbox', { name: /Gerekçe kodu/ }), 'APPROVE_OK');
  await user.click(within(dialog).getByRole('button', { name: 'Onayla' }));
  await waitFor(() => expect(approve).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(claim.status).toBe('APPROVED'));
  expect(await screen.findByText('Claim onaylandı.')).toBeInTheDocument();
});

it.each([
  ['Reddet', 'Claim reddedildi.', 'REJECTED'],
  ['İade et', 'Claim iade edildi; düzeltme sürümü açıldı.', 'RETURNED'],
] as const)('shows current-claim feedback for %s', async (action, feedback, status) => {
  const claim = financialClaim();
  const { user } = await mount(claim.id, true);
  await onClaim(claim.reference);
  const dialog = await openReason(user, action);
  await user.type(within(dialog).getByRole('textbox', { name: /Gerekçe kodu/ }), 'REVIEW_REASON');
  await user.click(within(dialog).getByRole('button', { name: action }));
  await waitFor(() => expect(claim.status).toBe(status));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(await screen.findByText(feedback)).toBeInTheDocument();
});
