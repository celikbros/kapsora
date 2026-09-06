---
version: 1
slug: "web-apps-backoffice-src-router-tsx"
primary_target: "web/apps/backoffice/src/router.tsx"
related_targets: ["web/apps/backoffice/src/App.tsx"]
---

Scope: `web/apps/backoffice` — the payer's review desk for the health vertical: the claim as a medical reviewer and as a financial reviewer see it, a treatment report under medical review, a person's health claims and cases as the sponsor's HR sees them, and who looked at a person's clinical data and why. Mode: Operate. Audience: two reviewers who work a queue for hours; an HR officer who checks that a claim exists and what it cost; an auditor. Content: the claim's lines with each line's decision, reason and stage; the exceptions that put it in front of a person; prices, contract amounts and the member's share; the clinical half where the caller holds it. Constraints: a field the API did not send has no place on the page — the two reviewers are served different fields, not the same fields with some hidden; money is a decimal string rendered as it arrives; a sensitive case asks for a purpose in a real dialog and records the look.

## Direction contract

THESIS: One claim page whose sections are decided by the projection the server sent: the clinical projection lays out diagnosis, description and a medical decision; the financial one lays out prices, contract amounts, the duplicate's reference and the cut. It refuses two routes for one record and it refuses hidden columns.

OWN-WORLD: DESIGN.md's world unchanged: dense tables, status beside the reference, one `primary` action, monospace money right-aligned, the request detail's sequence — what was asked, what the system said, what a person decided.

STORY: A reviewer opens the claim from the worklist, reads first why it is in front of them, decides each line with a reason in their own stage, and finishes the claim if the policy lets them. HR opens a person and sees references, dates, providers, amounts and status, and nothing that could carry a diagnosis. An auditor reads who looked and why.

FIRST VIEWPORT: Reference and status beside it, the member and the provider under the title; the exceptions panel first; the lines table laid out for the projection; the decision controls in the table's own rows; the claim-level action alone at the foot.

FORM: extension of the M4 request-detail sequence; no roll.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance.
