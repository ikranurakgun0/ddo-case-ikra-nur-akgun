package api_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"esnafdefter/internal/auth"
	"esnafdefter/internal/stock"
	"esnafdefter/internal/testutil"
)

func (e *env) product(t *testing.T, tok, sku, kind string) string {
	t.Helper()
	code, m, _ := e.call("POST", "/products", map[string]any{"sku": sku, "name": "Ürün " + sku, "kind": kind, "vat_rate": 20, "sale_price": 20000}, tok, nil)
	if code != 201 {
		t.Fatalf("ürün: %d %v", code, m)
	}
	return m["id"].(string)
}

func buyBody(supplier, product, date, qty string, price int) map[string]any {
	b := saleBody(supplier, date)
	b["kind"] = "purchase"
	b["lines"] = []map[string]any{{"product_id": product, "description": "alış", "quantity": qty, "unit_price": price, "vat_rate": 20}}
	return b
}

func sellBody(customer, product, date, qty string) map[string]any {
	b := saleBody(customer, date)
	b["lines"] = []map[string]any{{"product_id": product, "description": "satış", "quantity": qty, "unit_price": 25000, "vat_rate": 20}}
	return b
}

func (e *env) buy(t *testing.T, tok, supplier, product, date, qty string, price int) map[string]any {
	t.Helper()
	return e.newInvoice(t, tok, buyBody(supplier, product, date, qty, price))
}

func (e *env) sell(tok, customer, product, date, qty string) (int, map[string]any) {
	code, m, _ := e.call("POST", "/invoices", sellBody(customer, product, date, qty), tok, nil)
	return code, m
}

func (e *env) stockOf(t *testing.T, tok, pid string) (qty, value float64) {
	t.Helper()
	_, m, _ := e.call("GET", "/products/"+pid, nil, tok, nil)
	s := m["stock"].(map[string]any)
	return s["quantity_milli"].(float64), s["value"].(float64)
}

// net hesap bakiyesi (borç - alacak)
func (e *env) acct(t *testing.T, tok, code string) float64 {
	d, c := balanceOf(t, e, tok, code)
	return d - c
}

func errCodeOf(m map[string]any) string {
	em, _ := m["error"].(map[string]any)
	s, _ := em["code"].(string)
	return s
}

type stockEnv struct {
	*env
	tok, cust, supp string
}

func newStockEnv(t *testing.T) *stockEnv {
	e := setup(t)
	tok := e.token(e.user)
	return &stockEnv{env: e, tok: tok, cust: e.newContact(t, tok, "customer"), supp: e.newContact(t, tok, "supplier")}
}

func TestWeightedAverageCostPostsSMM(t *testing.T) {
	s := newStockEnv(t)
	p := s.product(t, s.tok, "A1", "product")
	s.buy(t, s.tok, s.supp, p, "2026-09-01", "10", 10000) // 10 x 100 TL
	s.buy(t, s.tok, s.supp, p, "2026-09-02", "10", 20000) // 10 x 200 TL -> ortalama 150 TL
	if code, m := s.sell(s.tok, s.cust, p, "2026-09-10", "5"); code != 201 {
		t.Fatalf("satış: %d %v", code, m)
	}
	q, v := s.stockOf(t, s.tok, p)
	if q != 15000 || v != 225000 { // 300000 - 5*150 TL (75000)
		t.Fatalf("stok: %v / %v", q, v)
	}
	if got := s.acct(t, s.tok, "621"); got != 75000 {
		t.Errorf("SMM (621) 75000 olmalı: %v", got)
	}
	// Değişmez: 153 hesabı = stok değeri
	if got := s.acct(t, s.tok, "153"); got != v {
		t.Errorf("153 bakiyesi stok değerine eşit olmalı: %v != %v", got, v)
	}
}

func TestStockFullySoldLeavesNoResidue(t *testing.T) {
	s := newStockEnv(t)
	p := s.product(t, s.tok, "A2", "product")
	b := buyBody(s.supp, p, "2026-09-01", "3", 3334) // 10002
	b["general_discount"] = 2                        // net 10000
	s.newInvoice(t, s.tok, b)
	var costs int64
	for i := 0; i < 3; i++ {
		if code, m := s.sell(s.tok, s.cust, p, fmt.Sprintf("2026-09-%02d", 10+i), "1"); code != 201 {
			t.Fatalf("satış %d: %d %v", i, code, m)
		}
	}
	_, mv, _ := s.call("GET", "/stock-movements?product_id="+p, nil, s.tok, nil)
	for _, it := range mv["items"].([]any) {
		m := it.(map[string]any)
		if m["direction"] == "out" {
			costs += int64(m["value"].(float64))
		}
	}
	if costs != 10000 {
		t.Fatalf("toplam çıkış maliyeti alış değerine (10000) eşit olmalı: %d", costs)
	}
	if q, v := s.stockOf(t, s.tok, p); q != 0 || v != 0 {
		t.Fatalf("stok bitince değer 0 olmalı: %v/%v", q, v)
	}
	if got := s.acct(t, s.tok, "153"); got != 0 {
		t.Errorf("153 sıfırlanmalı: %v", got)
	}
	if got := s.acct(t, s.tok, "621"); got != 10000 {
		t.Errorf("621: %v", got)
	}
}

// T-4 kanıtı: geçmişe dönük alış, sonraki çıkışların maliyetini ve SMM kayıtlarını yeniden hesaplatır.
func TestBackdatedPurchaseRecalculatesLaterCostsAndSMM(t *testing.T) {
	s := newStockEnv(t)
	p := s.product(t, s.tok, "A3", "product")
	s.buy(t, s.tok, s.supp, p, "2026-09-01", "10", 10000)
	if code, m := s.sell(s.tok, s.cust, p, "2026-09-10", "5"); code != 201 {
		t.Fatalf("satış: %d %v", code, m)
	}
	if got := s.acct(t, s.tok, "621"); got != 50000 {
		t.Fatalf("başlangıç SMM: %v", got)
	}
	// Satıştan ÖNCEki tarihe sonradan alış girilir: 10 x 300 TL
	s.buy(t, s.tok, s.supp, p, "2026-09-05", "10", 30000)
	// Ortalama (100000+300000)/20 = 200 TL -> satışın maliyeti 1000 TL
	if got := s.acct(t, s.tok, "621"); got != 100000 {
		t.Fatalf("SMM yeniden hesaplanmalı (100000): %v", got)
	}
	q, v := s.stockOf(t, s.tok, p)
	if q != 15000 || v != 300000 {
		t.Fatalf("stok: %v/%v", q, v)
	}
	if got := s.acct(t, s.tok, "153"); got != v {
		t.Errorf("153 = stok değeri olmalı: %v != %v", got, v)
	}
	// Denetim izi: eski/yeni değer
	var old, nw string
	err := s.pool.QueryRow(context.Background(),
		`SELECT old_value->>'value', new_value->>'value' FROM audit_log WHERE action='stock_cost_recalc'`).Scan(&old, &nw)
	if err != nil || old != "50000" || nw != "100000" {
		t.Fatalf("denetim kaydı: %v %v %v", old, nw, err)
	}
	// Satıştan SONRAki bir alış maliyeti değiştirmez
	s.buy(t, s.tok, s.supp, p, "2026-09-20", "10", 90000)
	if got := s.acct(t, s.tok, "621"); got != 100000 {
		t.Errorf("sonraki alış SMM'yi değiştirmemeli: %v", got)
	}
}

func TestCorrectPurchaseInvoiceRecomputesCosts(t *testing.T) {
	s := newStockEnv(t)
	p := s.product(t, s.tok, "A4", "product")
	orig := s.buy(t, s.tok, s.supp, p, "2026-09-01", "10", 10000)
	s.sell(s.tok, s.cust, p, "2026-09-10", "10") // tüm stok satıldı
	if got := s.acct(t, s.tok, "621"); got != 100000 {
		t.Fatalf("SMM: %v", got)
	}
	// Alış faturası düzeltilir: birim fiyat 100 -> 120 TL. Ara durumda (eski iptal) stok eksiye düşer ama
	// işlem sonunda tek seferde kontrol edildiği için düzeltme başarılı olmalıdır.
	fix := buyBody(s.supp, p, "2026-09-01", "10", 12000)
	code, m, _ := s.call("POST", "/invoices/"+orig["id"].(string)+"/correct", fix, s.tok, nil)
	if code != 201 {
		t.Fatalf("düzeltme: %d %v", code, m)
	}
	if got := s.acct(t, s.tok, "621"); got != 120000 {
		t.Errorf("SMM 120000'e güncellenmeli: %v", got)
	}
	if q, v := s.stockOf(t, s.tok, p); q != 0 || v != 0 {
		t.Errorf("stok: %v/%v", q, v)
	}
	if got := s.acct(t, s.tok, "153"); got != 0 {
		t.Errorf("153: %v", got)
	}
	if o := s.invoice(t, s.tok, orig["id"].(string)); o["status"] != "cancelled" {
		t.Errorf("eski fatura iptal edilmeli: %v", o["status"])
	}
	// Satış faturalarını ve başka cariyi düzeltmeye izin verilmez
	_, sales, _ := s.call("GET", "/invoices", nil, s.tok, nil)
	for _, it := range sales["items"].([]any) {
		in := it.(map[string]any)
		if in["kind"] == "sale" {
			if c, _, _ := s.call("POST", "/invoices/"+in["id"].(string)+"/correct", fix, s.tok, nil); c != 400 {
				t.Errorf("satış faturası düzeltilemez: %d", c)
			}
		}
	}
	other := s.newContact(t, s.tok, "supplier")
	if c, _, _ := s.call("POST", "/invoices/"+m["id"].(string)+"/correct", buyBody(other, p, "2026-09-01", "10", 1), s.tok, nil); c != 400 {
		t.Errorf("farklı cariye düzeltme reddedilmeli: %d", c)
	}
}

func TestInsufficientStockIsRejectedAndRollsBack(t *testing.T) {
	s := newStockEnv(t)
	p := s.product(t, s.tok, "A5", "product")
	s.buy(t, s.tok, s.supp, p, "2026-09-01", "5", 10000)
	code, m := s.sell(s.tok, s.cust, p, "2026-09-10", "6")
	if code != 409 || errCodeOf(m) != "insufficient_stock" {
		t.Fatalf("yetersiz stok 409 olmalı: %d %v", code, m)
	}
	// Satış, bir alıştan ÖNCEki bir tarihe yazılırsa da engellenir
	if code, m := s.sell(s.tok, s.cust, p, "2026-08-01", "1"); code != 409 {
		t.Fatalf("stok olmayan geçmiş tarihli satış 409 olmalı: %d %v", code, m)
	}
	// Hiç iz bırakmamalı ve numara tüketmemeli
	inv := s.newInvoice(t, s.tok, sellBody(s.cust, p, "2026-09-11", "5"))
	if inv["seq"] != 1.0 {
		t.Fatalf("başarısız satışlar numara tüketmemeli: seq=%v", inv["seq"])
	}
	if got := s.acct(t, s.tok, "621"); got != 50000 {
		t.Errorf("SMM yalnızca başarılı satış için: %v", got)
	}
}

func TestNegativeStockPolicyIsConfigurable(t *testing.T) {
	s := newStockEnv(t)
	p := s.product(t, s.tok, "A6", "product")
	_, st, _ := s.call("GET", "/settings", nil, s.tok, nil)
	if st["allow_negative_stock"] != false {
		t.Fatalf("varsayılan kapalı olmalı: %v", st)
	}
	emp := s.token(testutil.CreateUser(t, s.pool, s.user.BusinessID, "c@t.test", "parola-xyz-1", auth.RoleEmployee))
	if c, _, _ := s.call("PUT", "/settings", map[string]any{"allow_negative_stock": true}, emp, nil); c != 403 {
		t.Fatalf("çalışan ayar değiştirememeli: %d", c)
	}
	if c, _, _ := s.call("PUT", "/settings", map[string]any{"allow_negative_stock": true}, s.tok, nil); c != 200 {
		t.Fatalf("sahip ayarı değiştirebilmeli: %d", c)
	}
	if code, m := s.sell(s.tok, s.cust, p, "2026-09-10", "3"); code != 201 {
		t.Fatalf("negatif stok açıkken satış serbest olmalı: %d %v", code, m)
	}
	if q, _ := s.stockOf(t, s.tok, p); q != -3000 {
		t.Fatalf("stok -3 olmalı: %v", q)
	}
	// Sonradan alış gelince (geçmişe dönük) maliyet son birim maliyetle değil, ilk eşleşmeyle yeniden hesaplanır
	s.buy(t, s.tok, s.supp, p, "2026-09-01", "10", 10000)
	if q, v := s.stockOf(t, s.tok, p); q != 7000 || v != 70000 {
		t.Fatalf("alış sonrası stok: %v/%v", q, v)
	}
	if got := s.acct(t, s.tok, "153"); got != 70000 {
		t.Errorf("153 = stok değeri: %v", got)
	}
	// Audit: ayar değişikliği kaydedilir
	var n int
	_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action='settings_update'`).Scan(&n)
	if n != 1 {
		t.Errorf("ayar değişikliği denetim kaydında olmalı: %d", n)
	}
}

func TestCancelPurchaseBlockedWhenStockAlreadySold(t *testing.T) {
	s := newStockEnv(t)
	p := s.product(t, s.tok, "A7", "product")
	pur := s.buy(t, s.tok, s.supp, p, "2026-09-01", "10", 10000)
	_, sale := s.sell(s.tok, s.cust, p, "2026-09-10", "8")
	code, m, _ := s.call("POST", "/invoices/"+pur["id"].(string)+"/cancel", nil, s.tok, nil)
	if code != 409 || errCodeOf(m) != "insufficient_stock" {
		t.Fatalf("satılmış stoğun alışı iptal edilememeli: %d %v", code, m)
	}
	if o := s.invoice(t, s.tok, pur["id"].(string)); o["status"] != "issued" {
		t.Fatalf("başarısız iptal hiçbir şeyi değiştirmemeli: %v", o["status"])
	}
	// Önce satış iptal edilirse alış da iptal edilebilir
	if c, _, _ := s.call("POST", "/invoices/"+sale["id"].(string)+"/cancel", nil, s.tok, nil); c != 200 {
		t.Fatalf("satış iptali: %d", c)
	}
	if c, _, _ := s.call("POST", "/invoices/"+pur["id"].(string)+"/cancel", nil, s.tok, nil); c != 200 {
		t.Fatalf("alış iptali artık mümkün olmalı: %d", c)
	}
	for _, acc := range []string{"153", "621"} {
		if got := s.acct(t, s.tok, acc); got != 0 {
			t.Errorf("%s sıfırlanmalı: %v", acc, got)
		}
	}
}

func TestCancelSaleRestoresStockAndReversesSMM(t *testing.T) {
	s := newStockEnv(t)
	p := s.product(t, s.tok, "A8", "product")
	s.buy(t, s.tok, s.supp, p, "2026-09-01", "10", 10000)
	_, sale := s.sell(s.tok, s.cust, p, "2026-09-10", "4")
	if c, _, _ := s.call("POST", "/invoices/"+sale["id"].(string)+"/cancel", nil, s.tok, nil); c != 200 {
		t.Fatalf("iptal: %d", c)
	}
	if q, v := s.stockOf(t, s.tok, p); q != 10000 || v != 100000 {
		t.Fatalf("stok geri gelmeli: %v/%v", q, v)
	}
	if got := s.acct(t, s.tok, "621"); got != 0 {
		t.Errorf("SMM ters çevrilmeli: %v", got)
	}
	if got := s.acct(t, s.tok, "153"); got != 100000 {
		t.Errorf("153: %v", got)
	}
}

func TestServiceLinesDoNotMoveStock(t *testing.T) {
	s := newStockEnv(t)
	svc := s.product(t, s.tok, "HZM", "service")
	if code, m := s.sell(s.tok, s.cust, svc, "2026-09-10", "3"); code != 201 {
		t.Fatalf("hizmet satışı stok istemez: %d %v", code, m)
	}
	var n int
	_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM stock_movements`).Scan(&n)
	if n != 0 {
		t.Fatalf("hizmet stok hareketi üretmemeli: %d", n)
	}
	if got := s.acct(t, s.tok, "621"); got != 0 {
		t.Errorf("hizmette SMM olmaz: %v", got)
	}
	// Hizmet alışı gider hesabına (770) yazılır
	pb := buyBody(s.supp, svc, "2026-09-11", "1", 5000)
	s.newInvoice(t, s.tok, pb)
	if got := s.acct(t, s.tok, "770"); got != 5000 {
		t.Errorf("770: %v", got)
	}
}

// T-4 yarış kanıtı: son 10 birim için 20 eşzamanlı satış -> tam 10 başarılı, 10 reddedilen; stok eksiye düşmez.
func TestConcurrentSalesCannotOversell(t *testing.T) {
	s := newStockEnv(t)
	p := s.product(t, s.tok, "A9", "product")
	s.buy(t, s.tok, s.supp, p, "2026-09-01", "10", 10000)
	var mu sync.Mutex
	var ok, rejected int
	var seqs []int
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, m := s.sell(s.tok, s.cust, p, "2026-09-10", "1")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case code == 201:
				ok++
				seqs = append(seqs, int(m["seq"].(float64)))
			case code == 409 && errCodeOf(m) == "insufficient_stock":
				rejected++
			default:
				t.Errorf("beklenmeyen yanıt: %d %v", code, m)
			}
		}()
	}
	wg.Wait()
	if ok != 10 || rejected != 10 {
		t.Fatalf("10 başarılı + 10 reddedilen bekleniyordu: %d / %d", ok, rejected)
	}
	sort.Ints(seqs)
	for i, v := range seqs {
		if v != i+1 {
			t.Fatalf("reddedilenler numara tüketmemeli; numaralar %v", seqs)
		}
	}
	if q, v := s.stockOf(t, s.tok, p); q != 0 || v != 0 {
		t.Fatalf("stok tam bitmeli: %v/%v", q, v)
	}
	if got := s.acct(t, s.tok, "621"); got != 100000 {
		t.Fatalf("SMM: %v", got)
	}
}

// Karışık, eşzamanlı iş yükünden sonra tüm stok/defter değişmezleri korunmalı.
func TestStockInvariantsAfterConcurrentMixedWorkload(t *testing.T) {
	s := newStockEnv(t)
	prods := []string{s.product(t, s.tok, "M1", "product"), s.product(t, s.tok, "M2", "product")}
	for i, p := range prods {
		s.buy(t, s.tok, s.supp, p, "2026-09-01", "200", 10000+i*3333)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ { // satışlar
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := prods[i%2]
			qty := fmt.Sprintf("%d.%03d", 1+i%4, (i*131)%1000)
			if code, m := s.sell(s.tok, s.cust, p, fmt.Sprintf("2026-10-%02d", 1+i%20), qty); code != 201 {
				t.Errorf("satış %d: %d %v", i, code, m)
			}
		}(i)
	}
	for i := 0; i < 6; i++ { // eşzamanlı geçmişe dönük alışlar
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := buyBody(s.supp, prods[i%2], fmt.Sprintf("2026-09-%02d", 2+i*3), "20", 8000+i*1777)
			if code, m, _ := s.call("POST", "/invoices", b, s.tok, nil); code != 201 {
				t.Errorf("alış %d: %d %v", i, code, m)
			}
		}(i)
	}
	wg.Wait()
	// bazı satışları iptal et
	_, list, _ := s.call("GET", "/invoices?limit=200", nil, s.tok, nil)
	n := 0
	for _, it := range list["items"].([]any) {
		in := it.(map[string]any)
		if in["kind"] == "sale" && n < 8 {
			if c, m, _ := s.call("POST", "/invoices/"+in["id"].(string)+"/cancel", nil, s.tok, nil); c != 200 {
				t.Errorf("iptal: %d %v", c, m)
			}
			n++
		}
	}

	ctx := context.Background()
	_, tb, _ := s.call("GET", "/reports/trial-balance", nil, s.tok, nil)
	if tb["balanced"] != true {
		t.Fatalf("mizan dengesiz: %v", tb)
	}
	_, rep, _ := s.call("GET", "/reports/stock", nil, s.tok, nil)
	if got := s.acct(t, s.tok, "153"); got != rep["total_value"].(float64) {
		t.Fatalf("153 (%v) stok raporu değerine (%v) eşit olmalı", got, rep["total_value"])
	}
	var outs int64
	_ = s.pool.QueryRow(ctx, `SELECT coalesce(sum(value),0) FROM stock_movements WHERE direction='out' AND active`).Scan(&outs)
	if got := s.acct(t, s.tok, "621"); got != float64(outs) {
		t.Fatalf("621 (%v) aktif çıkış maliyetleri toplamına (%d) eşit olmalı", got, outs)
	}
	// Veritabanındaki değerler, saf AOM fonksiyonunun baştan oynatmasıyla birebir aynı olmalı
	for _, pid := range prods {
		rows, _ := s.pool.Query(ctx, `SELECT id, direction='out', quantity_milli, value, active FROM stock_movements
			WHERE product_id=$1::uuid ORDER BY movement_date, (direction='out'), id`, pid)
		var moves []stock.Move
		stored := map[int64]int64{}
		for rows.Next() {
			var m stock.Move
			var v int64
			_ = rows.Scan(&m.ID, &m.Out, &m.Qty, &v, &m.Active)
			if m.Out {
				stored[m.ID] = v
			} else {
				m.InValue = v
			}
			moves = append(moves, m)
		}
		rows.Close()
		res := stock.Replay(moves)
		if res.MinQty < 0 {
			t.Errorf("%s: stok negatife düşmüş", pid)
		}
		for id, c := range res.Cost {
			if stored[id] != c {
				t.Errorf("hareket %d: kayıtlı maliyet %d, yeniden oynatma %d", id, stored[id], c)
			}
		}
	}
}

func TestStockMovementsAreImmutableInDatabase(t *testing.T) {
	s := newStockEnv(t)
	p := s.product(t, s.tok, "B1", "product")
	s.buy(t, s.tok, s.supp, p, "2026-09-01", "10", 10000)
	s.sell(s.tok, s.cust, p, "2026-09-10", "2")
	ctx := context.Background()
	for _, q := range []string{
		`UPDATE stock_movements SET quantity_milli = 1`,
		`UPDATE stock_movements SET movement_date = '2020-01-01'`,
		`UPDATE stock_movements SET value = 1 WHERE direction = 'in'`,
		`DELETE FROM stock_movements`,
		`TRUNCATE stock_movements`,
	} {
		if _, err := s.pool.Exec(ctx, q); err == nil {
			t.Errorf("%q reddedilmeliydi", q)
		}
	}
	// pasifleştirilmiş hareket yeniden etkinleştirilemez
	s.pool.Exec(ctx, `UPDATE stock_movements SET active=false WHERE direction='out'`) //nolint:errcheck
	if _, err := s.pool.Exec(ctx, `UPDATE stock_movements SET active=true WHERE direction='out'`); err == nil {
		t.Error("iptal geri alınamamalı")
	}
}

func TestProductValidationAndTenancy(t *testing.T) {
	s := newStockEnv(t)
	p := s.product(t, s.tok, "C1", "product")
	cases := map[string]map[string]any{
		"kopya sku":       {"sku": "C1", "name": "x", "kind": "product", "vat_rate": 20},
		"geçersiz kdv":    {"sku": "C2", "name": "x", "kind": "product", "vat_rate": 8},
		"geçersiz tür":    {"sku": "C3", "name": "x", "kind": "gift", "vat_rate": 20},
		"adsız":           {"sku": "C4", "name": " ", "kind": "product", "vat_rate": 20},
		"bilinmeyen alan": {"sku": "C5", "name": "x", "kind": "product", "vat_rate": 20, "x": 1},
	}
	want := map[string]int{"kopya sku": 409, "geçersiz kdv": 400, "geçersiz tür": 400, "adsız": 400, "bilinmeyen alan": 400}
	for name, body := range cases {
		if code, m, _ := s.call("POST", "/products", body, s.tok, nil); code != want[name] {
			t.Errorf("%s: %d bekleniyordu, %d (%v)", name, want[name], code, m)
		}
	}
	// Başka işletmenin ürünü faturada kullanılamaz ve görünmez
	bizB := testutil.CreateBusiness(t, s.pool, "B", "5555555555")
	tokB := s.token(testutil.CreateUser(t, s.pool, bizB, "b@b.test", "parola-b-123", auth.RoleOwner))
	custB := s.newContact(t, tokB, "customer")
	if code, m := s.sell(tokB, custB, p, "2026-09-10", "1"); code != 404 || errCodeOf(m) != "product_not_found" {
		t.Errorf("başka işletmenin ürünü 404 olmalı: %d %v", code, m)
	}
	if code, _, _ := s.call("GET", "/products/"+p, nil, tokB, nil); code != 404 {
		t.Errorf("ürün sızmamalı: %d", code)
	}
	_, rep, _ := s.call("GET", "/reports/stock", nil, tokB, nil)
	if len(rep["products"].([]any)) != 0 {
		t.Errorf("stok raporu izole olmalı: %v", rep)
	}
	if code, m := s.sell(s.tok, s.cust, "00000000-0000-0000-0000-000000000000", "2026-09-10", "1"); code != 404 {
		t.Errorf("bilinmeyen ürün 404: %d %v", code, m)
	}
}

// Her satış FARKLI bir seriden kesilir: fatura sayacı satırı istekleri sıraya sokmaz, bu yüzden
// aşırı satışı yalnızca ürün satırı kilidi engeller. (Aynı seriyi kullanan test sayaç kilidiyle de geçerdi.)
func TestConcurrentSalesAcrossSeriesCannotOversell(t *testing.T) {
	s := newStockEnv(t)
	p := s.product(t, s.tok, "D1", "product")
	s.buy(t, s.tok, s.supp, p, "2026-09-01", "10", 10000)
	var mu sync.Mutex
	var ok, rejected int
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := sellBody(s.cust, p, "2026-09-10", "1")
			b["series"] = fmt.Sprintf("S%d", i)
			code, m, _ := s.call("POST", "/invoices", b, s.tok, nil)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case code == 201:
				ok++
			case code == 409 && errCodeOf(m) == "insufficient_stock":
				rejected++
			default:
				t.Errorf("beklenmeyen yanıt: %d %v", code, m)
			}
		}(i)
	}
	wg.Wait()
	if ok != 10 || rejected != 10 {
		t.Fatalf("10 başarılı + 10 reddedilen bekleniyordu: %d / %d", ok, rejected)
	}
	if q, v := s.stockOf(t, s.tok, p); q != 0 || v != 0 {
		t.Fatalf("stok tam bitmeli: %v/%v", q, v)
	}
	if got := s.acct(t, s.tok, "621"); got != 100000 {
		t.Fatalf("SMM: %v", got)
	}
}
