# Yerel doğal geliştirme ortamı (konteynersiz)

Durum: başlangıç sürümü. Tam otomasyon ve tüm servisler WP-I1-06 ile gelir; bu doküman
bugün çalışan asgari yolu anlatır.

## Gereksinimler

| Bileşen | Sürüm | Not |
|---|---|---|
| Go | 1.27+ | `go version` |
| PostgreSQL | 18.x | Windows kurulumu `C:\Program Files\PostgreSQL\18`, servis `postgresql-x64-18`, port 5432 |
| Git | 2.4x+ | LF satır sonu `.gitattributes` ile zorunlu |
| Node.js + pnpm | 24 / 10 | Yalnız frontend (WP-I1-05) |


Docker, Kubernetes, Valkey/Redis kullanılmaz (ADR-021).

## Adımlar

1. `.env.example` dosyasını `.env` olarak kopyalayın; `CHANGE_ME` değerlerini kendi
   PostgreSQL superuser bilgilerinizle doldurun. `.env` git'e girmez.
2. Rol ve veritabanı: `make db-init` veya `.\scripts\dev.ps1 db-init`
   (`kapsora_app` rolü ve `kapsora` veritabanı; mevcutsa dokunmaz).
3. Şema: `make migrate-up` (sürüm 9).
4. Doğrulama: `make test-db` (geçici `kapsora_test_*` veritabanları yaratıp siler).
5. Çalıştırma: `make run-api`, `make run-worker`, `make run-scheduler`; her biri `.env`
   içindeki `KAPSORA_DATABASE_URL` ile (`kapsora_app` rolü) bağlanır.
6. Kontrol: `curl http://localhost:8080/health/ready` → `{"status":"UP",...}`.

## Henüz elle yapılanlar (WP-I1-06 otomatikleştirecek)

- Giriş sunucusu gerekmez (ADR-022): kullanıcı hesapları KAPSORA'nın kendi
  veritabanındadır. Yerel bir hesap açmak için:

  ```sh
  go run ./cmd/keygen                     # KAPSORA_COOKIE_SIGNING_KEY üretir, .env'e yazın
  go run ./cmd/seed account demo@kapsora.test "Demo Kullanıcı" demo@kapsora.test
  ```

  Parola bir kez ekrana yazılır ve ilk kullanımda değiştirilmesi istenir.
- (Kaldırıldı) Keycloak: JDK 21 kurun, Keycloak 26.7 zip'ini `tools/keycloak` altına açın,
  `bin\kc.bat start-dev --http-port=8081 --import-realm` (realm dosyası WP-I1-01 ile gelir).
- MinIO: `minio.exe server C:\kapsora-data\minio --console-address :9001`.
- ClamAV ve Mailpit: doğal ikili dosyalar; ayrıntı WP-I1-06 raporuyla bu dokümana eklenecek.

## Sık karşılaşılan sorunlar

- `-race` Windows'ta cgo ister; yarış testleri CI'da (Linux) çalışır.
- `__Host-` çerezi HTTPS ister; yerelde `KAPSORA_COOKIE_SECURE=false` ile ad `kapsora_session`
  olur (yalnız local ortamda kabul edilir).
- Şema testleri `kapsora_app` rolünün şifresini `.env` içindeki
  `KAPSORA_TEST_APP_PASSWORD` değerine ayarlar; geliştirme URL'siyle aynı tutun.
