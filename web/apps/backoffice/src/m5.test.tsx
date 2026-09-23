import { createMockServer } from '@kapsora/api-client/mocks/node';
import { SessionProvider } from '@kapsora/auth';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, render, renderHook, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { App } from './App';
import { createServices } from './api';
import { accessFor, resetAccessMemory, useAccessState } from './health/access';

/**
 * The M5 review screens against the mock world. The milestone is judged by what the
 * sponsor's HR user cannot see, so the first test here is a scan of the rendered DOM for
 * every clinical string the world seeds — not for a field, for the words themselves.
 */
const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  api.reset();
  resetAccessMemory();
});
afterAll(() => server.close());

function mount(path: string) {
  const services = createServices({ baseUrl: BASE });
  const history = createMemoryHistory({ initialEntries: [path] });
  const view = render(<App services={services} history={history} />);
  return { services, history, view };
}

async function login(username: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

function tenantA() {
  return api.world.tenants.find((t) => t.code === 'DEMO_A')!;
}

/** Every clinical string the world seeds for tenant A: what HR must never be shown. */
function clinicalStrings(): string[] {
  const tenant = tenantA().id;
  const out = new Set<string>();
  for (const d of api.world.diagnoses) {
    const value = api.world.codeValues.find((v) => v.id === d.codeValueId);
    if (d.tenantId === tenant && value) out.add(value.display);
  }
  for (const e of api.world.encounters)
    if (e.tenantId === tenant && e.notesClinical) out.add(e.notesClinical);
  for (const r of api.world.medicalReports)
    if (r.tenantId === tenant && r.clinicalSummary) out.add(r.clinicalSummary);
  for (const l of api.world.claimLines)
    if (l.tenantId === tenant && l.description) out.add(l.description);
  for (const c of api.world.claims)
    if (c.tenantId === tenant && c.reviewCommentMedical) out.add(c.reviewCommentMedical);
  for (const x of api.world.stayExtensions)
    if (x.tenantId === tenant && x.reasonText) out.add(x.reasonText);
  const strings = [...out].filter((s) => s.trim().length > 0);
  expect(
    strings.length,
    'the world seeds no clinical string; the scan would prove nothing',
  ).toBeGreaterThan(4);
  return strings;
}

function claimIn(status: string) {
  const claim = api.world.claims.find((c) => c.tenantId === tenantA().id && c.status === status);
  expect(claim, `no claim in ${status}`).toBeDefined();
  return claim!;
}

function sensitiveCase() {
  const found = api.world.healthCases.find(
    (c) => c.tenantId === tenantA().id && c.sensitivity === 'SENSITIVE',
  );
  expect(found, 'no sensitive case in the world').toBeDefined();
  return found!;
}

async function settled() {
  await waitFor(() => expect(document.querySelectorAll('[aria-busy="true"]')).toHaveLength(0));
  await waitFor(() => expect(screen.queryAllByText('…')).toHaveLength(0), { timeout: 10_000 });
}

describe('sponsor HR', () => {
  it('sees a person’s cases and claims and not one clinical word, on any screen it can reach', async () => {
    const strings = clinicalStrings();
    const person = api.world.people.find((p) => p.id === claimIn('APPROVED').personId)!;
    const { history } = mount(`/people/${person.id}`);
    const user = await login('sponsor.hr');
    await screen.findByRole('tab', { name: 'Sağlık' });
    await user.click(screen.getByRole('tab', { name: 'Sağlık' }));
    const tab = await screen.findByTestId('health-tab');
    await within(tab).findByTestId('person-claims');
    await settled();

    // The tab: references, dates, providers, status — and none of the seeded words.
    const body = document.body.textContent ?? '';
    for (const s of strings) expect(body, `HR screen carries "${s}"`).not.toContain(s);
    expect(within(tab).queryByText(/Tanı/)).toBeNull();

    // Every link the tab offers leads to a page that is just as clean.
    const links = within(tab).getAllByRole('link');
    expect(links.length).toBeGreaterThan(0);
    await user.click(links[0]!);
    await waitFor(() => expect(history.location.pathname).toMatch(/^\/claims\//));
    await screen.findByTestId('claim-lines');
    await settled();
    const claimBody = document.body.textContent ?? '';
    for (const s of strings) expect(claimBody, `HR claim page carries "${s}"`).not.toContain(s);
    expect(screen.getByTestId('claim-lines').getAttribute('data-projection')).toBe('FINANCIAL');
    expect(screen.queryByRole('columnheader', { name: 'Açıklama' })).toBeNull();
    expect(screen.queryByRole('columnheader', { name: 'Tanı' })).toBeNull();
  });
});

describe('the financial reviewer', () => {
  it('is served the money columns and no description column, and decides in figures', async () => {
    const claim = claimIn('PENDING_FINANCIAL');
    mount(`/claims/${claim.id}`);
    const user = await login('financial.reviewer');
    const table = await screen.findByTestId('claim-lines');
    expect(table.getAttribute('data-projection')).toBe('FINANCIAL');
    expect(screen.queryByRole('columnheader', { name: 'Açıklama' })).toBeNull();
    expect(screen.queryByRole('columnheader', { name: 'Tanı' })).toBeNull();
    expect(screen.getByRole('columnheader', { name: 'Ödeyen' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Üye payı' })).toBeInTheDocument();
    // The duplicate suspicion names the other claim's reference.
    const exceptions = screen.getByTestId('claim-exceptions');
    expect(exceptions.textContent).toMatch(/CLM-/);

    // The financial stage asks for figures per line and a reason code.
    const first = screen.getByTestId('decision-1');
    expect(within(first).getByRole('option', { name: 'Kesinti' })).toBeInTheDocument();
    await user.type(within(first).getByLabelText(/^Gerekçe kodu/), 'TARIFF');
    const rows = api.world.claimLines.filter((l) =>
      api.world.claimVersions.some((v) => v.id === l.versionId && v.claimId === claim.id),
    );
    for (const l of rows) {
      if (l.lineNo === 1) continue;
      await user.type(
        within(screen.getByTestId(`decision-${l.lineNo}`)).getByLabelText(/^Gerekçe kodu/),
        'TARIFF',
      );
    }
    await user.click(screen.getByTestId('save-decisions'));
    await waitFor(() =>
      expect(screen.getByText('Satır kararları kaydedildi.')).toBeInTheDocument(),
    );
  });
});

describe('a sensitive case', () => {
  it('asks the medical reviewer for a purpose, and declining is a real path to the financial half', async () => {
    const claim = claimIn('PENDING_MEDICAL');
    claim.caseId = sensitiveCase().id;
    mount(`/claims/${claim.id}`);
    const user = await login('doctor.a');
    const dialog = await screen.findByTestId('purpose-dialog');
    expect(dialog).toBeInTheDocument();
    expect(screen.queryByTestId('claim-lines')).toBeNull();

    await user.click(screen.getByTestId('purpose-decline'));
    const table = await screen.findByTestId('claim-lines');
    expect(table.getAttribute('data-projection')).toBe('FINANCIAL');
    expect(screen.getByTestId('declined-note')).toBeInTheDocument();
    expect(screen.queryByRole('columnheader', { name: 'Açıklama' })).toBeNull();
    const strings = clinicalStrings();
    const body = document.body.textContent ?? '';
    for (const s of strings) expect(body, `declined view carries "${s}"`).not.toContain(s);
    // Declining is not a look: nothing was written to the access log for it.
    const reviewer = api.world.accounts.find((a) => a.username === 'doctor.a')!;
    expect(
      api.world.healthAccessEvents.filter(
        (e) => e.actorId === reviewer.actorId && e.outcome === 'SUCCESS',
      ),
    ).toHaveLength(0);
  });

  it('opens the clinical half with a stated purpose, and the look is on the record', async () => {
    const claim = claimIn('PENDING_MEDICAL');
    claim.caseId = sensitiveCase().id;
    mount(`/claims/${claim.id}`);
    const user = await login('doctor.a');
    await screen.findByTestId('purpose-dialog');
    await user.selectOptions(screen.getByLabelText(/^Amaç/), 'CLAIM_REVIEW');
    await user.type(screen.getByLabelText(/^Gerekçe/), 'ön onay aşımı incelemesi');
    await user.click(screen.getByTestId('purpose-confirm'));
    const table = await screen.findByTestId('claim-lines');
    expect(table.getAttribute('data-projection')).toBe('CLINICAL');
    expect(screen.getByRole('columnheader', { name: 'Açıklama' })).toBeInTheDocument();
    expect(screen.getByTestId('clinical-note')).toBeInTheDocument();
    const reviewer = api.world.accounts.find((a) => a.username === 'doctor.a')!;
    await waitFor(() => {
      const looks = api.world.healthAccessEvents.filter(
        (e) =>
          e.actorId === reviewer.actorId &&
          e.purposeCode === 'CLAIM_REVIEW' &&
          e.outcome === 'SUCCESS',
      );
      expect(looks.length).toBeGreaterThan(0);
      expect(looks.some((e) => e.reasonText === 'ön onay aşımı incelemesi')).toBe(true);
    });
  });
});

describe('the medical reviewer', () => {
  it('decides each line yes or no on clinical grounds and never types a figure', async () => {
    const claim = claimIn('PENDING_MEDICAL');
    mount(`/claims/${claim.id}`);
    const user = await login('doctor.a');
    const table = await screen.findByTestId('claim-lines');
    expect(table.getAttribute('data-projection')).toBe('CLINICAL');
    expect(screen.getByRole('columnheader', { name: 'Açıklama' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Tanı' })).toBeInTheDocument();
    expect(screen.queryByRole('columnheader', { name: 'Ödeyen' })).toBeNull();
    const first = screen.getByTestId('decision-1');
    expect(
      within(first)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['Onaylandı', 'Reddedildi']);
    expect(within(first).queryByLabelText(/Ödeyen tutarı/)).toBeNull();

    const rows = api.world.claimLines.filter((l) =>
      api.world.claimVersions.some((v) => v.id === l.versionId && v.claimId === claim.id),
    );
    for (const l of rows) {
      await user.type(
        within(screen.getByTestId(`decision-${l.lineNo}`)).getByLabelText(/^Gerekçe kodu/),
        'CLINICALLY_INDICATED',
      );
    }
    await user.click(screen.getByTestId('save-decisions'));
    await waitFor(() =>
      expect(screen.getByText('Satır kararları kaydedildi.')).toBeInTheDocument(),
    );
    // Medical before financial: the claim moved on to the stage the submit said was still owed.
    await waitFor(() =>
      expect(screen.getByTestId('claim-status')).toHaveTextContent('Mali incelemede'),
    );
  });
});

describe('sensitive record navigation', () => {
  it.each(['grant', 'decline'] as const)('does not carry %s to another claim', async (choice) => {
    const first = claimIn('PENDING_MEDICAL');
    const second = claimIn('APPROVED');
    first.caseId = sensitiveCase().id;
    second.caseId = sensitiveCase().id;
    const { history } = mount(`/claims/${first.id}`);
    const user = await login('doctor.a');
    await screen.findByTestId('purpose-dialog');
    await user.click(
      screen.getByTestId(choice === 'grant' ? 'purpose-confirm' : 'purpose-decline'),
    );
    await screen.findByTestId('claim-lines');
    await history.push(`/claims/${second.id}`);
    await screen.findByTestId('purpose-dialog');
    expect(screen.queryByTestId('claim-lines')).toBeNull();
    await user.click(screen.getByTestId('purpose-decline'));
    await screen.findByTestId('claim-lines');
    await history.push(`/claims/${first.id}`);
    await waitFor(() =>
      expect(screen.getByTestId('claim-lines')).toHaveAttribute(
        'data-projection',
        choice === 'grant' ? 'CLINICAL' : 'FINANCIAL',
      ),
    );
    expect(screen.queryByTestId('purpose-dialog')).toBeNull();
  });
});

describe('sensitive access context', () => {
  it.each(['actor', 'tenant', 'session'] as const)(
    'asks again after the %s changes',
    async (boundary) => {
      const claim = claimIn('PENDING_MEDICAL');
      const { services, view } = mount(`/claims/${claim.id}`);
      await login('doctor.a');
      await screen.findByTestId('claim-lines');
      view.unmount();
      const hook = renderHook(() => useAccessState(claim.id), {
        wrapper: ({ children }) => (
          <SessionProvider store={services.store}>{children}</SessionProvider>
        ),
      });
      act(() => hook.result.current.grant('MEDICAL_REVIEW', 'For this record only'));
      expect(accessFor(hook.result.current.state)?.purpose).toBe('MEDICAL_REVIEW');
      const before = services.store.getState();
      expect(before.session).not.toBeNull();
      expect(before.activeTenant).not.toBeNull();
      act(() => {
        if (boundary === 'actor')
          services.store.setState({ session: { ...before.session!, actorId: 'another-reviewer' } });
        if (boundary === 'session')
          services.store.setState({
            session: { ...before.session!, expiresAt: '2099-01-01T00:00:00Z' },
          });
        if (boundary === 'tenant')
          services.store.setState({
            activeTenant: {
              ...before.activeTenant!,
              tenant: { ...before.activeTenant!.tenant, id: 'another-tenant' },
            },
          });
      });
      expect(accessFor(hook.result.current.state)).toBeUndefined();
    },
  );
});
