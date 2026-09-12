import i18next, { type i18n, type TFunction } from 'i18next';
import { initReactI18next } from 'react-i18next';
import en from './locales/en.json';
import helpBackofficeEn from './locales/help/en/backoffice.json';
import helpBackofficeSetupEn from './locales/help/en/backoffice-setup.json';
import helpMemberEn from './locales/help/en/member.json';
import helpProviderEn from './locales/help/en/provider.json';
import helpTermsEn from './locales/help/en/terms.json';
import helpBackofficeTr from './locales/help/tr/backoffice.json';
import helpBackofficeSetupTr from './locales/help/tr/backoffice-setup.json';
import helpMemberTr from './locales/help/tr/member.json';
import helpProviderTr from './locales/help/tr/provider.json';
import helpTermsTr from './locales/help/tr/terms.json';
import tr from './locales/tr.json';

export { Trans, useTranslation } from 'react-i18next';
export * from './format';

/**
 * Turkish is complete; English is a skeleton that falls back to Turkish per key.
 *
 * `common` is the product's own words. `help` is what the product says about itself —
 * the explanation of a term where it stands and of a page as a whole — kept apart so the
 * words are data: one file per app plus the shared terms, and a tenant can be given its
 * own wording later without touching a screen.
 */
export const resources = {
  tr: {
    common: tr,
    help: {
      terms: helpTermsTr,
      pages: {
        backoffice: { ...helpBackofficeSetupTr, ...helpBackofficeTr },
        provider: helpProviderTr,
        member: helpMemberTr,
      },
    },
  },
  en: {
    common: en,
    help: {
      terms: helpTermsEn,
      pages: {
        backoffice: { ...helpBackofficeSetupEn, ...helpBackofficeEn },
        provider: helpProviderEn,
        member: helpMemberEn,
      },
    },
  },
} as const;

export type Locale = keyof typeof resources;

/** Initialises the shared i18next instance; call once per app before rendering. */
export function initI18n(locale: Locale = 'tr'): i18n {
  if (!i18next.isInitialized) {
    void i18next.use(initReactI18next).init({
      resources,
      lng: locale,
      fallbackLng: 'tr',
      defaultNS: 'common',
      ns: ['common', 'help'],
      interpolation: { escapeValue: false },
      returnNull: false,
      initImmediate: false,
    });
  } else if (i18next.language !== locale) {
    void i18next.changeLanguage(locale);
  }
  return i18next;
}

/** Message for a problem code; unknown codes get the generic text. */
export function problemMessage(t: TFunction, code: string, fallbackTitle?: string): string {
  const key = `problems.${code}`;
  if (i18next.exists(key)) {
    return t(key);
  }
  return fallbackTitle && fallbackTitle.trim() !== '' ? fallbackTitle : t('problems.UNKNOWN');
}

/** Message for a field error code; falls back to the server message, then the code. */
export function fieldErrorMessage(
  t: TFunction,
  code: string,
  serverMessage?: string,
  params?: Record<string, unknown>,
): string {
  const key = `fieldErrors.${code}`;
  if (i18next.exists(key)) {
    return t(key, params ?? {});
  }
  return serverMessage && serverMessage.trim() !== '' ? serverMessage : code;
}

/** One shared domain term, explained where it stands. */
export interface HelpTerm {
  title: string;
  body: string;
}

/** One named thing on a page — a section, an action or a status — and what it means. */
export interface HelpEntry {
  label: string;
  body: string;
}

/** What a page is for, in page order: its purpose, its sections, its actions, its statuses. */
export interface HelpPage {
  title: string;
  purpose: string;
  sections?: HelpEntry[];
  actions?: HelpEntry[];
  statuses?: HelpEntry[];
}

export type HelpApp = 'backoffice' | 'provider' | 'member';

/**
 * The `help` resource at `key`: the Turkish one, with whatever the current language has
 * written laid over it field by field — so an English page that names only its title still
 * carries the Turkish purpose and lists rather than empty ones.
 */
function helpResource(key: string): unknown {
  const turkish = i18next.getResource('tr', 'help', key) as unknown;
  const current = i18next.language ?? 'tr';
  if (current === 'tr') return turkish;
  const found = i18next.getResource(current, 'help', key) as unknown;
  if (found === undefined) return turkish;
  if (turkish && typeof turkish === 'object' && found && typeof found === 'object') {
    return { ...(turkish as object), ...(found as object) };
  }
  return found;
}

/** The explanation of a shared term, or nothing when none is written. */
export function helpTerm(key: string): HelpTerm | undefined {
  const found = helpResource(`terms.${key}`);
  return found && typeof found === 'object' ? (found as HelpTerm) : undefined;
}

/** The help written for one page of one app, or nothing when none is written. */
export function helpPage(app: HelpApp, key: string): HelpPage | undefined {
  const found = helpResource(`pages.${app}.${key}`);
  return found && typeof found === 'object' ? (found as HelpPage) : undefined;
}

export { i18next };
