# Yerel hesaplar ve demo verisi

KAPSORA kullanıcıları kendi veritabanında tutar (ADR-022): Argon2id parola özeti, opak
oturum çerezi, CSRF belirteci. Keycloak yoktur; bu doküman v1.2'deki "keycloak-local"
runbook'unun yerini alır.

## Anahtarlar

```sh
go run ./cmd/keygen
```

Çıktıdaki iki değeri `.env` içine yazın: `KAPSORA_COOKIE_SIGNING_KEY` (oturum/CSRF/imleç
imzası) ve `KAPSORA_LOCAL_MASTER_KEY` (VKN/TCKN gibi alanların zarf şifrelemesi). Üretimde
bu anahtarlar `/etc/kapsora/api.env` içindedir; değiştirilirse mevcut oturumlar düşer,
master key değişirse şifreli alanlar okunamaz (anahtar rotasyonu I3'te gelir).

## Demo tenant ve kullanıcılar

```sh
KAPSORA_SEED_DEMO_PASSWORD='demo parola 2026 kapsora' go run ./cmd/seed demo
```

| Kullanıcı | Tenant | Rol |
|---|---|---|
| `admin.a` | DEMO_A | TENANT_ADMIN |
| `reviewer.a` | DEMO_A | REVIEWER |
| `provider.a` | DEMO_A | PROVIDER_STAFF |
| `admin.b` | DEMO_B | TENANT_ADMIN |
| `both.ab` | DEMO_A, DEMO_B | TENANT_ADMIN / AUDITOR |

`seed demo` idempotenttir; mevcut hesapların parolasını değiştirmez. Parola verilmezse
rastgele üretilir ve bir kez ekrana yazılır. Aynı kullanıcı adları web mock'unda da
vardır (12+ karakter herhangi bir parola ile).

## Tek hesap açma

```sh
go run ./cmd/seed account ayse.yilmaz "Ayşe Yılmaz" ayse@example.invalid
```

Parola bir kez yazılır; ilk girişte değiştirilmesi istenir (`mustChangePassword`).
Tenant üyeliği ve rol ataması I2'deki yönetim ekranlarıyla gelir; o zamana kadar
`seed demo` içindeki örüntü (`internal/identity/application/roles.go`) kullanılır.

## Demo verisini sıfırlama

```sh
psql "$KAPSORA_MIGRATE_DATABASE_URL" -c "delete from iam.session; delete from iam.credential;"
go run ./cmd/seed demo
```

Tam sıfırlama için veritabanını düşürüp `make db-init && make migrate-up` yeterlidir.

## Kilitlenen hesap

10 başarısız girişte hesap 15 dakika kilitlenir (`ACCOUNT_LOCKED`). Beklemek yerine:

```sh
psql "$KAPSORA_MIGRATE_DATABASE_URL" -c "update iam.credential set failed_attempts = 0, locked_until = null where actor_id = (select id from iam.actor where identity_issuer = 'kapsora' and identity_subject = 'admin.a');"
```
