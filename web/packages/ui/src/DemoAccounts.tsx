import { useTranslation } from '@kapsora/i18n';

export interface DemoAccount {
  username: string;
  /** The display name the sample data gives the account. */
  name: string;
  /** What the account does, in a few words. */
  role: string;
}

/** The password every demo account is seeded with (scripts/dev.ps1 seed-demo, the mock world). */
export const DEMO_PASSWORD = 'demo parola 2026 kapsora';

/**
 * Every demo account, whichever app it belongs to. The single sign-in sends each to its own app,
 * so every sign-in screen lists them all; the part before the dot says which app that is.
 */
export const DEMO_ACCOUNTS: DemoAccount[] = [
  {
    username: 'financial.reviewer',
    name: 'Fuat Mali Değerlendirici',
    role: 'Yönetim paneli · İcmal, geri ödeme, mutabakat, dışa aktarım',
  },
  {
    username: 'payer.approver',
    name: 'Pınar Ödeyici Onaylayıcı',
    role: 'Yönetim paneli · Ödeme mutabakatı onayı ve ödeme kaydı',
  },
  {
    username: 'admin.a',
    name: 'Ayşe Yönetici',
    role: 'Yönetim paneli · Kurumlar, hak sahipleri, programlar, sözleşmeler',
  },
  {
    username: 'doctor.a',
    name: 'Demet Tıbbi Değerlendirici',
    role: 'Yönetim paneli · Tıbbi rapor ve claim incelemesi',
  },
  {
    username: 'sponsor.hr',
    name: 'Selin İnsan Kaynakları',
    role: 'Yönetim paneli · Çalışan görünümü, tanı görmeden',
  },
  {
    username: 'billing.a',
    name: 'Burak Faturalama',
    role: 'Sağlayıcı portalı · Hakediş, fatura, icmal, cari ekstre',
  },
  {
    username: 'provider.a',
    name: 'Pelin Sağlayıcı',
    role: 'Sağlayıcı portalı · Uygunluk sorgusu, talep, vaka',
  },
  {
    username: 'reservation.a',
    name: 'Rezan Rezervasyon',
    role: 'Sağlayıcı portalı · Otel kontenjanı, gelen ve giden misafir',
  },
  {
    username: 'member.a',
    name: 'Hak sahibi',
    role: 'Üye uygulaması · Kalan haklar, konaklama, geri ödeme',
  },
  {
    username: 'staff.member',
    name: 'Deniz Çalışan',
    role: 'İki uygulama · Tıbbi değerlendirici ve aynı zamanda üye',
  },
];

/**
 * The sample accounts under the sign-in form, each signing in with one press. An app renders it
 * only on the development server (import.meta.env.DEV), with the sample data or against a local
 * API loaded by the demo seed: a built bundle never carries it, so a real deployment cannot show
 * a list of who may sign in.
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
