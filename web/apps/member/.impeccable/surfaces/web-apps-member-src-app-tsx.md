---
version: 1
slug: 'web-apps-member-src-app-tsx'
primary_target: 'web/apps/member/src/App.tsx'
related_targets:
  [
    'web/apps/member/src/main.tsx',
    'web/apps/member/src/lodging/SearchPage.tsx',
    'web/apps/member/src/lodging/BookingPage.tsx',
  ]
---

Scope: `web/apps/member` — the entitled person's own app, from M6 a product: what the plan has left, a room the plan carries, a hold, the exact amount they will pay, the confirmation, the voucher at the door, the cancellation and what it costs. Mode: Operate on the booking flow (the visitor completes a task: find, hold, confirm), Read on the home and the booking list. Audience: a member on a phone, low patience, Turkish, often between other things; `390` is the first viewport and `1440` the second. Content: remaining entitlements as figures the server sent, the search's rooms with their nights and quote, the hold with the server's expiry, the booking as it is, the voucher shown once, the cancellation preview before the command. Constraints: a member is bound to one person on the server and never names one; decimal strings are shown and never computed; the countdown renders `holdExpiresAt`, never a timer started on click; the voucher token is shown once and kept nowhere; nothing in browser storage; one primary action per screen.

## Direction contract

THESIS: The booking is a receipt that fills in as the member chooses. Every step appends a line — the dates, the room, the plan's share, **Ödeyeceğiniz** — and the receipt stays pinned at the bottom with the one action on it, so the amount the member will pay is on screen before, during and after they commit. It refuses the consumer-travel pattern of photo cards, a hidden total and a surprise on the last screen.

OWN-WORLD: DESIGN.md's world unchanged and inherited whole: `bg` ground, white `surface` panels, one `primary` action, monospace right-aligned money with its ISO code, status as a `Badge`, the tenant named in the header. The member surface adds nothing to the palette or the type scale; its only new structure is the pinned receipt and the bottom tab bar, both drawn from the tokens.

STORY: The member opens the app and reads what is left — "2 gece" first — and the next stay if there is one. Konaklama ara: dates, guests, where; the rooms answer with their nights and "Ödeyeceğiniz"; a room the plan does not fully carry says so on its own line. Odayı tut: the receipt is complete, the countdown reads the server's deadline, the policy is a sentence, and Onayla carries the amount on its own line. The voucher appears once, with the way to make another. Later, Rezervasyonlarım: each booking as it is; on one of them, "İptal edersem ne öderim" is answered before İptal et is offered.

FIRST VIEWPORT (390): the header with the tenant badge; the home's remaining figures as a plain two-column list (entitlement · remaining with unit), the next booking under it with its dates and status, then one button. On the search: the three fields stacked, results as rows (property · room type · nights · Ödeyeceğiniz), and the receipt pinned at the bottom from the moment a room is chosen. The bottom tab bar (Ana sayfa · Ara · Rezervasyonlarım) on every screen.

FORM: Receipt sheet, lead of the surface roll (seed c2ea6938, dealt 6·1·7, the receipt locked by the owner on 2026-09-07 with "kalan haklar önce" and the bottom tab bar).

FINISH: unreviewed and undocumented is unfinished; this build ends with the detector, the finish review, DESIGN.md, and captures at 390 and 1440 carrying their provenance.
