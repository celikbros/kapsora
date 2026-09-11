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

## Demo tenant, kullanıcılar ve iş verisi

```sh
KAPSORA_SEED_DEMO_PASSWORD='demo parola 2026 kapsora' go run ./cmd/seed demo
```

Bu komut iki tenant'ı (DEMO_A / DEMO_B), aşağıdaki hesapları, referans veriyi ve
`scripts/demo/KAPSORA-Demo-Rehberi.html` içindeki altı senaryonun **başlangıç durumunu**
oluşturur. Her kayıt gerçek uygulama servisinden geçer: sözleşme yayınlama, icmal gönderme,
karar, mutabakat ve geri ödeme aynı kapıları kullanır.

**Ön koşul:** MinIO çalışıyor olmalı (`scripts/native/up.ps1` ya da `scripts/native/up.sh`).
Faturanın görüntüsü ve üyenin makbuzu belge deposuna yazılır; `.env` içindeki
`KAPSORA_MINIO_ROOT_USER` / `KAPSORA_MINIO_ROOT_PASSWORD` yoksa komut bunu söyleyip durur.

| Kullanıcı | Tenant | Rol | Kapsam |
|---|---|---|---|
| `admin.a` | DEMO_A | TENANT_ADMIN, PROGRAM_MANAGER | tenant |
| `reviewer.a` | DEMO_A | FINANCIAL_REVIEWER | tenant |
| `provider.a` | DEMO_A | PROVIDER_STAFF | DEMO_HOSPITAL |
| `admin.b` | DEMO_B | TENANT_ADMIN | tenant |
| `both.ab` | DEMO_A, DEMO_B | AUDITOR | tenant |
| `financial.reviewer` | DEMO_A | FINANCIAL_REVIEWER | tenant |
| `payer.approver` | DEMO_A | PAYER_APPROVER | tenant |
| `doctor.a` | DEMO_A | MEDICAL_REVIEWER | tenant |
| `sponsor.hr` | DEMO_A | SPONSOR_HR | tenant |
| `billing.a` | DEMO_A | PROVIDER_BILLING | DEMO_HOSPITAL (provider.a ile aynı kurum) |
| `reservation.a` | DEMO_A | PROVIDER_RESERVATION | DEMO_HOTEL |
| `member.a` | DEMO_A | MEMBER | PERSON (Melis Üye) |

`financial.reviewer` ile `payer.approver` bilerek ayrı hesaplardır: icmal kararını alan kişi
ile ödemeyi serbest bırakan kişinin farklı olması (maker-checker) senaryo 3'ün konusudur.

`seed demo` idempotenttir; mevcut hesapların parolasını değiştirmez ve ikinci çalıştırma
hiçbir satırı çoğaltmaz. Parola verilmezse rastgele üretilir ve bir kez ekrana yazılır. Aynı
kullanıcı adları web mock'unda da vardır (12+ karakter herhangi bir parola ile).

### Oluşan iş verisi (DEMO_A)

| Ne | Durum |
|---|---|
| Kurumlar | `DEMO_SPONSOR`, `DEMO_PAYER`, `DEMO_HOSPITAL`, `DEMO_HOTEL` — hepsinde VKN (şifreli + kör indeks) |
| Sözleşmeler | `DEMO_HEALTH` (muayene/fizyoterapi/MR, 30 gün vade), `DEMO_LODGING` (gecelik fiyat, 14 gün vade, konaklama şartları) — ikisi de ACTIVE + PUBLISHED sürüm |
| Program / plan | `DEMO_BENEFIT` / `DEMO_STANDARD`: 40.000 TL sağlık bütçesi, 20 fizyoterapi seansı, 10 konaklama gecesi |
| Üye | `member.a` planda kayıtlı, hak hesapları açık |
| Faturalar | `DEMO-0001` taslak (dağıtımı 300 TL eksik) … `DEMO-0008` |
| İcmaller | biri taslak, biri **İncelemede** (1 fatura "Karar bekliyor", 4.000 TL), üçü karara bağlanmış |
| Ödeme mutabakatı | biri **Onay bekliyor** (bugün vadeli), biri **Onaylandı** (3 gün sonra vadeli), biri **Ödendi** |
| Geri ödeme | `member.a` için **Gönderildi**, temiz makbuz, maskeli hesap |
| Konaklama | Demo Sahil Oteli, iki oda tipi, bugünden itibaren 121 gece kontenjan |
| Mutabakat | iki gün için dört koşu: iki "Dengeli", iki "Fark var" |

Senaryo 6'nın **dışa aktarım** adımı `kapsora-worker`'ı gerektirir: dosyayı render eden
süreç odur. Diğer beş senaryo yalnızca `kapsora-api` ile yürür.

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

Bu yalnızca oturumları ve parolaları siler; iş verisi yerinde kalır ve `seed demo` onu
bulup dokunmaz. İş verisini yeniden üretmek için veritabanını düşürüp
`make db-init && make migrate-up && go run ./cmd/seed demo` çalıştırın: icmal, karar ve
mutabakat kayıtları tasarım gereği append-only olduğu için tek tek silinecek şeyler değildir.

## Kilitlenen hesap

10 başarısız girişte hesap 15 dakika kilitlenir (`ACCOUNT_LOCKED`). Beklemek yerine:

```sh
psql "$KAPSORA_MIGRATE_DATABASE_URL" -c "update iam.credential set failed_attempts = 0, locked_until = null where actor_id = (select id from iam.actor where identity_issuer = 'kapsora' and identity_subject = 'admin.a');"
```
