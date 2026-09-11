import { appUrlsFrom } from '@kapsora/auth';

/** Where the three apps live, for the single sign-in's hand-over between them. */
export const APP_URLS = appUrlsFrom(import.meta.env, import.meta.env.DEV, window.location);
