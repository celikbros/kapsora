# WP-I4-06 · Screens: requests, worklist, documents — and the first provider portal

| Field                      | Value                                                                                                                                                          |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M4 (plan increment I4)                                                                                                                                         |
| Size                       | L                                                                                                                                                              |
| Depends on                 | contracts of WP-I4-01..05 (mock first, real API when they land)                                                                                                |
| Runs in parallel with      | all M4 packages                                                                                                                                                |
| Migration numbers assigned | none                                                                                                                                                           |
| OpenAPI operations owned   | none (consumes)                                                                                                                                                |
| Read first                 | `DESIGN.md` — especially **Patterns settled while building** — and `PRODUCT.md`; WP-I3-06 as delivered; `web/apps/provider` (the shell exists, empty since M1) |

## 1. Goal

The screens where the work actually happens, and the first screens a provider ever sees.
Until now KAPSORA has been configured; from here it is used.

## 2. Scope

### 2.1 Backoffice

1. **Talepler**: request list with status, person, provider, program, date and channel
   filters; request detail showing the current version, its items, the eligibility
   explanation and the rules that fired, the version history, and the documents linked to
   it. The commands are separate buttons with their own confirmations — **Gönder, İade et,
   Reddet, Onayla, Kısmen onayla, İptal et** — and the screen never offers one the status
   does not allow.
2. **İş listem**: the queue view. What is mine, what is unassigned, what is late. Claiming
   is one click and a claim somebody else won says who won rather than failing silently.
   Late items are marked by their own snapshot SLA, and an escalated item says where it
   came from.
3. **Belgeler**: on the request, an upload area that shows a file's state honestly —
   yükleniyor, taranıyor, temiz, virüslü — and offers download only for a clean one. A
   missing-document request shows exactly which types are missing.
4. **Bildirimler**: template list and the version editor with its declared variables; the
   message log with its delivery attempts, including the ones that were suppressed and
   why.

### 2.2 Provider portal (`web/apps/provider`, first real screens)

The provider portal is _Operate with a fast-entry bias_ (PRODUCT.md). Its user stands at a
desk between patients. Four screens and no more:

1. **Uygunluk sorgusu** — the check they run before anything else, on one screen, with the
   answer and its explanations.
2. **Yeni talep** — create and submit, with the member found by name or by the identifier
   search behind a step-up.
3. **Taleplerim** — their own requests and what each is waiting for.
4. **Belge yükle** — the missing document, from the request that is waiting for it.

A provider-scoped actor sees only their own provider's data; the filter is in the server's
repository, and the screens must not add their own client-side filter that could disagree
with it.

## 3. Design notes

- **The request detail is a story, not a form.** What was asked for, what the system said,
  what a person decided, what is still missing — in that order, top to bottom. The version
  history is a list of what changed, not a diff nobody reads.
- **Return and reject look different.** A returned request is an invitation to fix
  something and its screen says what; a rejected one is finished and says why. Giving them
  the same styling is how a correctable mistake gets read as a refusal.
- **A file's state is never implied.** "Taranıyor" is a real state with a spinner, not an
  optimistic "yüklendi". A file that failed its scan says so where the download would be.
- **The worklist is dense and boring on purpose.** It is read for hours. No cards, no
  avatars, no progress rings: a table with the columns an operator sorts by.
- Everything else follows the patterns in `DESIGN.md`: ETag merge-patch forms, problem
  codes with stable Turkish messages, absent rather than disabled controls, keyset paging,
  decimal strings never parsed, nothing in browser storage.

## 4. Tests required

- Mock world: requests in every status, a work queue with an overdue item, an infected and
  a clean document, and one suppressed notification.
- Vitest flows: the transitions offered per status; return then correct then resubmit
  producing version 2; a claim lost to another actor; a missing-document request naming
  its types; an infected file offering no download.
- Playwright: a request from draft to approved in the backoffice; a provider checking
  eligibility, submitting a request and uploading the document it asks for.
- `pnpm design` clean; i18n keys under `requests.*`, `worklist.*`, `documents.*`,
  `notifications.*` complete in Turkish. **Update `DESIGN.md` in the same commit as any
  screen that settles a new pattern**, not afterwards.

## 5. Acceptance criteria

- [ ] A request can be taken from draft to approved, and to returned and back, without any
      screen ever writing a status.
- [ ] Two operators cannot own the same work item, and the loser is told who does.
- [ ] A file is downloadable only after it is scanned clean, and its state is always shown
      as it is.
- [ ] A provider can check eligibility, submit a request and upload a document without
      seeing another provider's anything.
- [ ] Lint, typecheck, unit, smoke and the Impeccable detector all clean.
