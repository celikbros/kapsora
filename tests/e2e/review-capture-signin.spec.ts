import { test } from '@playwright/test';

import { captureSignIn } from './signin-capture';

/** The backoffice sign-in screen for the design review (REVIEW_CAPTURE=1 only). */
test.skip(!process.env['REVIEW_CAPTURE'], 'set REVIEW_CAPTURE=1 to capture review screenshots');

test('backoffice sign-in', async ({ page }) => {
  await captureSignIn(page, 'backoffice');
});
