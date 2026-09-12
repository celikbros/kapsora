import {
  helpPage,
  helpTerm,
  useTranslation,
  type HelpApp,
  type HelpEntry,
  type HelpPage,
} from '@kapsora/i18n';
import { Dialog as RadixDialog, Popover as RadixPopover } from 'radix-ui';
import {
  forwardRef,
  useEffect,
  useId,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react';
import { cn } from './cn';

/*
 * In-product help, in two shapes.
 *
 * `HelpHint` is a circled question beside a label, a heading or a column: it explains what
 * the thing *is* — a term, a figure, a consequence the label does not carry. It never sits
 * on the obvious, and it never carries a refusal's reason; that stays inline in the
 * product's own sentence. `HelpDrawer` is the page explained: what it is for, what each
 * section shows, what each action does, what each status word will do next.
 *
 * Both open on press and on the keyboard. The hint also opens on hover, but only from a
 * mouse: the member app is a phone, and a finger has no hover.
 */

/** A drawn icon: a circle and a question in the one stroke weight, no glyph standing in for it. */
function QuestionIcon({ size = 18 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" fill="none" aria-hidden="true">
      <circle cx="10" cy="10" r="8" stroke="currentColor" strokeWidth="1.75" />
      <path
        d="M7.6 7.9a2.4 2.4 0 1 1 3.4 2.2c-.7.35-1 .8-1 1.5"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
      />
      <circle cx="10" cy="14.4" r="0.9" fill="currentColor" />
    </svg>
  );
}

function CloseIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 20 20" fill="none" aria-hidden="true">
      <path
        d="M5 5l10 10M15 5L5 15"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
      />
    </svg>
  );
}

const HOVER_DELAY_MS = 120;

export interface HelpHintProps {
  /** A shared term under `help:terms`; its title and body are looked up. */
  term?: string;
  /** Or the explanation written here, for a thing that is this screen's alone. */
  title?: string;
  body?: ReactNode;
  className?: string;
}

/**
 * A circled question that explains the unfamiliar where it stands. Press, Enter, Space
 * or a mouse hover opens a short explanation; Escape, the close mark or a press outside
 * closes it. A hover-opened hint leaves focus where it was; a pressed one takes it, so a
 * keyboard reader lands on the text.
 */
export function HelpHint({ term, title, body, className }: HelpHintProps) {
  const { t } = useTranslation();
  const headingId = useId();
  const [open, setOpen] = useState(false);
  // Pressed open stays open until dismissed; hover-open follows the pointer. Refs rather
  // than state: the focus hooks below read them at the moment of opening and closing, after
  // the render that would have cleared a state value.
  const pinned = useRef(false);
  const closingPinned = useRef(false);
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  const looked = term ? helpTerm(term) : undefined;
  const heading = title ?? looked?.title ?? '';
  const text = body ?? looked?.body ?? '';
  if (!heading && !text) return null;

  function enter(e: ReactPointerEvent) {
    if (e.pointerType !== 'mouse' || pinned.current) return;
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setOpen(true), HOVER_DELAY_MS);
  }
  function leave(e: ReactPointerEvent) {
    if (e.pointerType !== 'mouse' || pinned.current) return;
    window.clearTimeout(timer.current);
    setOpen(false);
  }
  function change(next: boolean) {
    if (!next) {
      closingPinned.current = pinned.current;
      pinned.current = false;
    }
    setOpen(next);
  }

  return (
    <RadixPopover.Root open={open} onOpenChange={change}>
      <RadixPopover.Trigger asChild>
        <button
          type="button"
          aria-label={`${t('help.about')}: ${heading}`}
          className={cn(
            // The glyph is 18px; the box is the 24px a finger needs, pulled in so the inline
            // rhythm stays the glyph's.
            'text-fg-muted hover:text-fg focus-visible:text-fg -m-0.5 inline-flex size-6 shrink-0 items-center justify-center rounded-full align-middle transition-colors',
            'data-[state=open]:text-fg',
            className,
          )}
          onPointerEnter={enter}
          onPointerLeave={leave}
          onClick={(e) => {
            // A press pins it open, or closes what the press had pinned. Radix would toggle
            // the same event; a hover has often opened it already, so the press decides alone.
            e.preventDefault();
            window.clearTimeout(timer.current);
            const next = !(open && pinned.current);
            pinned.current = next;
            change(next);
          }}
        >
          <QuestionIcon />
        </button>
      </RadixPopover.Trigger>
      <RadixPopover.Portal>
        <RadixPopover.Content
          side="top"
          align="start"
          sideOffset={6}
          collisionPadding={12}
          aria-labelledby={headingId}
          onPointerEnter={enter}
          onPointerLeave={leave}
          onOpenAutoFocus={(e) => {
            if (!pinned.current) e.preventDefault();
          }}
          onCloseAutoFocus={(e) => {
            // A hover that opened it never took focus, so closing must not move it either.
            if (!closingPinned.current) e.preventDefault();
          }}
          className="bg-surface-raised text-fg border-line shadow-card fade-up-in z-50 w-[min(92vw,20rem)] rounded-lg border p-4"
        >
          <div className="flex items-start justify-between gap-3">
            <p id={headingId} className="text-fg text-sm font-medium">
              {heading}
            </p>
            <RadixPopover.Close
              aria-label={t('common.close')}
              className="text-fg-muted hover:text-fg -mt-0.5 -mr-1 inline-flex size-6 shrink-0 items-center justify-center rounded"
            >
              <CloseIcon />
            </RadixPopover.Close>
          </div>
          <div className="text-fg-muted mt-1 text-sm leading-6">{text}</div>
        </RadixPopover.Content>
      </RadixPopover.Portal>
    </RadixPopover.Root>
  );
}

export interface HelpButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  /** Accessible name; the button shows only the mark. */
  label: string;
  /** Whether the drawer it opens is open now. */
  expanded: boolean;
}

/** The "?" in an app header: a ghost icon button that opens the page's help. */
export const HelpButton = forwardRef<HTMLButtonElement, HelpButtonProps>(function HelpButton(
  { label, expanded, className, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type="button"
      aria-label={label}
      aria-haspopup="dialog"
      aria-expanded={expanded}
      className={cn(
        'text-fg hover:bg-surface-sunken inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-transparent transition-colors',
        className,
      )}
      {...rest}
    >
      <QuestionIcon />
    </button>
  );
});

function EntryList({ heading, entries }: { heading: string; entries: HelpEntry[] | undefined }) {
  if (!entries || entries.length === 0) return null;
  return (
    <section className="mt-6">
      <h3 className="text-fg text-sm font-semibold">{heading}</h3>
      <dl className="divide-line border-line mt-2 divide-y border-y">
        {entries.map((entry) => (
          <div key={entry.label} className="py-2.5">
            <dt className="text-fg text-sm font-medium">{entry.label}</dt>
            <dd className="text-fg-muted mt-0.5 text-sm leading-6">{entry.body}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

/**
 * A page's help as written: purpose, then sections, actions and statuses in page order.
 * The drawer renders the purpose itself, as the dialog's description, and passes `purpose`
 * as false so the sentence is on the page once.
 */
export function PageHelpContent({ page, purpose = true }: { page: HelpPage; purpose?: boolean }) {
  const { t } = useTranslation();
  return (
    <div>
      {purpose ? <p className="text-fg text-sm leading-6">{page.purpose}</p> : null}
      <EntryList heading={t('help.sections')} entries={page.sections} />
      <EntryList heading={t('help.actions')} entries={page.actions} />
      <EntryList heading={t('help.statuses')} entries={page.statuses} />
    </div>
  );
}

export interface HelpDrawerProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The current page's help; when nothing is written for it, the app's general help. */
  page: HelpPage;
  children?: ReactNode;
}

/**
 * The page explained, in a drawer from the right: one scroll, ruled sections, closed by
 * Escape, the backdrop or its own mark. Full width on a phone.
 */
export function HelpDrawer({ open, onOpenChange, page, children }: HelpDrawerProps) {
  const { t } = useTranslation();
  return (
    <RadixDialog.Root open={open} onOpenChange={onOpenChange}>
      <RadixDialog.Portal>
        <RadixDialog.Overlay className="fixed inset-0 z-40 bg-black/40" />
        <RadixDialog.Content
          className={cn(
            'bg-surface-raised text-fg border-line slide-in-right fixed inset-y-0 right-0 z-50 flex w-[min(100vw,26rem)] flex-col border-l shadow-lg',
            'focus:outline-none',
          )}
        >
          <div className="border-line flex items-start justify-between gap-3 border-b px-5 py-4">
            <div className="min-w-0">
              <p className="text-fg-muted text-xs">{t('help.pageHelp')}</p>
              <RadixDialog.Title className="text-fg mt-0.5 text-lg font-semibold">
                {page.title}
              </RadixDialog.Title>
            </div>
            <RadixDialog.Close
              aria-label={t('common.close')}
              className="text-fg-subtle hover:text-fg hover:bg-surface-sunken -mr-1 inline-flex size-8 shrink-0 items-center justify-center rounded-md"
            >
              <CloseIcon />
            </RadixDialog.Close>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
            <RadixDialog.Description className="text-fg text-sm leading-6">
              {page.purpose}
            </RadixDialog.Description>
            <PageHelpContent page={page} purpose={false} />
            {children}
          </div>
        </RadixDialog.Content>
      </RadixDialog.Portal>
    </RadixDialog.Root>
  );
}

/**
 * The help key for a path, from a table of route patterns in the router's own spelling
 * (`/people/$personId`). The most specific pattern wins — more literal segments first, so
 * `/claims/new` beats `/claims/$claimId` — and nothing matches when no pattern does.
 */
export function matchHelpKey(
  pathname: string,
  routes: Readonly<Record<string, string>>,
): string | undefined {
  const path = pathname.replace(/\/+$/, '') || '/';
  const patterns = Object.keys(routes).sort((a, b) => literals(b) - literals(a));
  for (const pattern of patterns) {
    if (toRegExp(pattern).test(path)) return routes[pattern];
  }
  return undefined;
}

/**
 * The help to show for a path: the page's own when written, else the app's general help
 * (`app`), else the fallback the shell hands in — so the drawer always has something true
 * to say.
 */
export function pageHelpFor(
  app: HelpApp,
  pathname: string,
  routes: Readonly<Record<string, string>>,
  fallback: HelpPage,
): HelpPage {
  const key = matchHelpKey(pathname, routes);
  return (key ? helpPage(app, key) : undefined) ?? helpPage(app, 'app') ?? fallback;
}

function literals(pattern: string): number {
  return pattern.split('/').filter((segment) => segment !== '' && !segment.startsWith('$')).length;
}

function toRegExp(pattern: string): RegExp {
  const source = pattern
    .split('/')
    .map((segment) =>
      segment.startsWith('$') ? '[^/]+' : segment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'),
    )
    .join('/');
  return new RegExp(`^${source === '' ? '/' : source}$`);
}
