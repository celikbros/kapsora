# WP-I6-05 · Screens: the member's first real app (search, hold, confirm, voucher, cancel), the property's desk (allotment, arrivals, departures, no-show), the payer's bookings

| Field                      | Value                                                                                                                                                                                                                                                                                                                                   |
| -------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M6 (plan increment I6)                                                                                                                                                                                                                                                                                                                  |
| Size                       | L                                                                                                                                                                                                                                                                                                                                       |
| Depends on                 | contracts of WP-I6-01..04 (mock first, real API when they land)                                                                                                                                                                                                                                                                         |
| Runs in parallel with      | all M6 packages                                                                                                                                                                                                                                                                                                                         |
| Migration numbers assigned | none                                                                                                                                                                                                                                                                                                                                    |
| OpenAPI operations owned   | none (consumes)                                                                                                                                                                                                                                                                                                                         |
| Read first                 | `DESIGN.md` (all of **Patterns settled while building**, now forty-two entries) and `PRODUCT.md`; WP-I4-06 and WP-I5-06 as delivered and their finish-review notes in ROADMAP.md (2026-09-05/06); the provider and backoffice surface briefs under `web/apps/*/.impeccable/surfaces/`; the member PWA shell (`web/apps/member/src/App.tsx`) as it stands — login, tenant, a "Yakında" home |

## 1. Goal

The member PWA becomes a product: a person on a phone, low patience, in Turkish, finds a
room their plan covers, holds it, sees to the kuruş what they will pay, confirms, and has
the voucher on the same screen. The property's desk opens its allotment, meets arrivals by
their voucher and sends departures off with the nights they used. The payer sees every
booking as a sequence and reviews the no-shows. The milestone is judged by two of these:
**the member sees the net amount before confirming**, and **the room is never oversold** —
the second is the server's, but the screens must never make it look otherwise.

## 2. Scope

### 2.1 Member PWA (new surface world; Impeccable's new-work flow, a concept round is owed)

Mobile-first, `390` is the first viewport, `1440` the second.

1. **Ana sayfa** — the person's entitlements as remaining figures (nights first), the next
   booking with its countdown or its dates, one action: Konaklama ara.
2. **Ara** — dates as a range, guests, region or property; results as rooms with `available`,
   the nights, and the quote: **"Ödeyeceğiniz"** as the member amount, the plan's share
   beside it, exact strings from the server, nothing computed here. A room the plan does
   not cover says why (the eligibility explanation) rather than hiding.
3. **Tut** — the hold with its countdown (the server's `holdExpiresAt`, not a client timer
   started on click), the guests, the policy in plain words (free until …, then …), and
   **Onayla** showing the member amount once more on the button's own line. Step-up when the
   server asks.
4. **Onay** — the voucher's QR and code shown **once**, with "Kuponu yeniden üret" on the
   booking page later; the message says the token is never sent by e-mail.
5. **Rezervasyonlarım** — list and detail: dates, property, status as it is, the fee a
   cancellation would cost now (`previewCancellation`) before "İptal et", the waitlist entry
   with its position, the offer with its expiry.

### 2.2 Provider portal (extends the form-first world; Impeccable's extension flow)

1. **Kontenjan** — a month per room type edited in place (capacity per day; held and
   confirmed read-only beside it), the season opened with one range.
2. **Gelenler / Gidenler** — today's arrivals with check-in by token (typed or scanned), a
   refused token named as such; departures with check-out and the actual nights the server
   computed shown before the command.
3. **No-show bildir** — the booking, the evidence upload through the document panel, the
   assessed fee from the snapshot, and the note that a second person confirms it.

### 2.3 Backoffice (extends the request-detail sequence)

1. **Tesisler** — properties and room types per provider, the lodging terms on the contract
   version (writable on a draft only, as every other term).
2. **Rezervasyonlar** — list and the detail as a sequence: what was searched and shown,
   what was held and when it expires, what the request decided, what was confirmed, what
   happened at the door, what it cost; the cancellation row with its snapshot.
3. **No-show incelemesi** and the **bekleme listesi** per property.
4. Person detail gains **Konaklama** (bookings, waitlist entries).

## 3. Design notes

- **The countdown is the server's.** `holdExpiresAt` is the truth; the client renders it
  and refreshes the booking when it passes. A hold that expired while the screen slept
  shows "Süresi doldu" and the way back to search, never a stale "Onayla".
- **Money is shown, never made.** Every amount on every screen is a server string.
- **The voucher is shown once and rotated on request**, never stored in the browser.
- **A refusal names the room and the night** (`ROOM_UNAVAILABLE` carries the first full
  night; the screen says it).
- Everything else follows DESIGN.md's settled patterns; the member world's own decisions
  (palette within the tokens, type at phone sizes, the countdown's motion) are the concept
  round's, recorded in a member surface brief.

## 4. Tests required

- Mock world: two properties, four room types, a season, a member bound to a person with
  nights remaining, a hold, a confirmed booking with a voucher, a completed and a cancelled
  one, a no-show report, a waitlist entry, and `member.a`, `reservation.a` accounts.
- Vitest flows: the member amount is visible before Onayla and equals the quote's string;
  the countdown reads the server's expiry and the expired state shows the way back; the
  voucher token appears once and is absent from any storage; the cancellation fee shown
  equals `previewCancellation`; check-in by a wrong token is a named refusal; the
  allotment editor refuses a capacity below held + confirmed with the server's date.
- Playwright: a member searches, holds, confirms and sees the voucher; a provider checks
  the same booking in by token; the payer opens the booking and reads the sequence.
- `pnpm design` clean; Impeccable's finish review as the skill directs, captures at 390 and
  1440; the member surface brief written before the build; i18n under `lodging.*` complete
  in Turkish. **Update `DESIGN.md` in the same commit as any screen that settles a pattern.**

## 5. Acceptance criteria

- [ ] A member sees the exact amount they will pay before confirming, on a phone.
- [ ] No screen ever computes, rounds or caches money or a token.
- [ ] A property's desk can run a day — allotment, arrivals, departures, a no-show — from
      the portal alone.
- [ ] Lint, typecheck, unit, smoke, the detector and the finish review all clean.
