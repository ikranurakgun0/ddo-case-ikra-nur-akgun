# EsnafDefter · Küçük işletme ön muhasebe, cari ve fatura sistemi

**Hazırlayan: İkra Nur Akgün** · DDO Yazılım Ekibi alım projesi (EsnafDefter case'i)

Çift kayıt muhasebesiyle cari hesap, fatura, tahsilat/ödeme ve stok süreçlerini yöneten, **Docker ile tek komutla ayağa kalkan**
(frontend + backend + PostgreSQL + veri üretici) bir sistem. Bu belgenin sonunda, case'in istediği
[Doğrulama Soruları](#doğrulama-soruları) cevaplanmıştır.

---

## Kurulum (tek komut)

Gereken: Docker Desktop ve Git.

```bash
git clone https://github.com/ikranurakgun0/ddo-case-ikra-nur-akgun.git
cd ddo-case-ikra-nur-akgun
cp .env.example .env
docker compose up --build
```

| Ne | Adres |
|---|---|
| **Arayüz** | http://localhost:3000 |
| API | http://localhost:8080 (örnek: `/health`) |
| **API belgeleri (Swagger/OpenAPI)** | http://localhost:3000/docs/ |
| Veritabanı | PostgreSQL 16, **host'a açılmaz**; yalnızca compose ağından erişilir |

* Veri üretici (`simulator`) **arka planda** seed eder (5 işletme, 3.000 cari, ≈50.000 hareket); bitince her 5 sn'de bir canlı olay üretir
  (arayüzde pano canlı güncellenir). Seed sürerken API ve arayüz kullanılabilir.
* Seed'in bittiği, simülatör logunda görünür: `docker compose logs simulator` → `seed tamamlandı`.
* Migrasyonlar ve seed **otomatiktir**, elle adım yoktur.

**Demo hesaplar** (parola hepsi için `Demo123!`; `N` = 1..5 işletme):

| Rol | E-posta |
|---|---|
| İşletme sahibi | `owner@isletmeN.demo` |
| Muhasebeci | `accountant@isletmeN.demo` |
| Çalışan | `employee@isletmeN.demo` |
| Müşteri (salt okunur ekstre) | `customer@isletmeN.demo` |

Giriş ekranında "Hızlı demo girişi" düğmeleri var. İşletme 5, canlı akıştan muaftır; `make verify` onun üzerinde çalışır.

### Tek komutla doğrulama

```bash
make verify      # ya da: docker compose --profile verify run --rm verify
```

Seed bitene kadar bekler, sonra her kapsam maddesini (F-1…F-7) ve zorlu görevi (T-1…T-4), kimlik doğrulamayı, yetki ihlallerini
ve idempotansı **HTTP API üzerinden uçtan uca** dener; sonunda `PASS`/`FAIL` tablosu basar, hata varsa sıfırdan farklı kodla çıkar
(≈3 sn). Diğer komutlar: `make test`, `make reset-demo`, `make load`. Windows'ta `make` yoksa karşılıkları `Makefile` ve
[TESTING.md](TESTING.md) içinde.

---

## Mimari özet

```
 Tarayıcı ──► nginx (frontend, :3000, root olmayan kullanıcı)
                 │  /            statik ES modülleri (derleme adımı yok)
                 │  /api/*   ──► Go API (:8080)  ──► PostgreSQL 16
                 │  /docs/       Swagger UI (yerel, CDN yok)              ▲
                                                                          │
              simulator ── aynı alan paketlerini kullanır ────────────────┘
              (seed + canlı akış)          verify ── yalnızca HTTP API ──► Go API
```

Go servisi katmanlıdır; **iş kuralları paketlerde, kısıt güvencesi veritabanında**:

| Paket | Sorumluluk |
|---|---|
| `internal/ledger` | Çift kayıt yevmiye: `Post`, `Reverse` (ters kayıt), dönem kapatma, mizan |
| `internal/invoicing` | KDV/iskonto hesabı (saf fonksiyon), fatura oluşturma/iptal/düzeltme, boşluksuz numara |
| `internal/payments` | Tahsilat/ödeme, FIFO mahsup, avans, silme, vade yaşlandırma, ekstre |
| `internal/stock` | Ağırlıklı ortalama maliyet (baştan oynatma), stok hareketleri, negatif stok politikası |
| `internal/api`, `httpx` | HTTP uçları, RBAC, hata biçimi, idempotans, keyset sayfalama |
| `internal/auth` | argon2id parola özeti, JWT (access) + yenileme belirteci (rotasyon) |
| `internal/sim` | Deterministik veri üretici ve canlı olay akışı |
| `internal/db/migrations` | Sürümlü şema (7 migrasyon); tetikleyiciler ve kısıtlar burada |

**Veritabanı güvenceleri** (uygulama hatası yapsa bile bozuk veri girmesin): borç=alacak (ertelenmiş constraint trigger),
değiştirilemez yevmiye/fatura/stok hareketi/denetim kaydı (UPDATE/DELETE/TRUNCATE engelli), kapalı dönem kilidi, mahsup sınırı,
fatura toplamı = satır toplamı, bileşik yabancı anahtarlarla işletmeler arası izolasyon. Paralar `bigint` kuruştur, `float` yoktur.

**Güvenlik:** her uçta sunucu tarafı rol denetimi (401/403; başka işletmenin verisi 404), argon2id, yenileme belirteci rotasyonu ve
yeniden kullanım tespiti, yapılandırılmış JSON log (parola/belirteç asla loglanmaz), katı CSP, konteynerlerde root olmayan kullanıcı,
çok aşamalı ve küçük imajlar (distroless / nginx-unprivileged), tüm yapılandırma ortam değişkenlerinden.

### Teknoloji ve gerekçeleri

* **Go 1.23 + chi + pgx**, **PostgreSQL 16**: zorunlu yığın. Standart kütüphaneye yakın, az bağımlılık.
* **Frontend: bağımsız ES modülleri (React/Vue yok), nginx.** Gerekçe: derleme/paket yöneticisi olmadığı için `docker compose up`
  sırasında kırılma riski yok; kod doğrudan okunabilir (~1.400 satır); kullanıcı verisi yalnızca `textContent` ile basıldığı için
  XSS yok ve `script-src 'self'` gibi katı bir CSP uygulanabiliyor. Bedeli: bileşen kütüphanesi/tip güvenliği yok (ayrıntı: K-8).
* **Kendi migrasyon çalıştırıcım** (`embed` + danışma kilidi); `goose` bağımlılığı yok (K-7).

Kararların tam kaydı (bağlam, seçenekler, karar, sonuç): **[DECISIONS.md](DECISIONS.md)** (9 karar).

### Kapsam

Cari hesaplar (müşteri/tedarikçi), ürün/hizmet kartları, satış/alış faturaları, tahsilat/ödeme (kasa/banka), stok hareketleri,
KDV özeti ve mizan, raporlar (yaşlandırma, stok, ekstre). Arayüz her ekranda **yükleniyor / boş / hata** durumlarını ele alır
(hata durumunda "Tekrar dene"), 390 px genişlikte kullanılabilir (tablolar kartlara dönüşür), klavye ile gezilebilir, rol bazlı menü.
Müşteri rolü yalnızca kendi salt okunur ekstresini görür.

---

## Test, CI ve ölçümler

| Ne | Sonuç |
|---|---|
| Go testleri | **121 test**, hepsi geçiyor; `api` (72), `ledger` (9), `db` (4), `sim` (3) paketleri **gerçek PostgreSQL**'e karşı çalışır (testler başına izole veritabanı) |
| `make verify` | 15 kimlik, **15/15 PASS** (seed edilmiş tam veride ≈3 sn) |
| CI (GitHub Actions) | `lint` (golangci-lint) + `test` (PostgreSQL servisiyle) + `docker` (imaj derlemesi), hepsi yeşil |
| Arayüz | Başsız Firefox ile uçtan uca senaryo (`tests/ui`), **gerçek nginx ve gerçek CSP altında 20/20** adım geçti (Swagger sayfası dahil; +1 isteğe bağlı "sunucu kapalı" adımı, `UI_DOCKER=1`) |
| k6 yük testi | 20 eşzamanlı kullanıcı, 20 sn: **5.511 fatura**, 0 hata, p95 = 161 ms; numaralar **1..5511 boşluksuz ve çiftsiz** (test kendisi doğrular) |

**Testlerin gerçekten bir şeyi koruduğunu nasıl bildim?** Kodu bilerek bozup (mutasyon) testlerin yakalayıp yakalamadığına baktım.
`make verify`'ın yakaladıkları: FIFO yerine LIFO (T-3), numara sayacı yerine `max+1` (T-2), KDV yuvarlamasının yukarı yerine aşağı olması
(T-2), müşteriye fatura yetkisi verilmesi (RBAC), tahsilat silinince mahsubun geri alınmaması (F-4, T-3), geçmişe dönük alışta maliyetin
yeniden hesaplanmaması (T-4), ürün kilidinin kaldırılması (T-4: 10 yerine 15 satış kabul edildi), kapalı dönem trigger'ının devre dışı kalması (T-1, T-2).

> Ölçüm ortamı notu: k6 ve süre ölçümlerimi **1 CPU'lu** bir ortamda (API, PostgreSQL ve k6 aynı çekirdekte) aldım; kendi makinenizde
> farklı çıkar. Tam seed bu ortamda 3 dk 7 sn sürdü (CPU sınırlı).

---

## Bilinen sınırlamalar

1. **Aynı ürüne ve aynı seri/yıla yazma serileşir.** Stok maliyeti, ürünün tüm hareketlerini yeniden oynatır; ürün satırı kilitlidir. Oynatmanın
   kendisi ucuz (200.000 hareket ≈ 15 ms CPU, `BenchmarkReplay`), asıl maliyet o kadar satırı okumaktır. Seed'de bir ürünün en fazla 293 hareketi var.
   Tek ürünün yüz binlerce hareketi olursa dönemsel kapanış (anlık görüntü) gerekir. Fatura numarası aynı seri/yıl içinde sıralıdır (çoklu seri kullanılabilir).
2. **KDV satır bazında yuvarlanır.** Aynı orandaki satırları toplayıp tek seferde yuvarlayan bir yöntemle birkaç kuruş fark çıkabilir.
   Toplamlar kendi içinde her zaman tutarlıdır.
3. **Kapalı dönemdeki alış faturası yerinde düzeltilemez**; tarihi sabit bir fiyat düzeltme belgesi yok (bkz. Soru 2).
4. **Mahsuplu fatura iptal edilemez / düzeltilemez**; önce ilgili tahsilat/ödeme silinmelidir.
5. **Fatura düzeltme** yalnızca alış faturaları için: eski iptal edilip yenisi kesilir (yeni numara tüketilir).
6. **Yaşlandırma raporunun geçmiş tarihli görünümü**, sonradan silinen ödemeleri yansıtmaz (silinen mahsuplar görünmez).
7. Parola sıfırlama, e-posta bildirimi, e-Fatura entegrasyonu ve çok para birimi kapsam dışıdır. Cari ekstre PDF çıktısı/nakit akış grafiği (bonus) yapılmadı.
8. `simulator reset` süper kullanıcı yetkisi ister ve yalnızca demo içindir; HTTP'den çağrılamaz. Compose'ta API de aynı veritabanı kullanıcısıyla çalışır
   (üretimde ayrı, yetkisi kısıtlı bir rol kullanılmalı).

---

## Doğrulama Soruları

### 1. En zorlandığın görev hangisiydi? Hangi alternatif çözümleri düşündün ve neden eledin?

**T-4 (ağırlıklı ortalama maliyet)**, özellikle "geçmişe dönük bir alış düzeltilince sonraki çıkışların maliyeti ve SMM kayıtları yeniden hesaplanmalı" kısmı.

* *Artımlı hareketli ortalama* (her harekette ortalamayı güncelle): geçmişe dönük değişiklikte sonraki bütün çıkışlar zaten yeniden hesaplanmak zorunda; artımlı durumla yeniden hesaplanan değerler arasında tutarsızlık riski yüksek. Elendi.
* *FIFO/katman maliyeti*: farklı bir değerleme yöntemi; şartname ağırlıklı ortalama istiyor. Elendi.
* *Dönemsel anlık görüntü (snapshot)*: hızlı ama anlık görüntü ile hareketler ayrışabilir, kapanış sonrası düzeltmeler karmaşıklaşır. Elendi.
* **Seçilen:** ürünün tüm hareketlerini kronolojik oynatan *saf bir fonksiyon* (`stock.Replay`). Tek doğruluk kaynağı hareketlerdir; fark kayıtları (SMM düzeltme yevmiyesi, denetim izi) bu fonksiyonun çıktısından türetilir. Saf olduğu için 5.000 rastgele senaryoda
  "stok bitince toplam maliyet = toplam alış değeri (kuruş kaybı yok)" gibi değişmezlerle test edilebildi.

İkinci zor nokta **T-2 numaralandırması**: PostgreSQL `SEQUENCE` rollback'te numara tüketir (boşluk), `max+1` eşzamanlıda çakışır; sayaç satırı + transaction kilidi ikisini de çözdü (K-3).

### 2. Çözümünün kırıldığı ya da yetersiz kaldığı bir senaryo bul ve nedenini açıkla (bilinen sınırlama).

**Kapatılmış bir dönemdeki alış faturasını düzeltmek.** Bunu varsayım olarak değil, çalıştırarak denedim (betik `tests/limits/closed_period_correction.py`):

1. 2 Nisan'da 10 adet × 100 ₺ alış, 10 Nisan'da 5 adet satış kesilir (satışın maliyeti **500 ₺**). Nisan dönemi kapatılır.
2. Alış faturası düzeltilmek istenir (birim fiyat 120 ₺): `POST /invoices/{id}/correct`.

| Deneme | Sonuç |
|---|---|
| Düzeltme, alışın **kendi tarihiyle** (2 Nisan, kapalı dönem) | `409 period_closed`: doğru, kapalı dönem değişmez |
| Düzeltme, **açık bir tarihle** (bugün) | `409 insufficient_stock`: yeni alış 10 Nisan'daki satıştan *sonraya* düştüğü için satış stoksuz kalır; işlem geri alınır, hiçbir şey değişmez |
| Aynı, **negatif stok politikası açıkken** | `201` **ama** satışın maliyeti **500 ₺ → 0 ₺**'ye düşer ve yeni alış bugüne yazıldığı için stokta 10 adet (1.200 ₺) kalır; oysa 5 adedi Nisan'da satılmıştı |

Hareketler sonrasında: `2026-04-02 giriş (iptal) · 2026-04-10 çıkış, maliyet 0 · 2026-10-25 giriş 1.200 ₺`.

**Neden:** fatura tarihi, yevmiye kaydının tarihi ve stok hareketinin tarihi **tek bir alan** (kapalı dönem kuralı yevmiye tarihine bağlı). Bu yüzden "alışın tutarını düzelt ama stok geçmişi yerinde kalsın"
ifade edilemiyor; düzeltme ya reddediliyor ya da hareketi başka tarihe taşıyarak geçmiş satışın maliyet geçmişini bozuyor. Doğru çözüm, **tarihi sabit bir fiyat düzeltme belgesi** (alış iade/fark faturası):
hareketin tarihini ve miktarını değiştirmeden yalnızca değeri bugün tarihli bir kayıtla düzeltir ve farkı SMM'ye yazar. Bu projede yapılmadı.
Varsayılan politikada (negatif stok kapalı) kullanıcı **hata alır, veri bozulmaz**; sessiz yanlışlık yalnızca negatif stok bilerek açıldığında oluşur.

Başka sınırlamalar yukarıdaki listede (özellikle 1. ve 2. madde).

### 3. Veritabanına eklediğin dizinlerden ikisini seç: hangi sorgu için, dizinsiz/dizinli ölçüm farkı nedir?

Ölçüm veri seti: seed edilmiş tam veri (31.334 fatura, 32.007 mahsup kaydı, 244.417 yevmiye satırı). Her ölçümü 3 kez yaptım; DROP INDEX'i bir
transaction içinde yapıp `ROLLBACK` ettim. Süre `EXPLAIN (ANALYZE)` "Execution Time" değeridir.

**(a) `invoices_biz_created_idx (business_id, created_at DESC, id DESC)`: fatura listesi (yeniden eskiye, keyset sayfalama)**

| | Süre (3 ölçüm) |
|---|---|
| Dizinli | **0,10 / 0,06 / 0,06 ms** |
| Dizinsiz | 4,3 / 5,9 / 5,6 ms (≈ **70–95 kat** yavaş) |

```sql
SELECT id::text, kind, series, year, number, issue_date, due_date, status, gross_total, created_at
  FROM invoices WHERE business_id = '<isletme-id>'
 ORDER BY invoices.created_at DESC, invoices.id DESC LIMIT 26;
```
```
-- DİZİNLİ
Limit (actual time=0.023..0.035 rows=26 loops=1)
  ->  Index Scan using invoices_biz_created_idx on invoices (actual time=0.022..0.032 rows=26 loops=1)
        Index Cond: (business_id = '<isletme-id>'::uuid)
Planning Time: 0.459 ms
Execution Time: 0.060 ms

-- DİZİNSİZ
Limit (actual time=3.945..3.948 rows=26 loops=1)
  ->  Sort (actual time=3.943..3.945 rows=26 loops=1)
        Sort Key: created_at DESC, id DESC
        Sort Method: top-N heapsort  Memory: 32kB
        ->  Bitmap Heap Scan on invoices (actual time=0.163..2.499 rows=6356 loops=1)
              Recheck Cond: (business_id = '<isletme-id>'::uuid)
              Heap Blocks: exact=230
              ->  Bitmap Index Scan on invoices_biz_issue_idx (actual time=0.137..0.137 rows=6373 loops=1)
Execution Time: 3.988 ms
```
Dizinsiz hâlde işletmenin 6.356 faturası okunup sıralanıyor; dizinle yalnızca 26 satır okunuyor. Fark işletme büyüdükçe doğrusal artar.

**(b) `payment_allocations_invoice_idx (invoice_id) WHERE reversed_at IS NULL`: FIFO mahsup için bir carinin açık faturalarının hesaplanması**

334 faturalı bir cari için ödeme kaydedilirken çalışan sorgu (her faturanın mahsup toplamı bir alt sorguyla hesaplanır):

| | Süre (3 ölçüm) |
|---|---|
| Dizinli | **3,1 / 2,7 / 2,1 ms** |
| Dizinsiz | 696 / 703 / 798 ms (≈ **250–380 kat** yavaş) |

```
-- DİZİNLİ
Sort (actual time=2.759..2.777 rows=334 loops=1)
  ->  Bitmap Heap Scan on invoices i (actual time=0.126..2.591 rows=334 loops=1)
        SubPlan 1
          ->  Aggregate (actual time=0.005..0.005 rows=1 loops=334)
                ->  Index Scan using payment_allocations_invoice_idx on payment_allocations a (... rows=1 loops=334)

-- DİZİNSİZ
Incremental Sort (actual time=92.351..872.195 rows=334 loops=1)
  ->  Index Scan using invoices_contact_idx on invoices i (actual time=4.389..871.376 rows=334 loops=1)
        SubPlan 1
          ->  Aggregate (actual time=2.595..2.595 rows=1 loops=334)
                ->  Seq Scan on payment_allocations a (actual time=1.398..2.587 rows=1 loops=334)
                      Rows Removed by Filter: 32006
```
Dizinsiz hâlde alt sorgu, 334 fatura için **334 kez tüm mahsup tablosunu (32.007 satır) tarıyor**. Ödeme anında cari kilidi tutulduğu için bu süre
aynı carinin diğer işlemlerini de bekletirdi; dizin hem hızı hem eşzamanlılığı kurtarıyor. Kısmî dizin (`WHERE reversed_at IS NULL`) geri alınmış mahsupları da dışarıda bırakıp küçük kalıyor.

> Plan incelemesi sırasında ayrıca bir hata buldum (bkz. Soru 4, madde 4).

### 4. Yapay zekâ kullandıysan, reddettiğin ya da düzelttiğin bir çıktıyı (önce/sonra) göster ve neden yanlış olduğunu açıkla.

Kodun büyük kısmını **Claude** ile birlikte geliştirdim; üretilen çıktıya güvenmek yerine **kodu bilerek bozarak (mutasyon), gerçek tarayıcıda gezerek ve plan okuyarak** sınadım.
Böyle bulunan, düzelttiğim beş gerçek hata:

**(1) Yarış testi aslında kilidi test etmiyordu.** Ürün satırı kilidini koddan çıkardığımda 20 eşzamanlı satış testi **hâlâ geçiyordu**.
Sebep: bütün satışlar aynı fatura serisini kullanıyordu ve sayaç satırı kilidi istekleri zaten sıraya diziyordu; ürün kilidi hiç devreye girmiyordu.

```go
// ÖNCE: tüm istekler aynı seriyi kullanır -> sayaç kilidi her şeyi serileştirir, ürün kilidi test edilmez
code, m := s.sell(s.tok, s.cust, p, "2026-09-10", "1")

// SONRA: her istek FARKLI seriden kesilir -> yalnızca ürün kilidi aşırı satışı engeller
b := sellBody(s.cust, p, "2026-09-10", "1")
b["series"] = fmt.Sprintf("S%d", i)
code, m, _ := s.call("POST", "/invoices", b, s.tok, nil)
```
Sonuç: kilidi kaldırınca yeni test `10 başarılı + 10 reddedilen bekleniyordu: 11 / 9` diyerek **başarısız oluyor** (aşırı satış yakalandı); kilitle geçiyor.

**(2) Arama kutusunda yarış durumu (tarayıcı testinde yakalandı).** Bir cari seçildikten sonra, yazarken başlamış bir sorgu sonuçlanınca açılır liste seçilmiş olan alanın üstünde yeniden açılıyordu
(Selenium: *"another element `<li>` obscures it"*).

```js
// ÖNCE
function pick(it) {
  picked = it;
  ...
}
// SONRA: seçimde yoldaki sorguları iptal et
function pick(it) {
  clearTimeout(timer);
  seq++;            // bekleyen sorgu sonuçlanınca paint() çağırmasın
  picked = it;
  ...
}
```

**(3) Boş modal penceresi.** Hiç açık faturası olmayan bir cariye tahsilat girince sonuç penceresi **boş** kalıyordu. API, mahsup yoksa `allocations` alanını göndermiyor; arayüz `p.allocations.length` yazdığı için `TypeError` fırlatıyordu
(kayıt başarılı oluyor ama kullanıcı bunu göremiyordu).

```js
// ÖNCE
p.advance > 0 && p.allocations.length ? ... : null
// SONRA
const allocs = p.allocations || [];     // API, mahsup yoksa alanı hiç göndermez
p.advance > 0 && allocs.length ? ... : null
```
Bu senaryo artık tarayıcı testinde sabit bir adımdır ("HİÇ faturası olmayan cariye tahsilat").

**(4) `ORDER BY id` yanlış sütuna bağlanıyordu (EXPLAIN'de fark ettim).** Fatura listesi sorgusunun planında `Sort Key: created_at DESC, ((id)::text) DESC` yazıyordu.
`SELECT id::text …` sütununun çıktı adı da `id` olduğundan PostgreSQL `ORDER BY id`'yi tablo sütununa değil çıktıdaki **metin** sütununa bağlamış; sayfalama koşulu ise gerçek `uuid` sütunuyla karşılaştırıyordu. İkisi tutarsız ve dizin sıralamayı tam karşılamıyordu (planda ek bir sıralama adımı vardı).

```sql
-- ÖNCE                                   -- SONRA
ORDER BY created_at DESC, id DESC          ORDER BY invoices.created_at DESC, invoices.id DESC
```
Düzeltmeden sonra plan doğrudan `Index Scan` oldu (0,06 ms, Soru 3a). Aynı kalıp cari ve ürün listelerinde de vardı, onları da düzelttim. **Dürüst not:** ölçüm yaptığım veritabanı `C.UTF-8` harmanıyla çalıştığı için küçük harfli hex UUID'nin metin sırası bayt sırasıyla aynıdır; yani bu **gizli (latent) bir hataydı**, mevcut testler eski hâlde de geçiyordu.
Eklediğim `TestPaginationStableWithTiedSortKeys` bunu o harmanda ayırt edemez, yalnızca ileride bozulmaya karşı bir koruma.

**(5) nginx güvenlik başlıkları üretimde yoktu (gerçek nginx ile denedim).** Yapılandırmada sunucu seviyesinde CSP, `X-Frame-Options` ve `nosniff` tanımlıydı; ama önbellek için konumlara `add_header Cache-Control` ekleyince nginx üst seviyedeki `add_header`'ları **devralmayı bırakıyor**. Gerçek nginx'e `curl -I` attığımda `/`, `/js/app.js`, `/css/app.css` ve SPA yönlendirmesi **hiç güvenlik başlığı olmadan** dönüyordu; yani "katı CSP" iddiam üretimde yanlış olacaktı. (Python ile taklit ettiğim test sunucusunda bu görünmüyordu.)

```nginx
# ÖNCE: Cache-Control için add_header kullanmak, üst seviyedeki güvenlik başlıklarını bu konumda siler
location /js/ { add_header Cache-Control "no-cache"; try_files $uri =404; }

# SONRA: expires -1 aynı "Cache-Control: no-cache" başlığını üretir ve devralmayı bozmaz
location /js/ { expires -1; try_files $uri =404; }
```
Düzeltmeden sonra her konumda CSP/X-Frame-Options/nosniff/Referrer-Policy var ve tüm arayüz senaryosu **gerçek CSP altında** geçiyor.

Küçük bir ek: `golangci-lint`, `misspell` kuralının Türkçe kelimeleri ("oluşmamalı") yanlış yazım saymasını ve kullanılmayan bir fonksiyonu yakaladı; ilkini kapattım, ikincisini sildim.

### 5. "Çift kayıt muhasebe motoru" görevindeki çözümün doğru çalıştığını nasıl kanıtladın? Komutu ve çıktısını yaz.

Üç katmanda kanıt var; hepsi tek komutla yeniden üretilebilir.

**(a) Uçtan uca:** `make verify` → **T-1 PASS** (5 satış + 3 alış faturası kesilip 3'ü iptal edilir; mizan dengeli; her yevmiye kaydında borç=alacak; iptal edilen faturanın hesapları ters kayıtla sıfırlanır; kapalı döneme fatura/ters kayıt 409; kapanış geri açılamaz).

**(b) Seed edilmiş tam veri üzerinde bağımsız SQL denetimi** (50.000 hareket: faturalar, tahsilatlar, iptaller, silinen ödemeler, geçmişe dönük düzeltmeler):

```bash
docker compose exec db psql -U esnaf -d esnafdefter -c "SELECT sum(debit) AS toplam_borc, sum(credit) AS toplam_alacak, sum(debit)-sum(credit) AS fark FROM journal_lines;"
docker compose exec db psql -U esnaf -d esnafdefter -tA -c "SELECT 'dengesiz yevmiye kaydı sayısı: ' || count(*) FROM (SELECT entry_id FROM journal_lines GROUP BY entry_id HAVING sum(debit) <> sum(credit)) x;"
```
```
 toplam_borc | toplam_alacak | fark
-------------+---------------+------
 57312608656 |   57312608656 |    0
dengesiz yevmiye kaydı sayısı: 0          (103.859 kayıt, 244.417 satır)
```
*(Canlı akış açıkken toplamlar zamanla artar; **fark her zaman 0**'dır. Pristine seed için `make reset-demo` sonrası ilk `seed tamamlandı` satırındaki özet (`fingerprint`) aynı tohumla aynı çıkar.)*

**(c) Eşzamanlı karışık iş yükünde değişmez** (`TestBooksStayBalancedAfterConcurrentMixedWorkload`): 30 eşzamanlı alış/satış faturası + 15 eşzamanlı iptal sonrası mizan dengeli, `sum(debit)-sum(credit)=0`, dengesiz kayıt 0, tam 15 ters kayıt.

```bash
docker compose --profile test run --rm test      # 121 test, gerçek PostgreSQL ile
```
Kuralın uygulamayla değil **veritabanıyla** zorlandığının kanıtı: `TestDatabaseRejectsUnbalancedEntryEvenViaRawSQL` doğrudan SQL ile dengesiz kayıt yazmayı dener; commit `DD002` ile reddedilir.
Ters kayıt silme değildir (`journal_lines` UPDATE/DELETE/TRUNCATE reddedilir, `TestJournalIsImmutable`).

---

## Proje yapısı

```
cmd/api          HTTP sunucusu (+ migrate, healthcheck alt komutları)
cmd/simulator    Veri üretici (seed | reset | run | fingerprint)
cmd/verify       Uçtan uca doğrulama (make verify)
internal/        ledger, invoicing, payments, stock, sim, api, httpx, auth, db (+migrations), config, testutil
frontend/        nginx + ES modülleri (js/, css/), Swagger UI (docs/)
loadtest/        k6 betiği
tests/ui/        Selenium ile arayüz senaryosu (isteğe bağlı)
tests/limits/    Bilinen sınırlamayı yeniden üreten betik (Soru 2)
DECISIONS.md     Mimari karar kayıtları
TESTING.md       Hızlı doğrulama rehberi (demo hesaplar, kimlik tablosu, elle senaryolar)
requests.http    VS Code REST Client koleksiyonu
.github/         CI iş akışı
```
