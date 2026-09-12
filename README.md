# KAPSORA

Kurumsal Hak, Fayda ve Hizmet Orkestrasyon Platformu. Kurum ve kuruluşların çalışan, üye,
müşteri, sigortalı, öğrenci ve diğer hak sahibi gruplarına sunduğu sağlık, konaklama ve benzeri
hizmetleri; hak cüzdanı, kural, sağlayıcı sözleşmesi, talep, claim, e-Belge, icmal, settlement ve
muhasebe kaydına kadar tek platformda yönetir.

- Başlangıç ve mevcut durum: [Devir notu](docs/HANDOVER.md)
- Tüm proje belgeleri: [Dokümantasyon dizini](docs/README.md)
- API sözleşmesi: [api/openapi/kapsora-v1.yaml](api/openapi/kapsora-v1.yaml)

## Teknoloji

Go 1.27 modüler monolith (api / worker / scheduler), PostgreSQL 18 (UUIDv7, composite tenant FK,
RLS), kendi kullanıcı hesaplarımızla giriş (Argon2id + opak oturum çerezi, ADR-022),
React 19 / TypeScript 5.9 / Vite 8 pnpm çalışma alanı (backoffice, sağlayıcı portalı, üye PWA),
S3 uyumlu obje deposu, transactional outbox.
**Konteyner yok:** tüm servisler yerelde ve üretimde doğal süreç olarak çalışır (ADR-021).
Ayrıntı: v1.2 bölüm 13-16, ADR-020, ADR-021.

## Hızlı başlangıç

Gereksinimler: Go 1.27+, yerel PostgreSQL 18, Node 24 + pnpm 10 (frontend).
MinIO, ClamAV ve Mailpit doğal kurulumları için
[docs/runbooks/local-native-environment.md](docs/runbooks/local-native-environment.md).
Giriş için ayrı bir sunucu gerekmez; hesaplar `go run ./cmd/seed account ...` ile açılır.

```sh
cp .env.example .env            # CHANGE_ME değerlerini kendi PostgreSQL bilgilerinizle doldurun
make tools                      # sqlc, oapi-codegen, oasdiff, golangci-lint, govulncheck
make db-init                    # kapsora_app rolü + kapsora veritabanı
make migrate-up                 # şemayı son sürüme yükseltir
make test-db                    # gerçek PostgreSQL üzerinde şema testleri
make run-api                    # http://localhost:8080/health/ready
make web-install && make web-dev # http://127.0.0.1:5173 (mock API ile; gerçek API için VITE_API_MOCK=false)
make native-install && make native-up   # MinIO, ClamAV, Mailpit doğal süreç olarak (Windows: .\scripts
ative\install.ps1 / up.ps1)
```

Windows'ta GNU make yoksa: `.\scripts\dev.ps1 <hedef>` aynı hedefleri çalıştırır.

## Yol haritası ve delegasyon

- [docs/plan/ROADMAP.md](docs/plan/ROADMAP.md): kilometre taşları, durum ve iş paketleri.
- [docs/delegation/](docs/delegation/README.md): dış geliştiriciler için el kitabı, iş paketleri (WP) ve rapor şablonu (İngilizce).

## Sık kullanılan hedefler

| Hedef | Ne yapar |
|---|---|
| `make test-unit` | Birim testleri (CI'da `-race` ile) |
| `make test-db` | Gerçek PostgreSQL 18 üzerinde migration, RLS ve bütünlük testleri |
| `make lint` | golangci-lint (depguard modül sınırlarını denetler) |
| `make openapi-generate` | OpenAPI'den Go sunucu tiplerini üretir (`api/generated`) |
| `make openapi-lint` | Spectral kural seti |
| `make sqlc` | `db/queries/*.sql` → `internal/platform/sqlcgen` |
| `make migrate-up` | Sahip rolüyle migration uygular |
| `make web-ci` | Web: tip üretimi, Prettier + ESLint, tsc, Vitest, build |
| `make web-e2e` | Playwright smoke testleri (mock API); `E2E_REAL_API=1` ile gerçek API |
| `make native-up` / `native-status` / `native-down` | MinIO + kovalar, clamd, Mailpit doğal süreçleri (`scripts/native/`) |

## Depo yapısı

```text
cmd/            api, worker, scheduler, migrate binary'leri
internal/       modüller (platform, identity, party, benefit, ... fiscal, accounting)
api/openapi     sözleşme; api/generated üretilen kod
db/migrations   forward-only SQL; db/queries sqlc; db/tests şema testleri
deploy/         systemd birimleri ve reverse proxy örnekleri (I1'de, WP-I1-06)
docs/           plan, adr, delegation, baseline-v1.2, integration, runbooks
web/            pnpm workspace: backoffice, provider, member (I1'den itibaren)
```

## Kurallar (kısa)

- Tenant tablolarında composite FK + RLS; uygulama rolü RLS'i aşamaz.
- Durum geçişi yalnız explicit komutla; submitted kayıt inplace değişmez.
- Log ve audit'te kişisel veri yok; hassas identifier şifreli + blind index.
- Her PR CI'dan geçer; migration'lar immutable, forward-only.
