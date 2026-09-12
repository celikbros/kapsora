# Yerel doğal geliştirme ortamı (konteynersiz)

Tüm bağımlılıklar doğal süreç olarak çalışır; Docker, Kubernetes, Valkey yoktur
(ADR-021). Giriş için ayrı bir sunucu gerekmez; hesaplar KAPSORA'nın kendi veritabanındadır
(ADR-022, bkz. [local-accounts.md](local-accounts.md)).

## 1. Gereksinimler

| Bileşen | Sürüm | Nasıl |
|---|---|---|
| Go | 1.27+ | https://go.dev/dl/ · `go version` |
| PostgreSQL | 18.x | Windows: resmi kurulum (servis `postgresql-x64-18`, port 5432). Ubuntu: PGDG deposu, `postgresql-18` |
| Node.js + pnpm | 24 / 10 | Node kurulumu; `npm install -g pnpm` (corepack yönetici hakkı isterse kullanıcı öneki yeterlidir) |
| Git | 2.4x+ | LF satır sonu `.gitattributes` ile zorunlu |
| ClamAV (Linux) | 1.x | `sudo apt install clamav clamav-daemon clamav-freshclam`; Windows'ta betik indirir |

MinIO, `mc` ve Mailpit'i `install` betiği indirir; sürüm ve SHA-256 değerleri
`scripts/native/versions.json` içinde sabittir, eşleşmeyen dosya kurulmaz.

## 2. Kurulum (bir kez)

```powershell
# Windows (PowerShell 5.1+ / 7)
.\scripts\native\install.ps1
```

```sh
# Linux / macOS
scripts/native/install.sh
```

Betik `tools/` altına (git dışı) `bin/minio`, `bin/mc`, `bin/mailpit` ve Windows'ta
`clamav/` dizinini koyar; ClamAV için `tools/clamav/clamd.conf` ve `freshclam.conf`
üretir; Go ve PostgreSQL sürümlerini denetler ve eksikse yönlendirir. Yönetici hakkı
gerekmez; sisteme hiçbir şey kurmaz.

## 3. Yapılandırma

```sh
cp .env.example .env
```

`.env` içinde:

- `KAPSORA_MIGRATE_DATABASE_URL` ve `KAPSORA_TEST_ADMIN_DATABASE_URL`: yerel PostgreSQL
  superuser bilgileri (`CHANGE_ME`).
- `KAPSORA_LOCAL_MASTER_KEY`, `KAPSORA_COOKIE_SIGNING_KEY`: `go run ./cmd/keygen` çıktısı.
- MinIO / ClamAV / Mailpit adresleri ve MinIO kök kimliği: örnek değerler yalnız yerel
  içindir; `.env` git'e girmez.
- Belge hattı (WP-I4-04): `kapsora-api` ve `kapsora-worker` nesne deposu olmadan
  başlamaz. `.env` dosyanız `.env.example`'dan eskiyse `KAPSORA_MINIO_ROOT_USER`,
  `KAPSORA_MINIO_ROOT_PASSWORD` ve `KAPSORA_DOCUMENT_*` satırlarını kopyalayın. Dosya
  gövdesi ne API'ye ne veritabanına girer: istemci `quarantine` kovasına presigned PUT ile
  yükler, worker dosyayı clamd'e akıtır, yalnız temiz dosya `secure` kovasına kopyalanır.

## 4. Veritabanı

```sh
make db-init        # kapsora_app rolü + kapsora veritabanı (varsa dokunmaz)
make migrate-up     # şema (sürüm 13)
make test-db        # geçici veritabanlarında migration/RLS testleri
go run ./cmd/seed demo   # DEMO_A / DEMO_B ve demo kullanıcılar (parola için local-accounts.md)
```

Windows'ta GNU make yoksa `.\scripts\dev.ps1 db-init` vb. aynı hedefleri çalıştırır.

Windows'ta demo verisi için `.\scripts\dev.ps1 seed-demo` kullanın: `.env`'i yeniden yükler, böylece aynı terminalde daha önce çalışan `migrate-up`'ın şema sahibi bağlantısı sızmaz ve seed uygulama rolüyle (RLS açık) çalışır; `KAPSORA_SEED_DEMO_PASSWORD` verilmemişse demo parolasını kullanır. Belge adımları için önce `native-up` gerekir.

8080 bu makinede başka bir uygulamadaysa `.env` içinde `KAPSORA_HTTP_ADDR=:8090` yazın ve web uygulamalarını `VITE_API_MOCK=false`, `VITE_API_BASE_URL=http://127.0.0.1:8090` ile başlatın.

## 5. Servisleri başlatma

```powershell
.\scripts\native\up.ps1      # MinIO + kovalar, clamd (önce freshclam), Mailpit
.\scripts\native\status.ps1  # sağlık tablosu
.\scripts\native\down.ps1    # durdur (veriler tools/data altında kalır)
```

```sh
scripts/native/up.sh && scripts/native/status.sh     # Linux; make native-up / native-status
```

`up` idempotenttir: pid dosyası canlıysa yeniden başlatmaz. İlk `freshclam` ~300 MB imza
indirir; `-SkipFreshclam` / `--skip-freshclam` ile atlanabilir (clamd o zaman imzasız
başlamaz). Linux'ta ClamAV dağıtım paketinden geliyorsa `up` yalnız
`clamav-daemon` servisinin durumunu bildirir; `/etc/clamav/clamd.conf` içinde
`TCPSocket 3310` ve `TCPAddr 127.0.0.1` olmalıdır.

| Servis | Adres | Sağlık |
|---|---|---|
| PostgreSQL | 127.0.0.1:5432 | TCP |
| MinIO | http://127.0.0.1:9000 (konsol 9001) | `/minio/health/live` |
| ClamAV clamd | tcp://127.0.0.1:3310 | `PING` → `PONG` |
| Mailpit | http://127.0.0.1:8025 (SMTP 1025) | `/livez` |
| KAPSORA API | http://127.0.0.1:8080 | `/health/ready` |

Kovalar: `quarantine`, `secure`, `exports`, `immutable`, `fiscal` (`immutable` ve `fiscal`
sürümlemeli).

## 6. Uygulamayı çalıştırma

Tek komut, tek pencere, tek adres (Windows):

```powershell
.\scripts\dev.ps1 up      # API, worker, scheduler ve üç uygulama; hepsi http://127.0.0.1:5181
.\scripts\dev.ps1 down    # arkada kalan süreçleri durdurur
```

`up` üç uygulamayı tek kapının arkasına koyar: yönetim paneli `/`, sağlayıcı portalı `/portal/`,
üye uygulaması `/uye/`. Sağlayıcı ve üye sunucuları kendi portlarında (5182, 5183) çalışır, ama
adresleri kapıdan geçer; kurulumdaki tek alan adının aynısı. Tek köken tek oturum çerezi
demektir, tek giriş de bu yüzden tek kalır. Ctrl+C hepsini durdurur.

Parçaları ayrı ayrı çalıştırmak isterseniz:

```sh
make run-api        # veya .\scripts\dev.ps1 run-api
make run-worker
make run-scheduler
make web-dev        # backoffice http://127.0.0.1:5173 (mock API); gerçek API için VITE_API_MOCK=false
```

## 7. Sık karşılaşılan sorunlar

| Belirti | Neden / çözüm |
|---|---|
| `install`: "checksum mismatch" | İndirme bozuk ya da yayıncı dosyayı değiştirmiş. Yeniden deneyin; sürerse `versions.json` güncellenmeden kurmayın. |
| `up`: "something else already listens on 127.0.0.1:9000" | Başka bir MinIO/uygulama portu tutuyor; `.env` içinde `KAPSORA_MINIO_ADDR` değiştirin. |
| clamd 180 sn içinde ayağa kalkmadı | İmza yüklemesi yavaş makinelerde uzun sürer; `tools/run/clamd.err.log` bakın, bir süre sonra `status` ile yeniden kontrol edin. |
| PostgreSQL DOWN | Windows: `Get-Service postgresql-x64-18 \| Start-Service`; Linux: `sudo systemctl start postgresql`. |
| `make db-init` parola hatası | `.env` içindeki superuser URL'si yanlış; `psql "<url>" -c 'select 1'` ile doğrulayın. |
| Vite dev sunucusu 5173 kullanımda | Başka bir proje çalışıyor; `pnpm dev -- --port 5180`. |
| `.env` yüklenmiyor | Satırlar `KEY=VALUE` olmalı; tırnak ve boşluk kullanmayın. |
