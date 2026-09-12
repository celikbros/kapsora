---
version: 1
slug: "web-packages-ui-src-help-tsx"
primary_target: "web/packages/ui/src/Help.tsx"
related_targets: ["web/packages/ui/src/FormField.tsx","web/apps/backoffice/src/layout/AppLayout.tsx","web/apps/provider/src/App.tsx","web/apps/member/src/App.tsx"]
---

## Direction contract

THESIS: help that explains the unfamiliar where it stands and says what a page is for, in
the product's own quiet voice. It refuses the category's tooltip carpet: no mark on the
obvious, no walkthrough overlay, no chat bubble in the corner.

OWN-WORLD: The Ledger. A circled question drawn in the icon stroke (1.75), muted until
hovered or focused; the popover is the raised surface with the 1px line and the card
shadow, 20rem wide, title in the label weight, body in 14px muted. The drawer is the same
surface sliding from the right, 26rem, full width below 640px, one scroll, sections ruled
by 1px lines; primary appears nowhere but focus.

STORY: a reviewer meets "mahsup" beside a figure, presses the mark, reads two sentences and
goes on; a new operator opens an unfamiliar page, presses "?" in the header, reads what it
is for and what each status will do next, closes it and works.

FIRST VIEWPORT: the mark is 18px, inline after its label, never wrapping alone. The header
button is the ghost icon button between the theme control and the account. The drawer
opens over the page with a 40% backdrop, its title the page's name, its close button top
right.

FORM: mark + popover; header button + drawer; structured page content in page order.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review,
the verdict, DESIGN.md, and captures at 390 and 1440 in both themes.
