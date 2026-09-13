# Tek sunucuya kurulum (Ubuntu 24.04 LTS, konteynersiz)

Boş bir sunucudan çalışan sisteme. Komutlar `root` ya da `sudo` ile çalıştırılır. Bu
kılavuz `docs/runbooks/deployment-layout.md` içindeki yerleşimi uygular; dosyalar `deploy/` altındadır.

## 0. Ön koşullar

- Ubuntu 24.04 LTS (ya da Debian 12), 4 vCPU / 8 GB RAM / 100 GB disk pilot için yeterli.
- Bir alan adı (`kapsora.example.com`) sunucunun IP'sine yönlendirilmiş; 80/443 açık.
- CI artefaktı `kapsora-binaries-<sürüm>.tar.gz` (statik ikililer + `web/` derlemeleri).
- Müşterinin SMTP aktarıcısı ve varsa S3 uyumlu deposu bilgileri.

## 1. Sistem hazırlığı

```sh
apt update && apt full-upgrade -y
apt install -y ufw curl gnupg lsb-release unattended-upgrades
ufw default deny incoming; ufw default allow outgoing
ufw allow 22/tcp; ufw allow 80,443/tcp; ufw enable
timedatectl set-timezone Europe/Istanbul
```

## 2. PostgreSQL 18

```sh
install -d /usr/share/postgresql-common/pgdg
curl -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc https://www.postgresql.org/media/keys/ACCC4CF8.asc
echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main" > /etc/apt/sources.list.d/pgdg.list
apt update && apt install -y postgresql-18
sudo -u postgres psql -c "CREATE ROLE kapsora_owner LOGIN PASSWORD '<güçlü parola>';"
sudo -u postgres psql -c "CREATE ROLE kapsora_app LOGIN NOBYPASSRLS PASSWORD '<güçlü parola>';"
sudo -u postgres psql -c "CREATE DATABASE kapsora OWNER kapsora_owner;"
```

`/etc/postgresql/18/main/postgresql.conf`: `listen_addresses = 'localhost'`,
`password_encryption = scram-sha-256`; `pg_hba.conf` içinde yalnız `scram-sha-256`.
Yönetilen PostgreSQL kullanılıyorsa bu adım yerine bağlantı bilgileri alınır.

## 3. ClamAV

```sh
apt install -y clamav clamav-daemon clamav-freshclam
sed -i 's/^#\?TCPSocket .*/TCPSocket 3310/; s/^#\?TCPAddr .*/TCPAddr 127.0.0.1/' /etc/clamav/clamd.conf
systemctl enable --now clamav-freshclam
systemctl restart clamav-daemon
```

İlk imza indirmesi birkaç dakika sürer; `systemctl status clamav-daemon` ile bekleyin.

## 4. MinIO (müşterinin S3 deposu yoksa)

```sh
useradd --system --home-dir /var/lib/minio --shell /usr/sbin/nologin minio
install -d -o minio -g minio /var/lib/minio /opt/minio/bin /etc/minio
# scripts/native/versions.json içindeki sürüm ve SHA-256:
curl -fsSL -o /opt/minio/bin/minio "<versions.json: minio.linux-amd64.url>"
echo "<versions.json: sha256>  /opt/minio/bin/minio" | sha256sum -c
chmod 0755 /opt/minio/bin/minio
printf 'MINIO_ROOT_USER=%s\nMINIO_ROOT_PASSWORD=%s\n' '<kullanıcı>' '<güçlü parola>' > /etc/minio/minio.env
chmod 0600 /etc/minio/minio.env; chown root:minio /etc/minio/minio.env
install -m 0644 deploy/systemd/minio.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now minio
```

Kovalar (`quarantine`, `secure`, `exports`, `immutable`, `fiscal`) `mc` ile açılır;
`immutable` ve `fiscal` sürümlemeli olmalıdır. Konsola (9001) yalnız SSH tüneliyle girin.

## 5. KAPSORA

```sh
tar -xzf kapsora-binaries-<sürüm>.tar.gz deploy/ 2>/dev/null || git clone https://github.com/celikbros/kapsora /tmp/kapsora
./deploy/install.sh kapsora-binaries-<sürüm>.tar.gz
/opt/kapsora/bin/keygen            # iki anahtar üretir
nano /etc/kapsora/api.env          # CHANGE_ME değerleri: DB URL (kapsora_app), anahtarlar
nano /etc/kapsora/migrate.env      # kapsora_owner URL
./deploy/install.sh kapsora-binaries-<sürüm>.tar.gz   # ikinci çalıştırma: migrate + servisler
systemctl status kapsora-api kapsora-worker kapsora-scheduler
curl -s http://127.0.0.1:8080/health/ready
```

`systemd-analyze verify /etc/systemd/system/kapsora-*.service` uyarı vermemelidir.

İlk yönetici hesabı:

```sh
sudo -u kapsora env $(grep -v '^#' /etc/kapsora/api.env | xargs) /opt/kapsora/bin/seed account admin "Sistem Yöneticisi" admin@musteri.example
```

Parola bir kez yazılır; ilk girişte değiştirilmesi zorunludur.

## 6. Reverse proxy

nginx:

```sh
apt install -y nginx certbot python3-certbot-nginx
install -m 0644 deploy/nginx/kapsora.conf /etc/nginx/sites-available/kapsora.conf
install -d /etc/nginx/snippets && install -m 0644 deploy/nginx/snippets/kapsora-proxy.conf /etc/nginx/snippets/
sed -i 's/kapsora.example.com/<alan adı>/g' /etc/nginx/sites-available/kapsora.conf
ln -sf /etc/nginx/sites-available/kapsora.conf /etc/nginx/sites-enabled/kapsora.conf
rm -f /etc/nginx/sites-enabled/default
certbot --nginx -d <alan adı>
nginx -t && systemctl reload nginx
```

Caddy tercih ediliyorsa `deploy/caddy/Caddyfile` → `/etc/caddy/Caddyfile`, alan adını
değiştirip `systemctl reload caddy`; sertifikayı Caddy kendisi alır.

Üç uygulama tek alan adında yayınlanır: yönetim paneli `/`, sağlayıcı portalı `/portal/`, üye
uygulaması `/uye/`. Tek alan adı tek oturum çerezi demektir; kişi hangisinden girerse girsin,
hesabının görevli olduğu uygulamaya yeniden parola sormadan geçer (tek giriş). Derlemeler bu
yollara göre üretilir (`VITE_BASE_PATH` ile değiştirilebilir); uygulamaları ayrı alan adlarına
koymak tek girişi bozar, çünkü oturum çerezi tek bir alan adına bağlıdır.

## 7. Doğrulama

- `https://<alan adı>/portal/` ve `https://<alan adı>/uye/` kendi giriş ekranlarını açar; bir
  sağlayıcı hesabıyla `/` üzerinden girildiğinde `/portal/`a geçilir.
- `https://<alan adı>/` backoffice giriş ekranı; `https://<alan adı>/health/ready` dışarıdan
  404 (yalnız localhost).
- Giriş → tenant → Kurumlar listesi; `journalctl -u kapsora-api -f` içinde `request_id`
  akıyor, hata yok.
- `curl -I https://<alan adı>/` başlıklarında HSTS, CSP, `X-Frame-Options: DENY`.

## 8. Güncelleme ve geri alma

```sh
./deploy/install.sh kapsora-binaries-<yeni sürüm>.tar.gz
```

Sıra: api/worker/scheduler durur → `kapsora-migrate` çalışır → servisler başlar. Şema
ileri yönlüdür (ADR-016); sorun çıkarsa düzeltme yeni bir sürümle ileriye doğru yapılır.
İkilileri geri almak gerekirse önceki artefaktla `install.sh --no-restart` ve
`systemctl restart kapsora-api kapsora-worker kapsora-scheduler`; şema geri alınmaz.

## 9. Günlük işletme

| İş | Komut |
|---|---|
| Servis durumu | `systemctl status 'kapsora-*'` |
| Günlükler | `journalctl -u kapsora-api --since -1h` |
| Migration sürümü | `sudo -u kapsora env $(grep -v '^#' /etc/kapsora/migrate.env \| xargs) /opt/kapsora/bin/migrate version` |
| Yedek | [backup-restore.md](backup-restore.md) |
| Güvenlik güncellemeleri | `unattended-upgrades` açık; PostgreSQL büyük sürüm geçişi planlı yapılır |

## Readiness after dependency failures

`GET /health/live` checks that the API process can answer HTTP. `GET /health/ready`
checks PostgreSQL, authenticated HEAD access to both configured document buckets, and
clamd PING concurrently within a two-second budget. Bucket probes do not read or write
documents. A missing/inaccessible bucket or unavailable scanner returns 503 with
`SERVICE_NOT_READY` and the failed dependency names; credentials and upstream errors are
not returned. Restoring the dependency restores readiness on the next probe.

Use readiness for traffic admission and liveness for process supervision. Do not restart
an otherwise live API repeatedly because its scanner or storage service is unavailable.
When updating an existing installation, restart the API through the operator's normal
deployment procedure before expecting the additional checks. No database migration or
new configuration variable is needed. This probe does not prove that the worker is
draining jobs; monitor outbox age and scan backlog separately during M10.

## I10-03 preparation findings

The Ubuntu execution target is still pending. These findings came from repository review,
not a completed installation or restore drill. Resolve them before using the commands
above for pilot acceptance:

- The CI `release-binaries` job currently uploads a directory containing prefixed
  `kapsora-*` executables for four commands. `deploy/install.sh` expects a tar archive
  with six unprefixed commands (`api`, `worker`, `scheduler`, `migrate`, `seed`, `keygen`)
  and the three web builds. The current CI artifact cannot be passed directly to it.
  Release packaging and a clean-target installation check remain open.
- The installer suppresses `systemd-analyze verify` failure. Run verification explicitly
  and require exit 0 before starting services; installer success does not prove unit validity.
- The generated runtime environment template omits the object-store credentials required
  by API/worker startup. Supply target-specific document storage configuration using the
  current configuration contract before startup; readiness must cover both actual buckets.
- The restore target needs the original encryption key and matching application/schema.
  The fresh-install `keygen`, seed and automatic migration steps must not be applied to
  a restored database. Follow [isolated recovery](backup-restore.md) instead.
- Avoid the historical `env $(grep ... | xargs)` examples: they do not preserve environment
  file quoting and can place secrets in process arguments. Run administrative commands via
  an operator-reviewed systemd unit with `EnvironmentFile=` and the intended `User=`;
  record the command and exit code, not its secret environment.
- The seed tool refuses production-like environments. Its demo command is not a production
  administrator provisioning procedure; do not weaken its environment check for deployment.
- CPU/memory/disk figures above are an initial sizing example. Pilot capacity and recovery
  times remain unmeasured until I10-02/I10-03 evidence exists.

Acceptance uses RPO <=5 minutes and RTO <=2 hours (baseline section 30.4), including object
and key recovery. The previous backup guide's 15-minute /4-hour targets are superseded.
