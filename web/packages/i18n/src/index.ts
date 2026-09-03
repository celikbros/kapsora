import i18next, { type i18n, type TFunction } from 'i18next';
import { initReactI18next } from 'react-i18next';
import en from './locales/en.json';
import tr from './locales/tr.json';

export { Trans, useTranslation } from 'react-i18next';
export * from './format';

/** Turkish is complete; English is a skeleton that falls back to Turkish per key. */
export const resources = {
  tr: { common: tr },
  en: { common: en },
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
      ns: ['common'],
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

export { i18next };
