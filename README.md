# KAPSORA

Kurumsal Hak, Fayda ve Hizmet Orkestrasyon Platformu. Kurum ve kuruluşların çalışan, üye,
müşteri, sigortalı, öğrenci ve diğer hak sahibi gruplarına sunduğu sağlık, konaklama ve benzeri
hizmetleri; hak cüzdanı, kural, sağlayıcı sözleşmesi, talep, claim, e-Belge, icmal, settlement ve
muhasebe kaydına kadar tek platformda yönetir.

- Master plan: [docs/plan/KAPSORA_Master_Plan_v2.0.md](docs/plan/KAPSORA_Master_Plan_v2.0.md)
- Dondurulmuş baseline (v1.2 şartname, sunum, ilk şema/OpenAPI): [docs/baseline-v1.2/](docs/baseline-v1.2/)
- Mimari kararlar: [docs/adr/](docs/adr/README.md)
- API sözleşmesi: [api/openapi/kapsora-v1.yaml](api/openapi/kapsora-v1.yaml)

## Teknoloji

Go 1.27 modüler monolith (api / worker / scheduler), PostgreSQL 18 (UUIDv7, composite tenant FK,
RLS), Keycloak OIDC + Go BFF, React 19 / TypeScript, Valkey, S3 uyumlu obje deposu, transactional
outbox. Ayrıntı: v1.2 bölüm 13-16 ve ADR-020.

## Hızlı başlangıç

Gereksinimler: Go 1.27+, Docker (compose) **veya** yerel PostgreSQL 18, Node 24 (frontend, I1'den itibaren).

```sh
cp .env.example .env            # yerel değerler; .env git'e girmez
make tools                      # sqlc, oapi-codegen, oasdiff, golangci-lint, govulncheck
make dev-up                     # Docker: postgres, keycloak, valkey, minio, clamav, mailpit, otel + migration
make run-api                    # http://localhost:8080/health/ready
```

Docker olmayan makinede (yerel PostgreSQL 18):

```sh
# .env içinde KAPSORA_MIGRATE_DATABASE_URL ve KAPSORA_TEST_ADMIN_DATABASE_URL'i kendi
# sahip/superuser bağlantınıza göre düzenleyin; kapsora_app rolünü testler kendileri oluşturur.
make migrate-up
make test-db
```

Windows'ta GNU make yoksa: `.\scripts\dev.ps1 <hedef>` aynı hedefleri çalıştırır.

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

## Depo yapısı

```text
cmd/            api, worker, scheduler, migrate binary'leri
internal/       modüller (platform, identity, party, benefit, ... fiscal, accounting)
api/openapi     sözleşme; api/generated üretilen kod
db/migrations   forward-only SQL; db/queries sqlc; db/tests şema testleri
deploy/         docker, compose, helm
docs/           plan, adr, baseline-v1.2, integration, runbooks
web/            pnpm workspace: backoffice, provider, member (I1'den itibaren)
```

## Kurallar (kısa)

- Tenant tablolarında composite FK + RLS; uygulama rolü RLS'i aşamaz.
- Durum geçişi yalnız explicit komutla; submitted kayıt inplace değişmez.
- Log ve audit'te kişisel veri yok; hassas identifier şifreli + blind index.
- Her PR CI'dan geçer; migration'lar immutable, forward-only.
