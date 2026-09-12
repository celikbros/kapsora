import { useTranslation } from '@kapsora/i18n';

import type { AppKind } from './AppChooser';

/** Which app an account works in; `both` is an account that works in two of them. */
export type DemoAccountApp = AppKind | 'both';

export interface DemoAccount {
  username: string;
  /** The display name the sample data gives the account. */
  name: string;
  /** What the account does, in a few words. The app is the group it sits under. */
  role: string;
  app: DemoAccountApp;
}

/** The password every demo account is seeded with (scripts/dev.ps1 seed-demo, the mock world). */
export const DEMO_PASSWORD = 'demo parola 2026 kapsora';

/**
 * Every demo account, whichever app it belongs to. The single sign-in sends each to its own
 * app, so every sign-in screen lists them all — grouped by the app, because five accounts that
 * each repeated "Yönetim paneli ·" said the same thing five times.
 */
export const DEMO_ACCOUNTS: DemoAccount[] = [
  {
    username: 'financial.reviewer',
    name: 'Fuat Mali Değerlendirici',
    role: 'İcmal, geri ödeme, mutabakat, dışa aktarım',
    app: 'backoffice',
  },
  {
    username: 'payer.approver',
    name: 'Pınar Ödeyici Onaylayıcı',
    role: 'Ödeme mutabakatı onayı ve ödeme kaydı',
    app: 'backoffice',
  },
  {
    username: 'admin.a',
    name: 'Ayşe Yönetici',
    role: 'Kurumlar, hak sahipleri, programlar, sözleşmeler',
    app: 'backoffice',
  },
  {
    username: 'doctor.a',
    name: 'Demet Tıbbi Değerlendirici',
    role: 'Tıbbi rapor ve claim incelemesi',
    app: 'backoffice',
  },
  {
    username: 'sponsor.hr',
    name: 'Selin İnsan Kaynakları',
    role: 'Çalışan görünümü, tanı görmeden',
    app: 'backoffice',
  },
  {
    username: 'billing.a',
    name: 'Burak Faturalama',
    role: 'Hakediş, fatura, icmal, cari ekstre',
    app: 'provider',
  },
  {
    username: 'provider.a',
    name: 'Pelin Sağlayıcı',
    role: 'Uygunluk sorgusu, talep, vaka',
    app: 'provider',
  },
  {
    username: 'reservation.a',
    name: 'Rezan Rezervasyon',
    role: 'Otel kontenjanı, gelen ve giden misafir',
    app: 'provider',
  },
  {
    username: 'member.a',
    name: 'Hak sahibi',
    role: 'Kalan haklar, konaklama, geri ödeme',
    app: 'member',
  },
  {
    username: 'staff.member',
    name: 'Deniz Çalışan',
    role: 'Tıbbi değerlendirici ve aynı zamanda üye',
    app: 'both',
  },
];

/** The groups, in the order the chooser names the apps; the pair last. */
const GROUPS: readonly DemoAccountApp[] = ['backoffice', 'provider', 'member', 'both'];

/** The groups with this screen's own app first: it is the one a person is here to sign in to. */
function groupsFor(app: DemoAccountApp | undefined): readonly DemoAccountApp[] {
  if (!app) return GROUPS;
  return [app, ...GROUPS.filter((group) => group !== app)];
}

/**
 * The sample accounts under the sign-in form, each signing in with one press. An app renders it
 * only on the development server (import.meta.env.DEV), with the sample data or against a local
 * API loaded by the demo seed: a built bundle never carries it, so a real deployment cannot show
 * a list of who may sign in.
 */
export function DemoAccounts({
  accounts,
  app,
  busy = false,
  onPick,
}: {
  accounts: DemoAccount[];
  /** The app whose sign-in screen this is; its group is listed first. */
  app?: DemoAccountApp;
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
      <h3 id="demo-accounts-heading" className="text-sm font-semibold">
        {t('auth.demoTitle', { defaultValue: 'Demo hesapları' })}
      </h3>
      <p className="text-fg-muted mt-1 text-xs">
        {t('auth.demoIntro', {
          defaultValue: 'Geliştirme sunucusu. Bir hesaba dokunun, o hesapla girilir.',
        })}
      </p>
      <div className="mt-4 grid gap-4">
        {groupsFor(app).map((group) => {
          const rows = accounts.filter((account) => account.app === group);
          if (rows.length === 0) return null;
          return (
            <div key={group}>
              <h4 className="text-fg text-xs font-semibold">{t(`auth.apps.${group}`)}</h4>
              <ul className="mt-1.5 grid gap-0.5">
                {rows.map((account) => (
                  <li key={account.username}>
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => onPick(account.username)}
                      className="hover:bg-surface-sunken focus-visible:outline-focus flex w-full items-baseline justify-between gap-3 rounded-md px-2 py-1.5 text-left text-sm transition-colors focus-visible:outline-2 disabled:cursor-not-allowed disabled:opacity-60"
                    >
                      <span className="min-w-0">
                        <span className="font-medium">{account.name}</span>
                        <span className="text-fg-muted block text-xs">{account.role}</span>
                      </span>
                      <span className="text-fg-muted shrink-0 font-mono text-xs">
                        {account.username}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          );
        })}
      </div>
    </section>
  );
}
