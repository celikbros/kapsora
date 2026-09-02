# Mimari Karar Kayıtları (ADR)

Her kritik karar bir ADR ile sabitlenir (v1.2 Ek B). Durumlar: Proposed, Accepted, Superseded.
Şablon: `ADR-000-template.md`. v1.2'de gerekçesi verilmiş kararlar "Accepted" olarak açılır ve
ilgili v1.2 bölümüne referans verir; plan v2.0 ile gelen kararlar ayrıntılı yazılır.

| ADR | Başlık | Durum | Kaynak |
|---|---|---|---|
| 001 | Modüler monolith ve servis ayrıştırma kriterleri | Accepted | v1.2 13.1, 32.3 |
| 002 | Dedicated production varsayılanı ve tenant-aware veri modeli | Accepted | v1.2 16.3, 33.3 |
| 003 | PostgreSQL 18, UUIDv7 ve SQL-first repository | Accepted | v1.2 14.1, 16.1; ADR-020 ile güncellendi |
| 004 | Composite tenant FK + RLS | Accepted | v1.2 16.10, 16.11 |
| 005 | Keycloak OIDC ve BFF session | Accepted | v1.2 15.3, 18 |
| 006 | Plan/contract/rule versioning ve effective dating | Accepted | v1.2 11.3, 11.5 |
| 007 | Entitlement immutable ledger | Accepted | v1.2 11.4 |
| 008 | Accommodation daily inventory and hold model | Accepted | v1.2 11.11 |
| 009 | Transactional outbox ve at-least-once worker | Accepted | v1.2 23 |
| 010 | S3 quarantine/secure document pipeline | Accepted | v1.2 22 |
| 011 | PostgreSQL search; Elasticsearch ertelendi | Accepted | v1.2 25 |
| 012 | Versioned decision table + constrained CEL | Accepted | v1.2 9.9, 11.7 |
| 013 | Payment/insurance/travel legal boundary | Accepted | v1.2 4.3, 20.4 |
| 014 | Health data segregation and access audit | Accepted | v1.2 11.10, 19.3 |
| 015 | API idempotency/ETag/error conventions | Accepted | v1.2 14.5, 14.6, 17.1 |
| 016 | Database migration expand/contract policy | Accepted | v1.2 34.3 |
| 017 | Mali entegrasyon kapsamı: GİB e-Belge ve muhasebe | Accepted | Plan v2.0 bölüm 2 |
| 018 | Özel entegratör adapter modeli; ilk adapter İşNet Nettefatura | Accepted | Plan v2.0 2.2, 2.12 |
| 019 | Muhasebe kanonik modeli ve ERP adapter'ları | Accepted | Plan v2.0 2.7 |
| 020 | I0 araç ve sürüm sapmaları (Go 1.27, decimal, XML doğrulama, TS istemci, CI servis konteyneri) | Accepted | 02.09.2026 onayı |
