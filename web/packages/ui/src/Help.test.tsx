import { i18next, initI18n, type HelpApp } from '@kapsora/i18n';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeAll, describe, expect, it } from 'vitest';

import { FormField } from './FormField';
import { HelpDrawer, HelpHint, matchHelpKey, pageHelpFor } from './Help';
import { Input } from './Input';

beforeAll(() => {
  initI18n('tr');
  i18next.addResourceBundle(
    'tr',
    'help',
    {
      terms: {
        mahsup: {
          title: 'Mahsup',
          body: 'Ödenecek tutardan düşülen, daha önce fazla ödenmiş para.',
        },
      },
      // A page of its own, so the real content the apps ship cannot merge into it.
      pages: {
        backoffice: {
          testPage: {
            title: 'Deneme sayfası',
            purpose: 'Bekleyen işler, hangisi sizde, hangisi gecikti.',
            sections: [{ label: 'Bende', body: 'Üstlendiğiniz işler.' }],
            statuses: [{ label: 'Gecikti', body: 'Termini geçmiş iş.' }],
          },
        },
      },
    },
    true,
    true,
  );
});

describe('the help mark', () => {
  it('opens on press with the term explained, and closes on Escape', async () => {
    render(<HelpHint term="mahsup" />);
    const mark = screen.getByRole('button', { name: 'Yardım: Mahsup' });
    await userEvent.click(mark);
    const popover = await screen.findByRole('dialog');
    expect(within(popover).getByText('Mahsup')).toBeInTheDocument();
    expect(within(popover).getByText(/fazla ödenmiş para/)).toBeInTheDocument();
    await userEvent.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('opens from the keyboard with the explanation written in place', async () => {
    render(<HelpHint title="Onaylanan" body="Sunucunun karar verdiği tutar." />);
    await userEvent.tab();
    expect(screen.getByRole('button', { name: 'Yardım: Onaylanan' })).toHaveFocus();
    await userEvent.keyboard('{Enter}');
    expect(await screen.findByRole('dialog')).toHaveTextContent('Sunucunun karar verdiği tutar.');
  });

  it('opens for a mouse hover and not for a finger, and a hover never moves focus', async () => {
    render(
      <>
        <input aria-label="Not" />
        <HelpHint title="Vade" body="Ödemenin son günü." />
      </>,
    );
    const input = screen.getByRole('textbox', { name: 'Not' });
    input.focus();
    const mark = screen.getByRole('button', { name: 'Yardım: Vade' });

    const pointer = (type: string, pointerType: string) =>
      new window.PointerEvent(type, { pointerType, bubbles: true, cancelable: true });

    fireEvent(mark, pointer('pointerover', 'touch'));
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(screen.queryByRole('dialog')).toBeNull();

    fireEvent(mark, pointer('pointerover', 'mouse'));
    const hint = await screen.findByRole('dialog', { name: 'Vade' });
    expect(hint).toHaveTextContent('Ödemenin son günü.');
    expect(input).toHaveFocus();
    fireEvent(mark, pointer('pointerout', 'mouse'));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    // Closing what a hover opened leaves the person where they were typing.
    expect(input).toHaveFocus();
  });

  it('pins open on a press after a hover, and a second press closes it', async () => {
    render(<HelpHint title="Vade" body="Ödemenin son günü." />);
    const mark = screen.getByRole('button', { name: 'Yardım: Vade' });
    const pointer = (type: string, pointerType: string) =>
      new window.PointerEvent(type, { pointerType, bubbles: true, cancelable: true });

    fireEvent(mark, pointer('pointerover', 'mouse'));
    await screen.findByRole('dialog');
    await userEvent.click(mark);
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    // Pinned: the pointer leaving no longer closes it.
    fireEvent(mark, pointer('pointerout', 'mouse'));
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    await userEvent.click(mark);
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('renders nothing for a term nobody has written', () => {
    const { container } = render(<HelpHint term="yok" />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe('a field with help', () => {
  it('keeps the mark beside the label, never inside it', async () => {
    render(
      <FormField label="Mahsup" help={<HelpHint term="mahsup" />}>
        <Input name="offset" />
      </FormField>,
    );
    const input = screen.getByLabelText('Mahsup');
    expect(input).toHaveAttribute('name', 'offset');
    const mark = screen.getByRole('button', { name: 'Yardım: Mahsup' });
    expect(mark.closest('label')).toBeNull();
    await userEvent.click(mark);
    expect(await screen.findByRole('dialog')).toBeInTheDocument();
  });
});

describe('the page help', () => {
  it('matches the most specific route and falls back to the app', () => {
    const routes = {
      '/': 'home',
      '/claims': 'claims',
      '/claims/new': 'claimNew',
      '/claims/$claimId': 'claim',
      '/organizations/$organizationId/edit': 'organizationEdit',
    };
    expect(matchHelpKey('/', routes)).toBe('home');
    expect(matchHelpKey('/claims', routes)).toBe('claims');
    expect(matchHelpKey('/claims/new', routes)).toBe('claimNew');
    expect(matchHelpKey('/claims/01J', routes)).toBe('claim');
    expect(matchHelpKey('/claims/01J/', routes)).toBe('claim');
    expect(matchHelpKey('/organizations/9/edit', routes)).toBe('organizationEdit');
    expect(matchHelpKey('/organizations/9', routes)).toBeUndefined();

    const fallback = { title: 'Yardım', purpose: 'Henüz yazılmadı.' };
    expect(pageHelpFor('backoffice', '/test', { '/test': 'testPage' }, fallback).title).toBe(
      'Deneme sayfası',
    );
    // A route with no page of its own gets the app's help, which every app ships.
    expect(pageHelpFor('backoffice', '/nowhere', {}, fallback).title).toBe('Yönetim paneli');
    expect(pageHelpFor('member', '/nowhere', {}, fallback).title).toBe('Üye uygulaması');
    // An app nothing is written for gets the fallback the shell handed in.
    expect(pageHelpFor('nowhere' as HelpApp, '/nowhere', {}, fallback)).toBe(fallback);
  });

  it('shows the page in a drawer: purpose, sections and statuses in order', () => {
    const page = pageHelpFor(
      'backoffice',
      '/test',
      { '/test': 'testPage' },
      {
        title: '',
        purpose: '',
      },
    );
    render(<HelpDrawer open onOpenChange={() => {}} page={page} />);
    const drawer = screen.getByRole('dialog', { name: 'Deneme sayfası' });
    expect(within(drawer).getByText(/hangisi gecikti/)).toBeInTheDocument();
    const headings = within(drawer)
      .getAllByRole('heading', { level: 3 })
      .map((heading) => heading.textContent);
    expect(headings).toEqual(['Bölümler', 'Durumlar']);
    expect(within(drawer).getAllByRole('term')[0]).toHaveTextContent('Bende');
    expect(within(drawer).getByRole('button', { name: 'Kapat' })).toBeInTheDocument();
  });
});
