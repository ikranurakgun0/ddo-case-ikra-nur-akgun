# Hızlı doğrulama rehberi

Ekip yöneticisi için: sistemi ayağa kaldırıp **tek komutla** her özelliği ve her zorlu görevi denemek, ardından arayüzde elle gezmek.

## 0. Ayağa kaldırma

```bash
cp .env.example .env
docker compose up --build        # arayüz http://localhost:3000 · API http://localhost:8080 · belgeler http://localhost:3000/docs/
```
Seed arka planda sürer (`docker compose logs simulator` → `seed tamamlandı`, süre ve içerik özeti ile). Bitmeden de arayüz kullanılabilir.

## 1. Demo hesaplar

Parola: **`Demo123!`**. Her işletme (`N` = 1..5) için dört rol vardır:

| Rol | E-posta | Ne görür/yapar |
|---|---|---|
| İşletme sahibi | `owner@isletmeN.demo` | her şey + ayarlar (negatif stok), dönem kapatma |
| Muhasebeci | `accountant@isletmeN.demo` | her şey (fatura iptali/düzeltme, ödeme silme, raporlar, dönem kapatma); negatif stok ayarı hariç |
| Çalışan | `employee@isletmeN.demo` | pano, fatura **kesme**, cari, ödeme, ürün; iptal/silme, rapor ve dönem kapatma **yok** |
| Müşteri | `customer@isletmeN.demo` | yalnızca **kendi salt okunur ekstresi** |

İşletme 1-4'e canlı olaylar yazılır (pano canlı güncellenir). İşletme 5 `make verify` içindir.

## 2. Tek komutla doğrulama

```bash
make verify                                          # Windows'ta make yoksa:
docker compose --profile verify run --rm verify
```
Seed bitene kadar bekler. Çıktının sonu 16 satırlık `PASS`/`FAIL` tablosu ve `16/16 geçti` olmalıdır; hata varsa sıfırdan farklı kodla çıkar. Süre ≈ 3-10 sn.

| Kimlik | Ne denenir | Elle nasıl denenir / beklenen |
|---|---|---|
| `HEALTH` | `GET /health` veritabanı dahil; bilinmeyen uç JSON hata | `curl localhost:8080/health` → `{"db":"up","status":"ok"}` |
| `AUTH` | yanlış parola/bilinmeyen kullanıcı **aynı** 401; belirteçsiz 401; refresh rotasyonu; eski belirtecin yeniden kullanımı tüm oturumları kapatır | `requests.http` → "Yanlış parola" → `401 invalid_credentials` |
| `RBAC` | müşteri fatura kesemez (403), çalışan dönem kapatamaz/iptal edemez (403), **başka işletmenin faturası/carisi 404** | arayüzde çalışan olarak giriş: Raporlar/Ayarlar menüde yok; `#/raporlar/mizan` → "erişim yetkiniz yok" |
| `F-1` | Cari hesaplar: oluştur, getir, arama, keyset sayfalama, doğrulama | Cariler → "+ Yeni cari"; arama kutusu; "Daha fazla yükle" |
| `F-2` | Ürün/hizmet kartları; tekrarlı SKU 409; KDV yalnızca %1/%10/%20 | Ürünler → "+ Yeni ürün / hizmet" |
| `F-3` | Satış/alış faturası; KDV dahil/hariç; iptal (ters kayıt) | Faturalar → "+ Yeni fatura"; satır ekle; kes; detayda "İptal et" |
| `F-4` | Tahsilat/ödeme, kasa (100) ve banka (102), silme (mahsup geri alınır) | Tahsilat / Ödeme → "+ Tahsilat al" |
| `F-5` | Stok hareketleri ve stok raporu | Ürün satırına tıkla → "Eldeki stok" + "Son stok hareketleri" |
| `F-6` | KDV özeti (iptal edilen hariç, defterle tutarlı) ve mizan (dengeli) | Raporlar → KDV özeti / Mizan ("✓ Mizan dengeli") |
| `F-7` | Yaşlandırma, ekstre (müşteri rolü salt okunur), stok raporu | Raporlar → Vade yaşlandırma; müşteri girişiyle "Hesap ekstrem" |
| `T-1` | **Çift kayıt:** mizan dengeli, her yevmiyede borç=alacak, ters kayıtla iptal, kapalı dönem kilidi | aşağıda "T-1 kanıtı" |
| `T-2` | **Boşluksuz numara** (30 eşzamanlı) ve KDV/iskonto doğruluğu | aşağıda "T-2 kanıtı" |
| `T-3` | **Tahsilat mahsubu:** FIFO, avans, silince geri alma, yaşlandırma kovaları (30/31/60/61/90/91 gün sınırları), 15 eşzamanlı tahsilat | aşağıda "T-3 kanıtı" |
| `T-4` | **Ağırlıklı ortalama maliyet:** geçmişe dönük alışla yeniden hesap, düzeltme, yetersiz stok, 15 eşzamanlı satış | aşağıda "T-4 kanıtı" |
| `IDEMPOTENCY` | `Idempotency-Key`: tekrar istek aynı sonuç; 8 eşzamanlı istek tek ödeme | `requests.http` → aynı anahtarla iki kez gönder |
| `AUDIT` | **Denetim kaydı:** fatura/ödeme/cari işlemleri kim, ne zaman, neyi, eski→yeni değerle yazılır; başarısız işlem iz bırakmaz; yalnızca sahip/muhasebeci okur | bir faturayı kes + iptal et → detayda **"Denetim izi"**; ya da `GET /audit-log?entity=invoice&entity_id=<id>` |

## 3. Kanıt komutları (zorlu görev kimliğiyle)

Tüm Go testleri (gerçek PostgreSQL ile, ≈1-2 dk):

```bash
make test                                  # ya da: docker compose --profile test run --rm test
```

| Görev | Kanıtı üreten komut | Beklenen |
|---|---|---|
| **Denetim** | `TestInvoiceLifecycleIsAudited`, `TestPaymentLifecycleIsAudited`, `TestPeriodCloseAndSettingsAreAudited`, `TestFailedOperationsLeaveNoAuditTrail`, `TestAuditLogRemainsAppendOnlyAfterNewWrites` | `ok` |
| **T-1** | `make test` içindeki `TestBooksStayBalancedAfterConcurrentMixedWorkload`, `TestDatabaseRejectsUnbalancedEntryEvenViaRawSQL`, `TestJournalIsImmutable`, `TestClosedPeriodBlocksPostingAndCannotReopen` | hepsi `ok` |
| | SQL denetimi: `docker compose exec db psql -U esnaf -d esnafdefter -c "SELECT sum(debit)-sum(credit) AS fark FROM journal_lines;"` | `fark = 0` |
| **T-2** | `TestInvoiceNumbersAreGapFreeUnderConcurrency`, `TestFailedInvoiceDoesNotConsumeNumber`, `TestInvariantsRandomized` (20.000 rastgele fatura) | `ok` |
| | k6: `make load` (20 eşzamanlı kullanıcı) | sonda `numaralar 1..N boşluksuz ve çiftsiz`, eşikler ✓ |
| **T-3** | `TestFIFOAllocationPartialPayment`, `TestConcurrentPaymentsNeverOverAllocate`, `TestAgingBuckets`, `TestDeletePaymentRestoresInvoicesAndLedger` | `ok` |
| **T-4** | `TestBackdatedPurchaseRecalculatesLaterCostsAndSMM`, `TestCorrectPurchaseInvoiceRecomputesCosts`, `TestConcurrentSalesAcrossSeriesCannotOversell`, `TestConservationRandomized` | `ok` |

Tek bir testi çalıştırmak için (örn. T-4 yarış testi):
```bash
docker compose --profile test run --rm test sh -c "go test ./internal/api/ -run TestConcurrentSalesAcrossSeriesCannotOversell -count=1 -v"
```

## 4. Arayüzde elle gezme senaryoları (≈10 dk)

1. `http://localhost:3000` → **"İşletme sahibi"** demo girişi → **Pano**: 6 gösterge, "Canlı" göstergesi; ~10 sn içinde "Son faturalar" yeni kayıtla (yeşil vurgu) güncellenir.
2. **Faturalar:** yeniden eskiye; "Satış/Alış" ve "Geçerli/İptal" süzgeçleri; bir satıra tıkla → kalemler, KDV, toplamlar; "Yevmiye kayıtları" (borç=alacak); **"Denetim izi"** (kim/ne zaman/ne yaptı, eski→yeni değer).
3. **+ Yeni fatura:** cari ara (örn. "Yıldız"), kalem gir (örn. miktar `2,5`, birim fiyat `1.250,50`), "KDV dahil" kutusunu dene, kes → detay penceresi açılır. Boş bırakıp kesmeyi dene → Türkçe doğrulama hatası.
4. Aynı faturayı **iptal et** → durum "İptal", yevmiye kayıtlarında ters kayıt görünür; **Raporlar → Mizan** hâlâ "dengeli".
5. **Tahsilat / Ödeme → + Tahsilat al:** bir cari seç, tutar gir → "En eski faturadan başlayarak mahsup edildi" tablosu; fazlası "avans".
6. **Cariler →** bir cari → "Alacak ekstresi", "Faturalar", "Ödemeler" sekmeleri; "Tahsilat al".
7. **Raporlar → Vade yaşlandırma:** 0-30 / 31-60 / 61-90 / 90+ kovaları; "Borçlar" sekmesi.
8. **Ürünler → bir ürün:** eldeki stok, ortalama maliyet, son hareketler.
9. **Ayarlar** (sahip): dönem kapatma bilgisi (ilk çeyrek kapalı), negatif stok anahtarı.
10. **Yetki:** çıkış yap → "Çalışan" gir: menüde Raporlar/Ayarlar yok. "Müşteri" gir: yalnızca ekstre. Adres çubuğuna `#/faturalar` yazmayı dene → "erişim yetkiniz yok".
11. **Mobil:** tarayıcı penceresini 390 px'e daralt: menü yatay kayar, tablolar kartlara dönüşür.
12. **Hata durumu:** `docker compose stop api` → bir listeyi yenile → "Sunucuya ulaşılamıyor" + **Tekrar dene**; `docker compose start api` → "Tekrar dene".

## 5. Diğer komutlar

| Komut | Ne yapar |
|---|---|
| `make reset-demo` (Windows: `docker compose stop simulator; docker compose run --rm simulator reset; docker compose run --rm simulator seed; docker compose start simulator`) | verileri **ilk seed haline** döndürür; aynı tohum = aynı veri (özet `seed tamamlandı` satırındaki `fingerprint`) |
| `make load` | k6 yük testi (fatura kesimi, sonda numara boşluksuzluğu denetimi) |
| `python3 tests/ui/ui_test.py` | başsız Firefox ile arayüz senaryosu (Selenium; ayrıntı dosyanın başında) |
| `python3 tests/limits/closed_period_correction.py` | README Soru 2'deki bilinen sınırlamayı yeniden üretir |
| `requests.http` | VS Code REST Client ile tüm uçlar |
| `docker compose down -v` | her şeyi (veritabanı dahil) siler |
