import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'demo parola 2026 kapsora';

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

/**
 * The property desk: the allotment a night at a time, refused below what is promised, and
 * the door, where a wrong token is refused by name. The seeded arrivals lie weeks ahead, so
 * a successful check-in is the unit test's to prove (it moves a booking to today); here
 * the desk proves its refusals, which is what a desk sees most.
 */
test('the desk opens a season, is refused below the commitment, and names a wrong token', async ({
  page,
}) => {
  await page.goto('/lodging/inventory');
  await login(page, 'reservation.a');
  await expect(page.getByRole('heading', { name: 'Kontenjan' })).toBeVisible();
  await page.getByLabel(/^Tesis/).selectOption({ index: 1 });
  await page.getByLabel(/^Oda tipi/).selectOption({ index: 1 });
  const table = page.getByTestId('inventory-table');
  await expect(table).toBeVisible();
  const rows = table.getByTestId('inventory-row');
  await expect(rows.first()).toBeVisible();
  // A night with something already promised: capacity zero is refused, and the refusal
  // arrives as the server's own sentence. The night is found by its counters, not guessed.
  const count = await rows.count();
  let committed = rows.first();
  for (let i = 0; i < count; i += 1) {
    const cells = await rows.nth(i).locator('td').allInnerTexts();
    if (Number(cells[2]) + Number(cells[3]) > 0) {
      committed = rows.nth(i);
      break;
    }
  }
  await committed.getByRole('button', { name: 'Düzenle' }).click();
  await committed.getByRole('spinbutton').fill('0');
  await committed.getByRole('button', { name: 'Kaydet' }).click();
  await expect(page.getByText(/zaten tutulmuş veya onaylanmış/).first()).toBeVisible();

  await page
    .getByRole('navigation', { name: 'Sağlayıcı portalı' })
    .getByRole('link', { name: 'Gelenler / Gidenler' })
    .click();
  await expect(page.getByRole('heading', { name: 'Gelenler / Gidenler' })).toBeVisible();
  const arrival = page.getByTestId('arrival-row').first();
  await expect(arrival).toBeVisible();
  await arrival.getByLabel(/Kupon kodu/).fill('KPS-WRONGTOKEN');
  await arrival.getByRole('button', { name: 'Giriş yap' }).click();
  await expect(arrival.getByRole('alert')).toBeVisible();
});
