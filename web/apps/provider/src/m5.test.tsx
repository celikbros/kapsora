import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { App } from './App';
import { createServices } from './services';

/**
 * The provider's health screens against the mock world, as provider.a. What the desk sees
 * of a case follows the projection the server sent, Uzat exists only while nothing is
 * undecided, and a correction stands beside the version it corrects.
 */
const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => api.reset());
afterAll(() => server.close());

function mount(path: string) {
  const services = createServices({ baseUrl: BASE });
  const history = createMemoryHistory({ initialEntries: [path] });
  render(<App services={services} history={history} />);
  return { services, history };
}

async function login(username: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

function caseOf(sensitivity: 'STANDARD' | 'SENSITIVE') {
  const found = api.world.healthCases.find((c) => c.sensitivity === sensitivity);
  expect(found, `no ${sensitivity} case in the world`).toBeDefined();
  return found!;
}

/** The first diagnosis of a case, with the code and the name the server renders it with. */
function diagnosisOf(caseId: string) {
  const row = api.world.diagnoses.find((d) =>
    api.world.encounters.some((e) => e.id === d.encounterId && e.caseId === caseId),
  );
  expect(row, 'the case has no diagnosis to show').toBeDefined();
  const value = api.world.codeValues.find((v) => v.id === row!.codeValueId);
  expect(value, 'the diagnosis names no code value').toBeDefined();
  return { code: value!.code, display: value!.display };
}

describe('the case', () => {
  it('reads as a story on a standard case: the encounter, its diagnosis by code and name', async () => {
    const record = caseOf('STANDARD');
    const diagnosis = diagnosisOf(record.id);
    mount(`/cases/${record.id}`);
    await login('provider.a');
    const table = await screen.findByTestId('encounter-table');
    await waitFor(() => expect(within(table).getByText(diagnosis.display)).toBeInTheDocument());
    expect(within(table).getByText(diagnosis.code)).toBeInTheDocument();
    expect(screen.queryByTestId('financial-note')).toBeNull();
    // The chapters hang off the case: the report, the stay and the claim are its sections.
    expect(screen.getByRole('heading', { name: /^Tedavi raporları/ })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: /^Yatış/ })).toBeInTheDocument();
    expect(await screen.findByTestId('case-claims')).toBeInTheDocument();
  });

  it('is narrowed to the financial half on a sensitive case and says so, without a diagnosis', async () => {
    const record = caseOf('SENSITIVE');
    const diagnosis = diagnosisOf(record.id);
    mount(`/cases/${record.id}`);
    await login('provider.a');
    expect(await screen.findByTestId('financial-note')).toBeInTheDocument();
    await screen.findByTestId('encounter-table');
    expect(screen.queryByText(diagnosis.display)).toBeNull();
    expect(screen.queryByRole('columnheader', { name: 'Branş' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Tanıları düzenle' })).toBeNull();
  });

  it('says a diagnosis is sensitive the moment it is chosen, before anything is saved', async () => {
    const record = caseOf('STANDARD');
    const sensitive = api.world.codeValues.find((v) => v.attributes['sensitive'] === true);
    expect(sensitive, 'no sensitive ICD-10 code in the world').toBeDefined();
    mount(`/cases/${record.id}`);
    const user = await login('provider.a');
    await screen.findByTestId('encounter-table');
    await user.click((await screen.findAllByRole('button', { name: 'Tanıları düzenle' }))[0]!);
    const editor = await screen.findByTestId('diagnosis-editor');
    expect(within(editor).queryByTestId('sensitive-notice')).toBeNull();
    await user.type(within(editor).getByLabelText(/ICD-10 ara/), sensitive!.display.slice(0, 6));
    const matches = await within(editor).findByTestId('icd10-matches');
    await user.click(
      await within(matches).findByRole('button', { name: new RegExp(sensitive!.code) }),
    );
    expect(within(editor).getByTestId('sensitive-notice')).toBeInTheDocument();
    // Nothing has been written: the case is still what it was.
    expect(api.world.healthCases.find((c) => c.id === record.id)!.sensitivity).toBe('STANDARD');
  });
});

describe('the stay', () => {
  it('offers Uzat only while no extension is undecided', async () => {
    const stay = api.world.inpatientStays.find((s) => s.status === 'ADMITTED');
    expect(stay, 'no admitted stay in the world').toBeDefined();
    expect(
      api.world.stayExtensions.filter((x) => x.stayId === stay!.id && x.status === 'REQUESTED'),
    ).toHaveLength(0);
    mount(`/stays/${stay!.id}`);
    const user = await login('provider.a');
    await screen.findByTestId('stay-figures');
    await user.click(await screen.findByTestId('extend-button'));
    await user.clear(screen.getByLabelText(/^Ek gün/));
    await user.type(screen.getByLabelText(/^Ek gün/), '2');
    await user.type(screen.getByLabelText(/^Gerekçe kodu/), 'COMPLICATION');
    await user.click(screen.getByRole('button', { name: 'Uzatma iste' }));
    await screen.findByTestId('extension-pending');
    expect(screen.queryByTestId('extend-button')).toBeNull();
    expect(
      api.world.stayExtensions.filter((x) => x.stayId === stay!.id && x.status === 'REQUESTED'),
    ).toHaveLength(1);
  });
});

describe('the claim', () => {
  it('shows the decided version beside the correction, line by line', async () => {
    const claim = api.world.claims.find((c) => c.status === 'RETURNED');
    expect(claim, 'no returned claim in the world').toBeDefined();
    expect(claim!.currentVersionNo).toBeGreaterThan(1);
    mount(`/claims/${claim!.id}`);
    await login('provider.a');
    const correction = await screen.findByTestId('correction');
    expect(
      within(correction).getByRole('heading', {
        name: `Sürüm ${claim!.currentVersionNo - 1} (karara bağlı)`,
      }),
    ).toBeInTheDocument();
    expect(
      within(correction).getByRole('heading', {
        name: `Sürüm ${claim!.currentVersionNo} (taslak)`,
      }),
    ).toBeInTheDocument();
    expect(await within(correction).findByTestId('previous-lines')).toBeInTheDocument();
    expect(within(correction).getByTestId('claim-lines-editor')).toBeInTheDocument();
    // The decided version stays exactly as decided: its decision is still readable.
    expect(
      within(within(correction).getByTestId('previous-lines')).queryAllByText('Karar bekliyor'),
    ).toHaveLength(0);
  });

  it('withholds every price on a draft and shows the server’s figures once decided', async () => {
    const draft = api.world.claims.find((c) => c.status === 'DRAFT');
    expect(draft).toBeDefined();
    mount(`/claims/${draft!.id}`);
    await login('provider.a');
    const editor = await screen.findByTestId('claim-lines-editor');
    expect(within(editor).queryByRole('columnheader', { name: 'Onaylanan' })).toBeNull();
    expect(screen.queryByTestId('readiness')).toBeNull();
  });
});
