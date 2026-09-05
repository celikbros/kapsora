---
version: 1
slug: "web-apps-provider-src-app-tsx"
primary_target: "web/apps/provider/src/App.tsx"
related_targets: ["web/apps/provider/src/main.tsx"]
---

# Provider portal — surface brief

Scope: `web/apps/provider` — the four screens a provider's desk staff use between patients. Mode: Operate. Audience: reception at a clinic, hospital or hotel on a shared desk computer; the job is to check, ask and attach without keeping a patient waiting. Content: the member (by name, or by identifier behind a step-up), the service, the date, the answer and its reasons, the provider's own requests and what each waits for. Constraints: a provider-scoped actor sees only its own organization's rows, filtered by the server; no client-side filter; decimal strings never parsed; nothing in browser storage.

## Direction contract

THESIS: The new-request form is the home page and eligibility is computed live as it fills — the answer arrives while the question is still being typed. It refuses the category default of a rail of four equal pages with the check buried behind one of them.

OWN-WORLD: DESIGN.md's world unchanged: `bg` ground, white `surface` panels, one `primary` action, tables for lists, monospace for codes and money, warning-soft for "still missing", no cards as page structure.

STORY: Staff see the form, name the member and the service, watch the right column answer, press Gönder, and hand over a reference. Waiting documents are attached from the request that names them.

FIRST VIEWPORT: Header; thin rail (Yeni talep · Taleplerim); left six columns the form (member, enrollment, service, date, quantity, amount); right four columns the live eligibility answer with its explanations; Gönder alone at the foot of the form.

FORM: Form-first sheet, 4th of 7 on the ranked list; seed bbc91507.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance.
