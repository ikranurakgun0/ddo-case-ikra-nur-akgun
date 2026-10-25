# Mimari karar kayıtları

Her kayıt: **bağlam → seçenekler → karar → sonuç**. Sonuç bölümlerinde, kararın bedelini ve ölçülebilir kanıtını yazdım.
Kanıt komutları `TESTING.md` içinde görev kimliğiyle listelidir.

---

## K-1 · Para: kuruş cinsinden tam sayı (`bigint`), ondalık yok

**Bağlam.** Mali verilerde kuruş hatası kabul edilemez. KDV ve iskonto hesabı, yuvarlamanın toplamı bozma riskini taşır.

**Seçenekler.**
1. `float64` → ikili kayan nokta; 0,1 + 0,2 sorunu. Şartname de yasaklıyor.
2. `numeric(18,2)` + Go'da `decimal` kütüphanesi → doğru ama bağımlılık ekler ve her işlemde dönüşüm gerekir.
3. **Tam sayı kuruş** (`bigint`), miktar için de "binde bir birim" tam sayısı (`quantity_milli`: 2,5 adet = 2500).

**Karar.** Seçenek 3. Çarpma-bölme ara değerleri taşmasın diye `math/big` ile yapılır; yarım-yukarı yuvarlama tek yerde (`mulDivRound`).

**Sonuç.** Hesap saf bir fonksiyon (`invoicing.Calculate`) ve rastgele 20.000 faturada "net+KDV=brüt, satır toplamı=fatura toplamı, iskonto payları toplamı=genel iskonto" değişmezleri test ediliyor.
**Bedel:** istemci tarafında kuruşa çeviri gerekir (arayüz `parseMoney` ile float kullanmadan çevirir); API kullanıcısı `10050` gibi değerlere alışmak zorundadır.

---

## K-2 · Çift kayıt kuralları uygulamada değil **veritabanında** zorlanır

**Bağlam.** "Mizan her zaman denk olmalı", "kayıt geriye dönük değişmemeli", "kapalı dönem kilitli" kuralları, uygulama koduna güvenilerek korunursa tek bir hata (veya doğrudan SQL) bütün defteri bozar.

**Seçenekler.**
1. Yalnızca Go kodunda doğrulama.
2. Go doğrulaması + **veritabanı güvencesi**: `DEFERRABLE INITIALLY DEFERRED` constraint trigger (borç=alacak), `BEFORE UPDATE/DELETE/TRUNCATE` trigger'ları (değiştirilemezlik), dönem kilidi trigger'ı, bileşik yabancı anahtarlar (başka işletmenin hesabı bağlanamaz).
3. Olay kaynağı (event sourcing) → bu kapsam için fazla.

**Karar.** Seçenek 2. Deferred trigger sayesinde kayıt ve satırları **aynı transaction içinde, sırayla** eklenir; denge commit anında bir kez denetlenir.

**Sonuç.** Uygulamayı atlayıp doğrudan SQL ile dengesiz kayıt yazmak commit'te `DD002` ile reddedilir (test: `TestDatabaseRejectsUnbalancedEntryEvenViaRawSQL`). Kapalı dönem trigger'ını bilerek devre dışı bıraktığımda `make verify` **T-1 ve T-2'yi FAIL** etti, yani bu kural gerçekten test ediliyor.
**Bedel:** iş kuralının bir kısmı SQL'de yaşar; migrasyonlarla birlikte gözden geçirilmesi gerekir. Özel hata kodları (`DD001..DD005`) ile Go tarafında anlamlı hatalara çevrilir.

---

## K-3 · Boşluksuz fatura numarası: sayaç satırı + transaction kilidi

**Bağlam.** Seri/yıl bazlı ardışık numaralar eşzamanlı üretimde bile boşluksuz ve çiftsiz olmalı. Başarısız bir fatura numarayı "yakmamalı".

**Seçenekler.**
1. PostgreSQL `SEQUENCE` → hızlı, ama **geri alınan transaction numarayı tüketir** (boşluk oluşur). Mevzuat açısından uygun değil.
2. `SELECT max(number)+1` → eşzamanlıda çift numara / benzersizlik hatası. (Denedim: eşzamanlı testte 500 hatası verdi.)
3. Danışma kilidi (`pg_advisory_xact_lock`) + `max+1` → çalışır ama sayaç tablosundan daha az görünür.
4. `SERIALIZABLE` + yeniden deneme → doğru ama yüksek çakışmada pahalı.
5. **`invoice_counters` tablosunda `INSERT … ON CONFLICT DO UPDATE … RETURNING`**: satır kilidi transaction sonuna kadar tutulur; aynı seri/yıl için faturalar sıraya girer; rollback numarayı geri verir.

**Karar.** Seçenek 5. Numara, fatura satırlarından hemen önce (ürün kilitlerinden sonra) alınır; kilit süresi kısa kalır.

**Sonuç.** 40 eşzamanlı istek 1..40 verir; başarısız (kapalı dönem) istekler numara tüketmez. `max+1` ile değiştirildiğinde `make verify` T-2 **FAIL** verir. k6 yükü altında (20 eşzamanlı kullanıcı, 20 sn) 6.389 fatura üretildi; numaralar 1..6389 boşluksuz ve çiftsizdi.
**Bedel:** aynı seri/yıl içindeki fatura kesimi **seri** çalışır (kilit). Bu bir sınırdır: tek bir seride çok yüksek yazma hızı gerekirse işletme birden fazla seri kullanabilir (seriler birbirini beklemez).

---

## K-4 · Stok maliyeti: **baştan oynatma (replay)** ile ağırlıklı ortalama

**Bağlam.** Geçmişe dönük bir alış eklenince/düzeltilince sonraki tüm çıkışların maliyeti ve SMM kayıtları yeniden hesaplanmalı.

**Seçenekler.**
1. Artımlı hareketli ortalama (her harekette ortalamayı güncelle) → geçmişe dönük değişikliklerde sonraki hareketlerin hepsi zaten yeniden hesaplanmalı; artımlı durumla tutarsızlık riski yüksek.
2. FIFO/katman maliyeti → şartname AOM istiyor; ayrıca farklı sonuç verir.
3. Dönemsel anlık görüntü (snapshot) → karmaşık; anlık görüntü ile hareketler arasında tutarsızlık olabilir.
4. **Ürünün tüm hareketlerini kronolojik oynatan saf bir fonksiyon** (`stock.Replay`); değişen çıkış maliyetleri `audit_log`'a eski/yeni değerle yazılır, toplam fark tek bir SMM düzeltme yevmiyesi olarak atılır.

**Karar.** Seçenek 4. Aynı ürünü değiştiren işlemler ürün satırı kilidiyle (`SELECT … FOR UPDATE`, id sırasıyla; kilitlenme olmaz) sıraya girer. Negatif stok denetimi, işlemin **sonunda** bir kez yapılır; böylece "eski alışı iptal et + yenisini kes" düzeltmesi ara durumda stok eksiye düşse bile başarılı olur.

**Sonuç.** Değişmezler: stok tamamen satılınca toplam maliyet = toplam alış değeri (kuruş artığı yok, 5.000 rastgele senaryoda doğrulandı); **153 hesabının bakiyesi her zaman stok değerine eşit** (her testte ve simüle edilmiş 50.000 harekette kontrol edilir). Ürün kilidini kaldırınca 10 birimlik stok için 15 eşzamanlı satışın hepsi kabul edildi (aşırı satış); kilitle tam 10 kabul edilir.
**Bedel:** her yazma ürünün hareket sayısı kadar iş yapar (O(n)). Ölçüm ve sınır için bkz. README, "Bilinen sınırlamalar".

---

## K-5 · Tahsilat mahsubu: FIFO + cari kilidi + veritabanı kısıtı

**Bağlam.** Tahsilat en eski açık faturadan başlayarak kapatır; iki eşzamanlı tahsilat bir faturayı fazla kapatmamalı; tahsilat silinirse mahsup geri alınmalı; fazlası avans olmalı.

**Seçenekler.** (a) Yalnızca uygulama kilidi, (b) yalnızca veritabanı kısıtı, (c) ikisi birlikte.

**Karar.** (c). Mahsup ve fatura iptali önce **cari satırını** kilitler (`FOR NO KEY UPDATE`: fatura eklemeyi engellemez); sonra faturaları FIFO sırasıyla (fatura tarihi, oluşturulma zamanı, id) doldurur. Ek olarak ertelenmiş bir constraint trigger, **hiçbir faturaya brütünden fazla, hiçbir ödemeden tutarından fazla mahsup** yazılmasına izin vermez.

**Sonuç.** Cari kilidini kaldırdığımda eşzamanlı testte istekler başarısız oldu ama **veritabanı fazla mahsubu yine engelledi**: kilit canlılığı ve doğru sonucu, kısıt ise güvenliği sağlıyor. Mahsup kayıtları silinmez; geri alma `reversed_at` ile yapılır (denetim izi korunur).
**Bedel:** mahsuplu fatura iptal edilemez (önce ödeme silinmeli); bir faturanın "kısmen iptali" desteklenmez.

---

## K-6 · İdempotans: anahtar tablosu, işle aynı transaction'da

**Bağlam.** Çift tıklama / ağ yeniden denemesi ikinci bir fatura veya ödeme üretmemeli.

**Seçenekler.** Bellek içi önbellek (tek örnekte çalışır, yeniden başlatmada kaybolur), Redis (yeni bağımlılık), **veritabanı tablosu**.

**Karar.** `idempotency_keys(business_id, key)` birincil anahtarı. Anahtar kaydı işle **aynı transaction'da** eklenir: `INSERT … ON CONFLICT DO NOTHING`. Eşzamanlı ikinci istek, birincinin commit'ini bekler (birincil anahtar çakışması) ve ilk yanıtı alır. İstek hata verirse transaction geri alındığı için anahtar **yanmaz**; düzeltilmiş istek aynı anahtarla gönderilebilir. Aynı anahtar farklı gövdeyle gelirse `422`.

**Sonuç.** 10 eşzamanlı aynı anahtarlı istek tek fatura üretir (`make verify` IDEMPOTENCY).

---

## K-7 · Migrasyon: kendi küçük çalıştırıcım, `goose` değil

**Bağlam.** Şema değişiklikleri sürümlü dosyalarla yapılmalı.

**Seçenekler.** `goose`, `golang-migrate`, **kendi çalıştırıcı** (`embed` + `schema_migrations` + danışma kilidi).

**Karar.** Kendi çalıştırıcı (~70 satır). Gerekçe: bağımlılık yükünü ve sandbox'ta modül indirme riskini azaltmak; ihtiyaç yalnızca "ileri" migrasyonlar. Her migrasyon tek transaction'da uygulanır; aynı anda birden çok örnek başlarsa danışma kilidiyle sıraya girer.

**Sonuç.** Migrasyonlar `go:embed` ile ikiliye gömülüdür; `make verify`/Docker'da ek adım gerekmez. **Bedel:** geri alma (down) ve sağlama toplamı denetimi yok; bunlar gerekirse `goose`'a geçişle eklenebilir.

---

## K-8 · Arayüz: bağımlılıksız ES modülleri, derleme adımı yok

**Bağlam.** Frontend serbest. Tek kişi, kısa süre, "tek komutla ayağa kalkma" ve ekip yöneticisinin hızlı inceleyebilmesi öncelikli.

**Seçenekler.** React/Vite, Vue, HTMX, **saf JavaScript (ES modülleri)**.

**Karar.** Saf JS + nginx. Gerekçeler: (1) derleme/paket yöneticisi yok → `docker compose up` sırasında ağ veya sürüm çakışması kaynaklı kırılma riski sıfır; (2) kod doğrudan okunabilir, ~1.400 satır; (3) kullanıcı verisi yalnızca `textContent` ile basılır (XSS yok) ve katı CSP (`script-src 'self'`) uygulanabilir; (4) imaj küçük (nginx-unprivileged, root olmayan kullanıcı).

**Sonuç.** Arayüz gerçek bir tarayıcıda (başsız Firefox, `tests/ui`) uçtan uca gezildi; bu süreçte iki gerçek arayüz hatası bulundu ve düzeltildi (README Soru 4).
**Bedel:** bileşen kütüphanesi ve tip güvenliği yok; büyüdükçe bir çatıya (ör. Vue) geçmek gerekir. Mobilde tablolar CSS ile karta dönüşür.
**Not:** katı CSP'nin üretimde gerçekten uygulandığını gerçek nginx ile `curl -I` üzerinden doğruladım; bu sırada konumlardaki `add_header`'ın üst seviye güvenlik başlıklarını devralmayı kestiğini fark edip `expires -1` ile düzelttim (README Soru 4, madde 5).

---

## K-9 · Seed: işletme başına deterministik rastgelelik, alan paketlerini kullanma

**Bağlam.** 5 işletme, 3.000 cari, ~50.000 hareket; aynı tohumla aynı veri; kısmi ödeme, iptal ve geçmişe dönük düzeltme senaryoları.

**Karar.** Her işletmenin **kendi** rastgele sayı üreteci vardır ve yalnızca kendi verisine dokunur; bu yüzden işletmeler paralel çalışsa da sonuç zamanlamadan etkilenmez. Üretim, uygulamanın kendi fatura/ödeme/stok paketlerini çağırır; yani kurallar (çift kayıt, KDV, FIFO, AOM) birebir geçerlidir; ham SQL ile veri basılmaz. İçerik özeti (SHA-256, kimlik ve zaman damgası hariç) ile determinizm doğrulanır.

**Sonuç.** Aynı tohum = aynı özet; `reset` + `seed` ilk seed ile aynı özeti verir (test). Seed sırasında yalnızca seed oturumunda `synchronous_commit=off` kullanılır (yarım kalırsa zaten baştan sıfırlanır).
**Bedel:** `reset` için süper kullanıcı yetkisi (`session_replication_role=replica`) gerekir; bu yalnızca demo/geliştirme komutudur, hiçbir HTTP ucundan çağrılamaz.

---

## K-10 · Denetim kaydı: işle aynı transaction'da, eklemeli-yalnız

**Bağlam.** Şartname, kritik işlemlerin (kim, ne zaman, neyi, eski/yeni değer) eklemeli-yalnız bir tabloya yazılmasını ister.

**Seçenekler.**
1. Veritabanı tetikleyicileriyle otomatik kayıt → "kim" bilgisi (uygulama kullanıcısı) bilinmez; iş anlamı (iptal, düzeltme) çıkarılamaz.
2. Uygulamada, işlemden sonra ayrı bir istekle yazma → işlem başarılı olup kayıt yazılamayabilir (ya da tersi).
3. **Uygulamada, işle AYNI transaction'da yazma** (`internal/audit`); tablo tetikleyiciyle eklemeli-yalnız.

**Karar.** Seçenek 3. Alan fonksiyonları (`CreateTx`, `CancelTx`, ödeme oluştur/sil/avans mahsubu), dönem kapatma ve cari/ürün oluşturma kaydı kendi transaction'ı içinde yazar.

**Sonuç.** Başarısız bir işlem (kapalı dönem, yetersiz stok, geçersiz KDV, tekrarlı SKU, dönemin geri açılması) **hiç kayıt bırakmaz**; başarılı bir işlem tam bir kayıt bırakır (`TestFailedOperationsLeaveNoAuditTrail`, `make verify` AUDIT). Kayıt hangi kullanıcının yaptığını (`actor_id`/e-posta) ve eski → yeni değeri taşır. Denetim kayıtlarını silmek/değiştirmek veritabanında reddedilir.
**Bedel:** her yazma işlemine bir `INSERT` eklenir. Ölçtüm: 6.000 olaylık seed denetimsiz 16,8–18,7 sn, denetimli 16,5–17,3 sn sürdü (≈6.000 ek denetim satırı yazılmasına rağmen fark ölçüm gürültüsü içinde). Yalnızca *kritik* işlemler kaydedilir, okumalar kaydedilmez; tablo zamanla büyür (dizin: `(business_id, id DESC)`).
