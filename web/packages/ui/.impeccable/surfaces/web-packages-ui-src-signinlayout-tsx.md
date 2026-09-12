---
version: 1
slug: "web-packages-ui-src-signinlayout-tsx"
primary_target: "web/packages/ui/src/SignInLayout.tsx"
related_targets: ["web/apps/backoffice/src/pages/LoginPage.tsx","web/packages/ui/src/DemoAccounts.tsx"]
---

## Direction contract

THESIS: the way in says what KAPSORA holds, in the product's own quiet voice. It refuses the
category's split-screen marketing hero: no gradient, no illustration, no slogan.

OWN-WORLD: The Ledger. Page ground plain, one raised card, 1px lines, Inter, tokens only;
primary appears once on the button and once on the "Buradasınız" mark.

STORY: a person lands here from any of the three doors, reads one sentence about what this is,
sees the three apps and which one they are at, and signs in with one account.

FIRST VIEWPORT: two columns on ≥1024px, max 64rem centred. Left: wordmark, tagline, one
sentence (≤58ch), then the three apps as a 1px-ruled definition list with the current one
marked. Right: the 24rem sign-in card, the primary action in it; the demo list below the form
on a development server, grouped by app. Below 1024px one column, the list dropped.

FORM: two-column welcome, first on the ordered list.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the
verdict, DESIGN.md, and every shipping raster carrying its provenance.
