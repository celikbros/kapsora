import { useTranslation } from '@kapsora/i18n';

export interface DemoAccount {
  username: string;
  /** The display name the sample data gives the account. */
  name: string;
  /** What the account does, in a few words. */
  role: string;
}

/**
 * The sample accounts under the sign-in form, each signing in with one press. An app renders it
 * only on the development server with the in-browser sample data: a built bundle never carries
 * it, so a real deployment cannot show a list of who may sign in.
 */
export function DemoAccounts({
  accounts,
  busy = false,
  onPick,
}: {
  accounts: DemoAccount[];
  busy?: boolean;
  onPick: (username: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <section
      aria-labelledby="demo-accounts-heading"
      className="border-line mt-6 border-t pt-4"
      data-testid="demo-accounts"
    >
      <h2 id="demo-accounts-heading" className="text-sm font-semibold">
        {t('auth.demoTitle', { defaultValue: 'Demo hesapları' })}
      </h2>
      <p className="text-fg-muted mt-1 text-xs">
        {t('auth.demoIntro', {
          defaultValue:
            'Bu kurulum örnek verilerle çalışıyor. Bir hesaba dokunun, o hesapla girilir.',
        })}
      </p>
      <ul className="mt-3 grid gap-1">
        {accounts.map((a) => (
          <li key={a.username}>
            <button
              type="button"
              disabled={busy}
              onClick={() => onPick(a.username)}
              aria-label={t('auth.demoSignInAs', {
                name: a.name,
                defaultValue: '{{name}} olarak gir',
              })}
              className="hover:bg-surface-sunken focus-visible:outline-primary flex w-full items-baseline justify-between gap-3 rounded-md px-2 py-1.5 text-left text-sm focus-visible:outline-2 disabled:cursor-not-allowed disabled:opacity-60"
            >
              <span className="min-w-0">
                <span className="font-medium">{a.name}</span>
                <span className="text-fg-muted block text-xs">{a.role}</span>
              </span>
              <span className="text-fg-muted font-mono text-xs">{a.username}</span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}
