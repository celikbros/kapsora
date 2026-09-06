---
version: 1
slug: "web-apps-provider-src-app-tsx"
primary_target: "web/apps/provider/src/App.tsx"
related_targets: ["web/apps/provider/src/main.tsx"]
---

Scope: `web/apps/provider` — the desk staff's screens between patients, and from M5 the clinic's health work: the case, the treatment report, the admission, the claim. Mode: Operate. Audience: reception and billing at a clinic or hospital on a shared desk computer; a doctor's assistant recording what happened. Content: the member (by name), the service, the date, the answer and its reasons, the provider's own requests; the case as a story (encounters, the primary diagnosis by name and code, documents), the report's services and limits, the stay's segments and reconciliation, the claim's lines and each line's decision. Constraints: a provider-scoped actor sees only its own organization's rows, filtered by the server; no client-side filter; decimal strings never parsed and never computed; nothing in browser storage; a clinical field arrives or it does not, the screen never hides one.

## Direction contract

THESIS: The new-request form is the home page with eligibility computed live, and the case is the spine of everything clinical: report, admission and claim are opened from the case and read as its chapters. It refuses a rail of six equal pages where the case is one list among others.

OWN-WORLD: DESIGN.md's world unchanged: `bg` ground, white `surface` panels, one `primary` action, tables that edit in place, monospace for codes and money, warning-soft for "still missing", no cards as page structure.

STORY: Staff see the form, name the member and the service, watch the right column answer, press Gönder. Later they open the case, record the encounter and its diagnosis — the sensitivity shows the moment a code is chosen — attach the report, admit, extend only when nothing is undecided, discharge and read the exact figures, and the billing desk turns the case into a claim whose prices the server names.

FIRST VIEWPORT: Header; rail (Yeni talep · Taleplerim · Vakalar · Claim'ler); the form-first home unchanged. On a case: the reference and the member with status beside it; the encounters table first, diagnosis in it; then the report, the admission and the claim as sections with their own status word and one action each.

FORM: Form-first sheet, 4th of 7, seed bbc91507; M5 extends it with the case spine, no new roll.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance.
