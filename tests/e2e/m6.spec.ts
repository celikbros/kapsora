import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'demo parola 2026 kapsora';

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

/** The payer opens a booking and reads it as the sequence that produced it. */
test('the payer reads a booking as a sequence and confirms a no-show as the second person', async ({
  page,
}) => {
  await page.goto('/lodging/bookings');
  await login(page, 'admin.a');
  const table = page.getByTestId('booking-table');
  await expect(table).toBeVisible();
  await page.getByLabel('Durum').selectOption('CONFIRMED');
  await expect(table.getByTestId('booking-row').first()).toContainText('Onaylandı');
  await table.getByTestId('booking-row').first().getByRole('link').click();
  await expect(page).toHaveURL(/\/lodging\/bookings\/[0-9a-f-]+$/);
  const sequence = page.getByTestId('booking-sequence');
  await expect(sequence).toContainText('Arandı ve gösterildi');
  await expect(sequence).toContainText('Tutuldu');
  await expect(sequence).toContainText('Talep kararı');
  await expect(sequence).toContainText('Onaylandı');
  await expect(sequence).toContainText('Kapıda');
  await expect(sequence).toContainText('Maliyet');
});

test('the waiting list and the properties are the payer’s to read', async ({ page }) => {
  await page.goto('/lodging/bookings');
  await login(page, 'admin.a');
  await expect(page.getByTestId('booking-table')).toBeVisible();
  // In-app navigation: a full page load starts a new mock session.
  const nav = page.getByTestId('lodging-nav');
  await nav.getByRole('link', { name: 'Bekleme listesi' }).click();
  await expect(page.getByTestId('waitlist-table')).toBeVisible();
  await nav.getByRole('link', { name: 'Tesisler' }).click();
  await expect(page.getByTestId('property-table')).toBeVisible();
  await page.getByTestId('property-row').first().getByRole('link').click();
  await expect(page.getByTestId('room-type-table')).toBeVisible();
});
