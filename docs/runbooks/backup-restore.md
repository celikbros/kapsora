# Yedekleme ve geri yükleme

Hedefler (v1.2 bölüm 31): RPO 15 dakika, RTO 4 saat; nokta-zamanına geri dönüş (PITR)
PostgreSQL için, sürümleme + ikinci kopya obje deposu için. Yedekler şifreli ve
uygulama sunucusundan ayrı bir yerde tutulur.

## 1. PostgreSQL: pgBackRest ile PITR

Kurulum (PGDG deposu 2. adımda eklenmişti):

```sh
apt install -y pgbackrest
install -d -o postgres -g postgres /var/lib/pgbackrest /var/log/pgbackrest
```

`/etc/pgbackrest/pgbackrest.conf`:

```ini
[global]
repo1-path=/var/lib/pgbackrest          # yerel repo; ikinci repo olarak S3 (aşağıda)
repo1-retention-full=2
repo1-retention-diff=7
repo1-cipher-type=aes-256-cbc
repo1-cipher-pass=<uzun rastgele parola>
process-max=2
log-level-console=info

[global:archive-push]
compress-level=3

[kapsora]
pg1-path=/var/lib/postgresql/18/main
pg1-port=5432
```

`postgresql.conf`:

```ini
archive_mode = on
archive_command = 'pgbackrest --stanza=kapsora archive-push %p'
wal_level = replica
max_wal_senders = 3
```

```sh
systemctl restart postgresql
sudo -u postgres pgbackrest --stanza=kapsora stanza-create
sudo -u postgres pgbackrest --stanza=kapsora check
sudo -u postgres pgbackrest --stanza=kapsora --type=full backup
```

Zamanlama (`/etc/cron.d/pgbackrest`):

```
0 2 * * 0  postgres  pgbackrest --stanza=kapsora --type=full backup
0 2 * * 1-6 postgres pgbackrest --stanza=kapsora --type=diff backup
```

WAL arşivi sürekli aktığı için RPO, arşivleme gecikmesi kadardır (`archive_timeout = 300`
ile en fazla 5 dakika). Uzak kopya: `repo2-type=s3` ile müşterinin S3'üne ya da ayrı
bir MinIO'ya (`repo2-s3-endpoint`, `repo2-s3-bucket=kapsora-backup`, kimlikler
`/etc/pgbackrest` altında 0600).

### Geri yükleme (PITR)

```sh
systemctl stop kapsora-api kapsora-worker kapsora-scheduler postgresql
sudo -u postgres pgbackrest --stanza=kapsora --delta \
  --type=time --target="2026-09-03 09:30:00+03" --target-action=promote restore
systemctl start postgresql
sudo -u postgres psql -c "select pg_is_in_recovery(), now();"
systemctl start kapsora-migrate kapsora-api kapsora-worker kapsora-scheduler
```

Geri yüklemeden sonra `iam.session` boşaltılır (tüm kullanıcılar yeniden giriş yapar)
ve `system.outbox_event` içinde tekrar gönderilecek olaylar kontrol edilir; idempotency
kayıtları sayesinde çift gönderim olmaz.

## 2. Obje deposu (MinIO)

- `immutable` ve `fiscal` kovaları sürümlemeli; silme işlemi eski sürümü korur.
- İkinci siteye kopya: `mc replicate add kapsora-local/fiscal --remote-bucket ...` (site
  replikasyonu) ya da gecelik `mc mirror --overwrite --remove` yerine `--preserve` ile
  `kapsora-backup` kovasına.
- Müşterinin S3'ü kullanılıyorsa sağlayıcının sürümleme ve çapraz bölge kopyası açılır.

Geri yükleme: `mc mirror kapsora-backup/<kova> kapsora-local/<kova>`; sürüm geri alma
`mc undo` ile.

## 3. Yapılandırma ve anahtarlar

`/etc/kapsora/*.env` ve `/etc/minio/minio.env` dosyaları parola kasasında da tutulur.
`KAPSORA_LOCAL_MASTER_KEY` kaybedilirse şifreli alanlar (VKN/TCKN vb.) geri gelmez;
veritabanı yedeği tek başına yeterli değildir. Anahtarları yedekle, yedeği şifrele.

## 4. Geri yükleme tatbikatı (üç ayda bir)

- [ ] Boş bir sunucuya `deploy-single-server.md` ile kurulum (30 dk).
- [ ] pgBackRest reposundan son tam + fark + WAL ile PITR (hedef: son 15 dk).
- [ ] `migrate version` beklenen şema sürümünü gösteriyor.
- [ ] `mc mirror` ile obje deposu; `fiscal` kovasında sürüm sayısı eşleşiyor.
- [ ] Uygulama: giriş, tenant seçimi, kurum listesi, örnek belge indirme.
- [ ] Süre ve bulgular `docs/runbooks/drills/<tarih>.md` dosyasına yazıldı; RTO 4 saat altı.
- [ ] Denetim izi: `audit.event` son kayıt zamanı yedek hedefiyle uyumlu.
