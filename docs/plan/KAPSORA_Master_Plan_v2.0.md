# KAPSORA Master Plan v2.0 - Yeniden Başlangıç

| Alan | Değer |
|---|---|
| Durum | Onaylandı (iş sahibi, 02.09.2026). Ad: KAPSORA. I0 yerelde doğrulandı 02.09.2026 (migration, 15 şema testi, sağlık uç noktaları); Docker Compose ve CI doğrulaması Docker'lı ortam ve GitHub remote gelince. Sıradaki: I1. |
| Tarih | 02.09.2026 |
| Önceki baseline | Teknik Proje Şartnamesi v1.2 (08.08.2026) |
| Bu planın rolü | v1.2 üzerine yazılan kapsam değişikliği ve düzeltme kaydı. Çelişki halinde bu plan geçerlidir; v1.2 dosyaları dondurulmuş tarihsel baseline olarak kalır. |
| Uygulayıcı | Claude (Claude Code): kod, migration, API sözleşmesi, test, CI ve dokümantasyon |
| Karar sahibi | İş sahibi: kapsam, ürün adı, entegratör ve muhasebe programı seçimi, pilot müşteri |

## 0. Neden yeni plan

- v1.2 dokümantasyonu tamamlanmış, ancak tek satır kod, repo, migration veya CI yok. Proje Faz 0'ın başındadır.
- v1.2 iki büyük iş gereksinimini kapsam dışı bırakmıştı: GİB e-Belge entegrasyonu ve muhasebe programı entegrasyonu. Bu ikisi artık ürün kapsamındadır ve mimariye baştan işlenmelidir; sonradan eklenirse billing modülü iki kez yazılır.
- v1.2 artefaktları (doküman, SQL, OpenAPI, sunum) arasında geliştirmeyi ilk sprintte durduracak tutarsızlıklar var. Bunlar kod yazılmadan kapatılmalıdır.

## 1. v1.2'ye göre değişenler

1. **Mali entegrasyon kapsama girdi.** Yeni `fiscal` (e-Belge) ve `accounting` (muhasebe) modülleri; `billing` modülü bunlara göre yeniden tanımlandı. Ayrıntı bölüm 2.
2. **Hata ve eksik listesi** kapatılacak. Ayrıntı bölüm 3.
3. **Uygulama modeli değişti.** v1.2 paralel ekipler ve 8-12 aylık takvim varsayıyordu. v2.0'da tek geliştirici-ajan artımlı teslim yapar: her artım çalışan, test edilmiş ve belgelenmiş yazılımla biter. Takvim yerine sıra ve bitti kriteri verilir. Ayrıntı bölüm 5.
4. **v1.2 dosyaları değiştirilmez.** Düzeltmeler yeni repo yapısında (`db/migrations`, `api/openapi`, `docs/architecture`) uygulanır. Kök dizindeki v1.2 dosyaları `docs/baseline-v1.2/` altına taşınır.
5. **Ürün adı** repo kurulumundan önce kesinleşmelidir. Ayrıntı bölüm 4.

## 2. Mali entegrasyon: GİB e-Belge ve muhasebe

### 2.1 İptal edilen v1.2 kararları

| v1.2 yeri | v1.2 ifadesi | v2.0 kararı |
|---|---|---|
| 4.2 MVP Kapsam Dışı | "GİB e-fatura üretimi veya özel entegratörlük" | e-Belge **alma, doğrulama, eşleştirme ve yanıtlama** MVP'de. e-Belge **düzenleme** MVP+1'de. KAPSORA özel entegratör olmaz; lisanslı bir özel entegratörü adapter ile kullanır. |
| 27.2 ERP, E-Fatura ve Muhasebe | "E-fatura oluşturma MVP kapsamında değildir" | Yukarıdaki gibi. Muhasebe entegrasyonu tek yönlü "referans saklama" değil, çift yönlü kayıt ve mutabakattır. |
| 10.9 Fatura ve İcmal, madde 2 | Sağlayıcı fatura metadata'sını elle girer | Sağlayıcının kestiği e-Fatura GİB üzerinden tenant'a ulaşır; KAPSORA otomatik alır ve claim/batch ile eşleştirir. Elle giriş yalnız kağıt fatura ve e-Arşiv fallback'i olarak kalır. |
| 16.8 `billing.invoice` | "external invoice metadata" | `billing.invoice` ya bir `fiscal.edocument`'tan üretilir ya da elle girilir; kaynağı `source` alanı belirtir. |
| 9.14 ve 12.5 Claim durumları | INVOICED, BATCHED, SETTLED | Korunur; `POSTED` (muhasebeye aktarıldı) ve `RECONCILED` billing/settlement tarafına eklenir. |
| 37 MVP sonrası | ERP derin entegrasyon yok | Muhasebe entegrasyonu MVP'dedir. |

### 2.2 Tasarımı belirleyen gerçekler

- e-Fatura yalnız GİB'e kayıtlı e-Fatura mükellefleri arasında çalışır. Kayıtlı olmayan alıcıya e-Arşiv fatura düzenlenir. Karşı tarafın kayıtlı olup olmadığı GİB kayıtlı kullanıcı listesinden sorgulanır.
- Belge formatı UBL-TR; her belgenin ETTN (UUID) kimliği vardır. Fatura numarası 16 karakterdir: 3 harf seri + 4 hane yıl + 9 hane sıra.
- **Ticari fatura** senaryosunda alıcı 8 gün içinde kabul veya red "uygulama yanıtı" gönderebilir. **Temel fatura** senaryosunda sistem içi red yoktur; itiraz harici yollarla (iade faturası, ihtar) yapılır. Bu ayrım KAPSORA'nın icmal inceleme kararlarını GİB'e nasıl yansıtacağını belirler.
- GİB'e doğrudan entegrasyon; GİB test ve onay süreci, mali mühür, HSM ve 7/24 erişilebilirlik yükümlülüğü gerektirir. **Karar:** MVP'de doğrudan entegrasyon yok; özel entegratör adapter'ı. Doğrudan entegrasyon ileride aynı port arkasında ayrı adapter olabilir.
- KAPSORA fatura kesen taraf değildir. Sağlayıcı tenant'a, gerektiğinde tenant hak sahibine veya sağlayıcıya fatura keser. KAPSORA tenant'ın e-Belge posta kutusunu okur ve tenant adına yanıt üretir.
- Muhasebe programı defterin sahibidir. KAPSORA kaynak belgenin, kararın ve ödeme emrinin sahibidir. KAPSORA e-Defter tutmaz, beyanname üretmez, hesap planı yönetmez; yalnız eşleştirme yapar.
- 2026 düzenlemesiyle oteller ciro sınırına bakılmaksızın e-Fatura mükellefi olmaktadır. Konaklama dikeyindeki sağlayıcı faturalarının tamamına yakını e-Belge olarak gelecektir; elle fatura girişi bu dikeyde istisna olur.

### 2.3 Sınır: KAPSORA ne yapar, ne yapmaz

| Yapar | Yapmaz |
|---|---|
| Tenant adına gelen e-Fatura/e-Arşiv belgelerini çeker, imza ve şema doğrulaması yapar, ham XML'i saklar | Özel entegratör hizmeti vermez, mali mühür veya HSM işletmez |
| Belgeyi sağlayıcı, claim, icmal ile otomatik eşleştirir; eşleşmeyeni iş kuyruğuna düşürür | e-Defter, BA/BS beyanı, KDV beyannamesi üretmez |
| İcmal inceleme kararını ticari faturada kabul/red uygulama yanıtı olarak GİB'e iletir | Kısmi kabul yanıtı üretmez (GİB'de yoktur); kesinti iade faturası veya mahsup ile çözülür |
| Onaylı settlement için muhasebe fişi, alış faturası kaydı ve ödeme emri üretir; ERP'ye gönderir | Muhasebe defteri tutmaz, hesap planı sahibi değildir |
| ERP'den ödeme gerçekleşme ve banka referansını geri alır; settlement'ı kapatır | Banka ödemesini kendisi yapmaz (v1.2 4.3 korunur) |
| Günlük ve aylık cari mutabakat farklarını raporlar | Hak sahibi katkı payı için MVP'de fatura düzenlemez (MVP+1) |

### 2.4 Inbound e-Belge akışı

1. Scheduler, tenant başına entegratör posta kutusunu 5 dakikada bir sorgular; entegratör webhook destekliyorsa webhook öncelikli, polling yedek.
2. Her yeni zarf `integration.inbox_message` ile idempotent alınır (entegratör zarf ID + ETTN).
3. UBL-TR XML şema ve schematron doğrulaması, imza doğrulaması, SHA-256 hash. Ham XML `document.object` olarak obje deposuna yazılır (`fiscal/` alanı; karantina adımı XML için şema doğrulamasıdır).
4. Gönderen VKN → `directory.organization` (vergi no hash) → `directory.tenant_organization` (PROVIDER). Eşleşmeyen VKN "Tanımsız sağlayıcı" iş kalemi açar.
5. `fiscal.edocument` kaydı RECEIVED → VALIDATED; satırlar `fiscal.edocument_line`.
6. Eşleştirme motoru: sağlayıcı + fatura tarihi aralığı + toplam tutar + para birimi + belge notundaki KAPSORA claim/batch referansı (sağlayıcı sözleşmesi bu referansı yazmayı şart koşar). Sonuç AUTO_MATCHED, CANDIDATE (birden fazla aday) veya UNMATCHED.
7. AUTO_MATCHED belge `billing.invoice` üretir (source EDOCUMENT, status RECEIVED); claim'ler INVOICED olur. CANDIDATE ve UNMATCHED "Eşleşmemiş e-Belge" kuyruğuna düşer.
8. Fatura icmale girer; v1.2 12.6 batch akışı çalışır. Reviewer line-level karar verir.
9. Batch kararı kesinleşince: ticari fatura ise kabul/red uygulama yanıtı `fiscal.application_response` olarak üretilir ve outbox ile entegratöre gönderilir; 8 günlük GİB süresi work item due date'idir. Temel fatura ise yanıt yoktur; kesinti tutarı mahsup veya iade faturası bekler.
10. Settlement onayı `accounting.posting` üretir (bölüm 2.7).

### 2.5 Outbound e-Belge (MVP+1)

- Tenant'ın hak sahibine katkı payı faturası (genellikle e-Arşiv) ve sağlayıcıya yansıtma/iade faturası.
- Aynı `fiscal.edocument` tablosu, `direction = OUT`; numaralandırma `platform.number_sequence` ile seri bazlı; GİB zarf durumu `fiscal.edocument_status_event` ile izlenir.
- KAPSORA'nın kendi operasyon hizmeti faturası ürün kapsamı değildir; şirket muhasebesidir.

### 2.6 GİB doğrudan entegrasyon (uzun vade, karar gerekli)

Aynı `FiscalGateway` port'u arkasında ikinci adapter. Ön koşullar: tenant'ın veya KAPSORA'nın GİB özel entegratör/doğrudan entegrasyon izni, mali mühür, HSM, GİB test senaryolarının geçilmesi. MVP'de planlanmaz; port tasarımı bunu engellemez.

### 2.7 Muhasebe entegrasyonu

**Kanonik model** (dış sistemden bağımsız, `accounting` modülü):

| Nesne | Anlam | Kaynak |
|---|---|---|
| Counterparty | Cari kart (sağlayıcı, sponsor, hak sahibi) | `directory.tenant_organization`, `party.person` |
| AccountMapping | KAPSORA iş anahtarı → hesap planı kodu, tarih aralıklı | Tenant konfigürasyonu |
| PurchaseInvoiceRecord | Sağlayıcı faturasının ERP'ye alış faturası olarak kaydı | `billing.invoice` |
| Voucher | Muhasebe fişi (mahsup/tahsil/tediye) ve satırları | settlement, adjustment, reimbursement |
| PaymentOrder | Ödeme emri | `billing.settlement` |
| PaymentConfirmation | ERP/banka'dan dönen gerçekleşme | ERP → `billing.payment_record` |
| ReconciliationStatement | Dönemsel cari mutabakat | Günlük/aylık job |

**Adapter türleri:** REST/SOAP API adapter'ı (API'si olan ERP'ler), dosya adapter'ı (CSV/XML/XLSX; API'si olmayan paketler ve evrensel yedek), SFTP taşıyıcı. İlk somut adapter iş sahibi kararıdır; varsayılan "Generic File Adapter" ilk artımda, pilot müşterinin ERP adapter'ı I9'da.

**Yönler:**

- KAPSORA → ERP: settlement APPROVED olduğunda alış faturası kaydı + mahsup fişi + ödeme emri. Her gönderim `accounting.posting` satırıdır; idempotent, outbox ile.
- ERP → KAPSORA: ödeme gerçekleşme (tarih, tutar, banka referansı), cari bakiye, fiş onay/ret.
- Mutabakat: günlük job KAPSORA settlement toplamı ile ERP cari hareketini karşılaştırır; fark `accounting.sync_run` sonucunda alarm üretir.

**Vergi kuralları:** KDV oranı ve tevkifat e-Belgeden okunur, KAPSORA hesaplamaz. Sağlık ve konaklama için farklı KDV oranları olabilir; hesap eşlemesi hizmet kategorisine göre yapılır.

### 2.8 Yeni modüller, tablolar ve portlar

**`fiscal` şeması / modülü**

| Tablo | Önemli alanlar | Kısıt |
|---|---|---|
| `fiscal.integrator_account` | tenant, adapter_code, credential_ref (Vault), mailbox alias, status | unique tenant+adapter |
| `fiscal.edocument` | tenant, direction IN/OUT, document_type EFATURA/EARSIV, profile TEMEL/TICARI/…, ettn, invoice_number, issue_date, sender/receiver VKN hash, sender_tenant_organization_id, currency, line_extension/tax_exclusive/tax_inclusive/payable totals, xml_object_id, xml_hash, gib_status, match_status, invoice_id, status | unique tenant+ettn; unique tenant+sender+invoice_number; composite FK'ler |
| `fiscal.edocument_line` | edocument, line_no, description, quantity, unit, unit_price, line_extension, tax_rate, tax_amount, withholding | unique edocument+line |
| `fiscal.edocument_status_event` | edocument, GİB zarf/belge durum kodu, mesaj, occurred_at | append-only |
| `fiscal.application_response` | edocument, response_type ACCEPT/REJECT, reason, response_ettn, sent status | one active per edocument; ticari profil CHECK |
| `fiscal.match_candidate` | edocument, invoice/batch/claim adayı, skor, seçilme | eşleştirme izi |
| `fiscal.taxpayer_lookup` | VKN hash, is_efatura_registered, alias, checked_at, expires_at | cache; günlük yenilenir |

**`accounting` şeması / modülü**

| Tablo | Önemli alanlar | Kısıt |
|---|---|---|
| `accounting.erp_connection` | tenant, adapter_code, endpoint_ref, mapping_version, status | unique tenant+code |
| `accounting.counterparty_mapping` | tenant_organization veya person → ERP cari kodu, valid_period | overlap exclusion |
| `accounting.account_mapping` | mapping_key (program/kategori/vergi/masraf merkezi), account_code, valid_period | overlap exclusion |
| `accounting.posting` | source_type SETTLEMENT/INVOICE/PAYMENT/REIMBURSEMENT/ADJUSTMENT, source_id, voucher_type, status DRAFT/QUEUED/SENT/ACKED/FAILED, external_ref, idempotency_key, payload snapshot | unique source+idempotency; immutable after SENT |
| `accounting.posting_line` | posting, account_code, debit, credit, cost_center, description | debit=credit toplamı CHECK/deferred |
| `accounting.payment_confirmation` | posting/settlement, bank_ref, amount, date, source | unique bank_ref per connection |
| `accounting.sync_run` | connection, direction, period, counts, diff summary, status | immutable |

**Değişen mevcut tablolar:** `billing.invoice` (+source, +edocument_id, +gib_status projection), `billing.settlement` (+posting_id, +status POSTED/RECONCILED), `directory.organization` (VKN artık oluşturma anında zorunlu identifier; bölüm 3 D4).

**Portlar:** `FiscalGateway` (FetchInbox, GetStatus, SendResponse, SendDocument, LookupTaxpayer), `EDocumentMatcher`, `AccountingGateway` (PostVoucher, PostPurchaseInvoice, SendPaymentOrder, FetchPayments, FetchLedgerBalance), `PostingBuilder` (settlement → voucher lines).

### 2.9 e-Belge durum makinesi

```text
RECEIVED -> VALIDATED | INVALID
VALIDATED -> AUTO_MATCHED | CANDIDATE | UNMATCHED
CANDIDATE / UNMATCHED -> MANUALLY_MATCHED | REJECTED_UNKNOWN
*_MATCHED -> UNDER_REVIEW -> ACCEPTED | REJECTED
ACCEPTED / REJECTED (ticari) -> RESPONSE_SENT | RESPONSE_FAILED
ACCEPTED -> POSTED -> PAID -> RECONCILED
```

Terminal: INVALID, REJECTED_UNKNOWN, RECONCILED. Her geçiş `workflow.status_event`, audit ve outbox üretir; generic PATCH yoktur (v1.2 11.8 korunur).

### 2.10 Yeni permission, job ve event

Permission: `fiscal.edocument.read`, `fiscal.edocument.match`, `fiscal.response.send` (maker-checker), `fiscal.integrator.manage` (step-up), `fiscal.document.issue` (MVP+1), `accounting.mapping.manage`, `accounting.posting.read`, `accounting.posting.send`, `accounting.reconcile`.

Job: e-Belge çekme (5 dk), GİB durum yenileme (15 dk), mükellef listesi yenileme (günlük), uygulama yanıtı son gün uyarısı (günlük; 8 gün kuralı), posting dispatch (event), ERP ödeme senkronu (saatlik), cari mutabakat (günlük özet, aylık rapor).

Outbox event: `fiscal.edocument.received`, `fiscal.edocument.matched`, `fiscal.response.requested`, `accounting.posting.requested`, `accounting.payment.confirmed`.

### 2.11 Güvenlik ve uyum

- Entegratör ve ERP kimlik bilgileri Vault referansıdır; DB'de yalnız referans.
- Hak sahibine kesilen e-Arşiv belgelerinde TCKN bulunur; v1.2 16.14 şifreleme ve blind index deseni aynen uygulanır. Ham XML obje deposunda SSE-KMS ile, erişim `document.download.sensitive` ile.
- Sağlık faturasının satır açıklamaları klinik bilgi taşıyabilir; mali reviewer satır açıklamasını görür, sponsor İK görmez (v1.2 11.10).
- e-Belge ham dosyaları ve uygulama yanıtları yasal saklama süresine tabidir; `privacy.retention_schedule` sınıfı FISCAL_DOCUMENT eklenir, varsayılan silme kapalı.
- Uygulama yanıtı göndermek geri alınamaz bir dış etkidir: maker-checker ve step-up zorunludur.

### 2.12 İlk özel entegratör: İşNet Nettefatura

**Karar (02.09.2026, iş sahibi):** İlk `FiscalGateway` adapter'ı İşNet Nettefatura'dır. Diğer entegratörler zamanla aynı port arkasına eklenir.

Doğrulanan bilgiler:

- İşNet (İş Net Elektronik Bilgi Üretim Dağıtım Ticaret ve İletişim Hizmetleri A.Ş., Türkiye İş Bankası iştiraki) GİB'in yayımladığı özel entegratör listesindedir.
- Nettefatura tek portaldan e-Fatura, e-Arşiv, e-İrsaliye, e-Defter, e-SMM ve e-MM yönetir.
- Entegrasyon yolları: web servis (başvuru ve gizlilik sözleşmesi ile; ücretsiz), Excel/text şablonlarıyla dosya aktarımı, Luca ürünleriyle hazır konnektör (Luca Net Kobi Ticari, Luca Koza, Luca Mali Müşavir Paketi).
- Web servis sözleşmesi (WSDL/REST, metot listesi, hata ve zarf durum kodları, test ortamı) herkese açık yayımlanmıyor; başvuru sonrası veriliyor.

Bunun plana etkisi:

- Nettefatura **GİB tarafını** (bölüm 2.4, artım I8) karşılar. **Muhasebe defteri tarafını** (artım I9) karşılamaz; defter Luca veya başka bir muhasebe programındadır. İki artım ayrı kalır.
- Adapter GİB standardına göre tasarlanır: UBL-TR belge yapısı ve GİB entegrasyon kılavuzundaki gönderim, durum sorgu, uygulama yanıtı ve kayıtlı kullanıcı sorgu işlemleri. Nettefatura'ya özgü uç noktalar yalnız adapter paketinde kalır; domain ve port değişmez.
- Web servis erişimi gelene kadar iki geçici yol: GİB standardından türetilmiş mock adapter ve Nettefatura Excel şablonlarını okuyup yazan dosya adapter'ı.

İş sahibinin I8 öncesi yapacakları:

1. İşNet'e web servis başvurusu ve gizlilik sözleşmesi (efaturadestek@nettefatura.com, 0850 724 33 87).
2. Test veya pilot mükellef hesabı ve test ortamı erişimi.
3. Web servis dokümanının `docs/integration/isnet/` altına alınması; gizlilik sözleşmesi izin vermiyorsa ayrı özel depo.
4. Pilot tenant'ın e-Fatura posta kutusu etiketi ve sağlayıcı sözleşmelerinde ticari/temel fatura tercihi.

Muhasebe tarafı için ipucu: Luca, İşNet'ten alış/satış faturalarını çekip muhasebeleştirebiliyor. Pilot müşteri Luca kullanıyorsa I9'un ilk adapter'ı Luca dosya/konnektör tabanlı olur. Karar bölüm 7'de açık.

## 3. Düzeltme listesi (v1.2 hataları ve eksikleri)

Hepsi I0 artımında, yeni repo yapısında uygulanır.

| ID | Yer | Sorun | Düzeltme |
|---|---|---|---|
| D1 | SQL `ck_service_request_submission` | DRAFT dışı her durumda `submitted_at` zorunlu; DRAFT → CANCELLED geçişi (doküman 12.1) kısıtı ihlal eder | `submitted_at` "hiç submit edilmedi" anlamını korur: DRAFT ise NULL; CANCELLED serbest; diğer tüm durumlarda NOT NULL |
| D2 | OpenAPI | `POST /api/v1/session/switch-tenant` yok; doküman 45'te 5. sırada | `/session/switch-tenant`, `/session/step-up`, `/session/logout` eklenir |
| D3 | SQL | `Idempotency-Key` tüm POST'larda zorunlu ama `system.idempotency_record` tablosu yok | Tablo ilk migration'a eklenir; middleware I0'da |
| D4 | OpenAPI `CreateOrganizationRequest` + SQL | Global dizin vergi no hash'i ile tekilleşiyor ama istek vergi no taşımıyor; `Organization` şeması global kurum ile tenant ilişkisini karıştırıyor | İstek `identifiers[]` (VKN/TCKN/MERSIS) alır, VKN zorunlu; sunucu global dizinde eşleşeni bulur veya yaratır. Yanıt `id` = tenant_organization id, `organizationId` = global id; `relationshipStatus` ve `organizationStatus` ayrı alanlar |
| D5 | SQL `party.identifier_type.uniqueness_scope` | TENANT/SPONSOR/NONE tanımlı ama unique kısıt her zaman tenant geneli | `person_identifier.scope_key` kolonu (TENANT: boş, SPONSOR: sponsor org id, NONE: satır id); unique `(tenant_id, identifier_type, scope_key, identifier_hash)` |
| D6 | SQL | Doküman `service.service_request_version` tanımlıyor, şemada yok; kalemler request'e bağlı | Version tablosu eklenir; `service_request_item.version_id`; submitted version immutable trigger |
| D7 | SQL | 16.15 ve 21.3'te vaat edilen append-only trigger'lar, `updated_at/row_version` trigger'ı ve `audit.access_event` yok | `platform.tg_touch_row()` trigger'ı; `platform.tg_forbid_update_delete()` audit/ledger/status_event için; uygulama rolünde audit için REVOKE UPDATE/DELETE; `audit.access_event` aylık partition |
| D8 | SQL | `benefit.entitlement_reservation` yok; Faz 3 kabul kriteri 100 eşzamanlı reserve testi istiyor | Tablo I2'de; ledger RESERVE hareketi reservation'a referans verir |
| D9 | SQL `service_request_item.unit_type` | CHECK yok, diğer `unit_type` kolonlarında var | Aynı CHECK listesi; ileride `catalog.unit_type` kataloğu |
| D10 | SQL `catalog.service_category.domain_code` | CARE yok; sunum slayt 6 "Bakım" dikeyini vaat ediyor | CARE eklenir; ASSISTANCE ve CARE MVP sonrası dikeylerdir ama katalog kodu hazırdır |
| D11 | OpenAPI `/eligibility/checks` | Durum değiştirmeyen sorgu için `Idempotency-Key` zorunlu | Opsiyonel yapılır; `pricing/quotes` de aynı |
| D12 | SQL `directory.tenant_organization.tenant_code` | Tekillik yok | Partial unique `(tenant_id, tenant_code) WHERE tenant_code IS NOT NULL` |
| D13 | MD satır 3432, SQL satır 2, MD 44 ve 45 | Sürüm etiketleri v1.1 / 1.0.0 / eksiz dosya adı | Baseline dosyaları dondurulur; yeni artefaktlar tek sürüm etiketi taşır |
| D14 | MD diyagram referansları | `diagrams/` klasörü yoktu | 02.09.2026'da DOCX'ten geri çıkarıldı; `diagrams/` kökte |
| D15 | MD 16.2, ERD diyagramı, SQL | Şema adı üç yerde farklı: `organization`, `org`, `directory` | Standart: `directory`; kod modülü `organization`. ERD I0'da yeniden çizilir |
| D16 | MD satır 234 | "züeyil" yazım hatası | Yeni dokümanda "zeyil" |
| D17 | Sunum slayt 12, 16, 18, 24 | Asistans MVP'de gibi anlaşılıyor; "MVP'de e-fatura kesilmez" artık yanlış; ERP satırında GİB yok; pilot 8-12 hafta ile geliştirme 8-12 ay karışabilir | Sunum v3.2: slayt 12'ye "MVP sonrası" etiketi, slayt 16 ve 18 GİB/muhasebe kapsamına göre güncellenir, slayt 24'e geliştirme/pilot ayrımı eklenir |

## 4. Ürün adı

**Öneri: KAPSORA kalsın.** Gerekçe:

- Sektör bağımsız ve tek bir anlama kilitlenmemiş bir ad; sağlık, konaklama, GİB ve muhasebe kapsamının hiçbirini dışlamıyor. "Kapsam" çağrışımı ürünün özüyle (kimin neyi kapsadığı) örtüşüyor.
- 02.09.2026 tarihli web taramasında aynı adla yazılım, şirket veya marka bulunamadı; kapsora.com içerik sunmuyor.
- Dört doküman, cookie adı, hata URI'leri ve sunum bu adla tutarlı. Değişiklik kazanım sağlamadan maliyet üretir.

Kalan iş: TÜRKPATENT marka sorgusu ve alan adı tescili (kapsora.com, kapsora.com.tr) lansman öncesi ticari ekip tarafından yapılır; v1.2 1.1 ile uyumludur ve teknik geliştirmeyi bloke etmez.

Ad değişikliği bugün ucuz, I0 sonrasında pahalıdır. Ada bağlı kalemler: repo ve Go module path, cookie adı (`__Host-kapsora_session`), hata tipi URI'leri, Keycloak realm, obje deposu bucket adları, dokümanlar ve sunum. Yine de değiştirilmek istenirse yedek adaylar: **HAKORA** (hak + ora; KAPSORA ile aynı yapı), **ENTORA** (entitlement + ora; İngilizce pazara daha yakın), **KAPSA** (kısa ve emir kipi; genel sözcük olduğu için marka riski daha yüksek). Karar I0 başlamadan verilir; plan ve baseline tek seferde yeniden adlandırılır.

## 5. Artımlı teslim planı

Her artım şu dört şeyle biter: çalışan kod, geçen test (unit + Testcontainers PostgreSQL + RLS), güncel OpenAPI ve migration, kısa ADR/doküman güncellemesi. Boyut: S = birkaç oturum, M = bir hafta ölçeği, L = birkaç hafta ölçeği. Bunlar sıra ve göreli büyüklüktür; takvim sözü değildir.

| # | Artım | İçerik | Bitti kriteri | Boyut |
|---|---|---|---|---|
| I0 | Foundation ve düzeltmeler | Repo yapısı (v1.2 43), git, Makefile, Docker Compose (PostgreSQL 18, Keycloak, Valkey, MinIO, ClamAV, Mailpit, OTel), Go api/worker/scheduler iskeleti, migration runner, v1.2 SQL'in 7 migration'a bölünmüş ve D1-D12 düzeltilmiş hali, `system.idempotency_record`, trigger'lar, OpenAPI v1 (D2, D4, D11 dahil), oapi-codegen, sqlc, CI (lint, vet, test, migration up, OpenAPI lint, gitleaks, Trivy), ADR-001..016 iskeleti | `make dev-up` çalışır; `/health/*` yanıt verir; boş DB'ye migration up geçer; CI yeşil; RLS iki tenant testi geçer | L |
| I1 | Tenant, IAM, organizasyon | BFF OIDC login/callback/logout, opaque session, `/me`, `/tenants`, switch-tenant, step-up, actor/membership/role/grant, permission middleware, organizasyon CRUD (VKN ile global tekilleştirme), audit writer, request/trace middleware | v1.2 42.3 sprint kabul kriterlerinin tamamı | L |
| I2 | Kişi, plan, eligibility, ledger | Person, şifreli identifier + HMAC arama, relationship/membership katalogları, import staging, program/plan/version/enrollment, entitlement definition/account/ledger/reservation, eligibility API | v1.2 Faz 3 kabul kriterleri; 100 eşzamanlı reserve testinde double spend yok | L |
| I3 | Katalog, sağlayıcı, sözleşme, kural | Service catalog, code system, provider/location/capability/practitioner, contract/version/price/package/quota, rule set/version/test/publish, CEL değerlendirici, pricing quote | v1.2 Faz 4 kabul kriterleri | L |
| I4 | Talep, workflow, belge, bildirim | Service request/version/item, authorization, fulfillment, work queue/item/SLA, document upload-scan-secure pipeline, notification template/message/delivery | v1.2 Faz 5 kabul kriterleri | L |
| I5 | Sağlık dikeyi | Health case, encounter, diagnosis, medical report, inpatient stay, klinik/mali görünürlük ayrımı | v1.2 Faz 6 kabul kriterleri | L |
| I6 | Konaklama dikeyi | Property, room type, inventory day, availability, hold, booking, contribution, voucher, cancel/no-show, check-in/out, waitlist | v1.2 Faz 7 kabul kriterleri; 500 eşzamanlı hold'da oversell yok | L |
| I7 | Claim, fatura, icmal, settlement | Claim/version/line/decision/adjustment, invoice (elle giriş yolu), batch, review, settlement, payment record, reimbursement | v1.2 Faz 8 kabul kriterleri | L |
| I8 | **Fiscal: e-Belge (yeni)** | `FiscalGateway` port + GİB standardından türetilmiş mock adapter + **İşNet Nettefatura adapter'ı** (bölüm 2.12) + Nettefatura Excel şablon adapter'ı (fallback), inbox çekme, UBL-TR doğrulama, eşleştirme motoru, eşleşmemiş belge kuyruğu, uygulama yanıtı (maker-checker), GİB durum izleme, mükellef sorgulama | Mock entegratörden gelen 1.000 belgenin %95'i otomatik eşleşir; Nettefatura test ortamında gerçek belge alma ve uygulama yanıtı gönderme uçtan uca; temel fatura akışı iade/mahsup ile kapanır; 8 gün SLA work item'ı çalışır | L |
| I9 | **Accounting: muhasebe (yeni)** | Kanonik model, account/counterparty mapping UI, posting builder, Generic File Adapter, pilot ERP adapter'ı, ödeme geri senkronu, cari mutabakat job'u ve raporu | Approved settlement ERP'de alış faturası + fiş + ödeme emri olarak görünür; ERP ödeme kaydı settlement'ı kapatır; mutabakat farkı sıfır veya açıklamalı | L |
| I10 | Entegrasyon, hardening, pilot | HR/policy import adapter'ı, webhook, k6 yük testi (v1.2 32.1), DR restore drill, pentest bulguları, runbook, UAT, pilot veri migrasyonu | v1.2 48 MVP tamamlanma kriterleri + bölüm 2 mali kriterler | L |
| I11 | MVP+1 | Outbound e-Arşiv/e-Fatura düzenleme, asistans/bakım dikeyleri, push bildirim, GİB doğrudan entegrasyon değerlendirmesi | Ayrı plan | - |

Frontend: I1'den itibaren her artım kendi ekranlarını getirir (v1.2 46 sırası); backoffice önce, sağlayıcı portalı I4'ten, hak sahibi PWA'sı I6'dan itibaren.

## 6. Çalışma yöntemi

- Her artım bir branch ve PR serisidir; her PR CI'dan geçer. Onay, kullanıcı tarafından PR veya artım sonunda verilir.
- v1.2'nin normatif kuralları (composite tenant FK, RLS, ORM yok, contract-first, outbox, explicit transition, PII-free log, para için numeric) değişmeden geçerlidir.
- Karar gerektiren her sapma ADR olarak `docs/adr/` altına yazılır; ADR-017 "Mali entegrasyon kapsamı", ADR-018 "Özel entegratör adapter modeli", ADR-019 "Muhasebe kanonik modeli" ilk üç yeni ADR'dir.
- Gerçek entegratör ve ERP sandbox'ı sağlanana kadar mock adapter'larla geliştirilir; port sözleşmeleri consumer-driven contract testleriyle sabitlenir.
- Test verisi sentetiktir; gerçek VKN, TCKN veya fatura kullanılmaz.

## 7. Karar gerektiren konular (yeni)

| Konu | İş sahibinin kararı | Geliştirmeyi bloke etmeyen varsayılan |
|---|---|---|
| Ürün adı | KAPSORA kalsın mı, yeni ad mı | Öneri: kalsın (bölüm 4). İş sahibi onayı bekleniyor; I0'dan önce |
| Özel entegratör | **Karar verildi: İşNet Nettefatura** (bölüm 2.12). Açık kalan: web servis başvurusu, gizlilik sözleşmesi, test hesabı | Erişim gelene kadar mock ve Excel şablon adapter'ı |
| Muhasebe programları | Defteri tutan program hangisi (Luca? Logo? Mikro? başka?) ve sırası. Nettefatura defter tutmaz; bu karar ayrıdır | Generic File Adapter; pilot müşteri Luca kullanıyorsa Luca konnektör/dosya adapter'ı; I9'a kadar seçilmeli |
| Outbound e-Belge | Tenant kimlere fatura kesecek (hak sahibi katkı payı, sağlayıcı yansıtma) | MVP+1; port I8'de hazır |
| Ticari/temel fatura politikası | Sağlayıcı sözleşmelerinde ticari fatura zorunlu mu | Ticari fatura önerilir; temel fatura akışı da desteklenir |
| Kesinti mekanizması | İade faturası mı, sonraki icmalde mahsup mu | Tenant konfigürasyonu; varsayılan mahsup |
| GİB doğrudan entegrasyon | Uzun vadede hedefleniyor mu | Hayır; port hazır |
| Pilot müşteri ve sektör | Hangi kurum, hangi program | `DEMO_ORG` sentetik tenant (v1.2 40) |

## 8. Hemen sonraki adım

Bu plan onaylandığında I0 başlar. I0'ın ilk günü: ad kararı uygulanır, kök dizin `docs/baseline-v1.2/` ve yeni repo yapısı olarak düzenlenir, git başlatılır, v1.2 SQL bölünüp D1-D12 düzeltmeleri migration'lara işlenir, OpenAPI v1 D2/D4/D11 ile güncellenir, Docker Compose ve CI kurulur.
