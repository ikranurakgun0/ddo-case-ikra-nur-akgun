package api_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"esnafdefter/internal/auth"
	"esnafdefter/internal/testutil"
)

// inv belirli tutarlı (KDV hariç net = amount, %20 KDV eklenir) bir satış faturası keser.
// Brüt = net * 1,2; testlerde sayıları kolay tutmak için net'i 100'ün katı seçiyoruz.
func (e *env) invoiceAt(t *testing.T, tok, contact, issue, due string, net int) map[string]any {
	t.Helper()
	b := saleBody(contact, issue)
	b["due_date"] = due
	b["lines"] = []map[string]any{{"description": "x", "quantity": "1", "unit_price": net, "vat_rate": 20}}
	return e.newInvoice(t, tok, b)
}

func (e *env) pay(t *testing.T, tok, contact string, amount int, date string) (int, map[string]any) {
	t.Helper()
	code, m, _ := e.call("POST", "/payments", map[string]any{
		"kind": "receipt", "contact_id": contact, "method": "bank", "date": date, "amount": amount}, tok, nil)
	return code, m
}

func (e *env) invoice(t *testing.T, tok, id string) map[string]any {
	t.Helper()
	_, m, _ := e.call("GET", "/invoices/"+id, nil, tok, nil)
	return m
}

func TestFIFOAllocationPartialPayment(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	// Brütler: 12000, 24000, 36000 (net 10000, 20000, 30000); en eski önce kapanmalı
	i1 := e.invoiceAt(t, tok, cid, "2026-08-01", "2026-09-01", 10000)
	i2 := e.invoiceAt(t, tok, cid, "2026-08-10", "2026-09-10", 20000)
	i3 := e.invoiceAt(t, tok, cid, "2026-08-20", "2026-09-20", 30000)

	code, p := e.pay(t, tok, cid, 20000, "2026-09-05") // 12000 + 8000
	if code != 201 {
		t.Fatalf("ödeme: %d %v", code, p)
	}
	if p["allocated"] != 20000.0 || p["advance"] != 0.0 {
		t.Fatalf("mahsup: %v", p)
	}
	allocs := p["allocations"].([]any)
	if len(allocs) != 2 || allocs[0].(map[string]any)["amount"] != 12000.0 || allocs[1].(map[string]any)["amount"] != 8000.0 {
		t.Fatalf("FIFO dağılımı yanlış: %v", allocs)
	}
	if a := e.invoice(t, tok, i1["id"].(string)); a["open"] != 0.0 || a["paid"] != 12000.0 {
		t.Errorf("fatura 1 kapanmalı: %v", a)
	}
	if a := e.invoice(t, tok, i2["id"].(string)); a["open"] != 16000.0 || a["paid"] != 8000.0 {
		t.Errorf("fatura 2 kısmen ödenmeli: %v", a)
	}
	if a := e.invoice(t, tok, i3["id"].(string)); a["open"] != 36000.0 {
		t.Errorf("fatura 3 dokunulmamalı: %v", a)
	}
	// Yevmiye: kasa/banka borç 20000, 120 alacak 20000
	if d, _ := balanceOf(t, e, tok, "102"); d != 20000 {
		t.Errorf("102 borç: %v", d)
	}
	if _, c := balanceOf(t, e, tok, "120"); c != 20000 {
		t.Errorf("120 alacak: %v", c)
	}
}

func TestOverpaymentBecomesAdvanceAndCanBeApplied(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	e.invoiceAt(t, tok, cid, "2026-08-01", "2026-09-01", 10000) // brüt 12000
	code, p := e.pay(t, tok, cid, 20000, "2026-09-05")
	if code != 201 || p["allocated"] != 12000.0 || p["advance"] != 8000.0 {
		t.Fatalf("fazla ödeme avans olmalı: %d %v", code, p)
	}
	if _, c := balanceOf(t, e, tok, "340"); c != 8000 {
		t.Errorf("340 alacak (avans): %v", c)
	}
	// Sonradan yeni fatura kesilir, avans mahsup edilir
	i2 := e.invoiceAt(t, tok, cid, "2026-09-10", "2026-10-10", 5000) // brüt 6000
	c2, p2, _ := e.call("POST", "/payments/"+p["id"].(string)+"/apply-advance", nil, tok, nil)
	if c2 != 200 || p2["allocated"] != 18000.0 || p2["advance"] != 2000.0 {
		t.Fatalf("avans mahsubu: %d %v", c2, p2)
	}
	if a := e.invoice(t, tok, i2["id"].(string)); a["open"] != 0.0 {
		t.Errorf("yeni fatura avansla kapanmalı: %v", a)
	}
	d340, c340 := balanceOf(t, e, tok, "340")
	if c340-d340 != 2000 {
		t.Errorf("340 net bakiye 2000 olmalı: %v/%v", d340, c340)
	}
}

func TestPaymentWithNoOpenInvoicesIsFullAdvance(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	code, p := e.pay(t, tok, cid, 5000, "2026-09-05")
	if code != 201 || p["allocated"] != 0.0 || p["advance"] != 5000.0 {
		t.Fatalf("%d %v", code, p)
	}
}

func TestDeletePaymentRestoresInvoicesAndLedger(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	i1 := e.invoiceAt(t, tok, cid, "2026-08-01", "2026-09-01", 10000)
	i2 := e.invoiceAt(t, tok, cid, "2026-08-10", "2026-09-10", 10000)
	_, p := e.pay(t, tok, cid, 20000, "2026-09-05")

	code, del, _ := e.call("DELETE", "/payments/"+p["id"].(string), nil, tok, nil)
	if code != 200 || del["status"] != "deleted" || del["allocated"] != 0.0 {
		t.Fatalf("silme: %d %v", code, del)
	}
	for _, id := range []string{i1["id"].(string), i2["id"].(string)} {
		if a := e.invoice(t, tok, id); a["open"] != 12000.0 || a["paid"] != 0.0 {
			t.Errorf("fatura yeniden açılmalı: %v", a)
		}
	}
	// Banka ve alıcılar hesabı ters kayıtla sıfırlanır (alıcılar = 2 faturanın brütü kalır)
	if d, c := balanceOf(t, e, tok, "102"); d != c {
		t.Errorf("102 sıfırlanmalı: %v/%v", d, c)
	}
	if d, c := balanceOf(t, e, tok, "120"); d-c != 24000 {
		t.Errorf("120 net bakiye 24000 olmalı: %v/%v", d, c)
	}
	if code, m, _ := e.call("DELETE", "/payments/"+p["id"].(string), nil, tok, nil); code != 409 || m["error"].(map[string]any)["code"] != "already_deleted" {
		t.Errorf("ikinci silme 409: %d %v", code, m)
	}
	// Silinen ödeme sonrası tekrar tahsilat aynı faturaları yine FIFO ile kapatabilmeli
	_, p2 := e.pay(t, tok, cid, 12000, "2026-09-06")
	if a := e.invoice(t, tok, i1["id"].(string)); a["open"] != 0.0 {
		t.Errorf("yeni ödeme en eski faturayı kapatmalı: %v (%v)", a, p2)
	}
}

func TestDeletePaymentWithAdvanceReversesAdvanceEntries(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	_, p := e.pay(t, tok, cid, 5000, "2026-09-05") // tamamı avans
	e.invoiceAt(t, tok, cid, "2026-09-06", "2026-10-06", 4000)
	e.call("POST", "/payments/"+p["id"].(string)+"/apply-advance", nil, tok, nil) // 4800 mahsup
	if code, _, _ := e.call("DELETE", "/payments/"+p["id"].(string), nil, tok, nil); code != 200 {
		t.Fatalf("silme başarısız: %d", code)
	}
	for _, acc := range []string{"102", "340"} {
		if d, c := balanceOf(t, e, tok, acc); d != c {
			t.Errorf("%s sıfırlanmalı (avans + mahsup kayıtları ters çevrilmeli): %v/%v", acc, d, c)
		}
	}
}

func TestReceiptNeverAllocatesToPurchaseInvoices(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "both")
	pb := saleBody(cid, "2026-08-01")
	pb["kind"] = "purchase"
	pur := e.newInvoice(t, tok, pb)
	_, p := e.pay(t, tok, cid, 24000, "2026-09-05")
	if p["allocated"] != 0.0 || p["advance"] != 24000.0 {
		t.Fatalf("tahsilat alış faturasına mahsup edilmemeli: %v", p)
	}
	if a := e.invoice(t, tok, pur["id"].(string)); a["open"] != 24000.0 {
		t.Errorf("alış faturası açık kalmalı: %v", a)
	}
	// Ödeme (disbursement) ise alış faturasını kapatır ve 320/102 hesaplarını kullanır
	c, d, _ := e.call("POST", "/payments", map[string]any{"kind": "disbursement", "contact_id": cid, "method": "cash", "date": "2026-09-06", "amount": 24000}, tok, nil)
	if c != 201 || d["allocated"] != 24000.0 {
		t.Fatalf("ödeme alış faturasını kapatmalı: %d %v", c, d)
	}
	if dd, _ := balanceOf(t, e, tok, "320"); dd != 24000 {
		t.Errorf("320 borç: %v", dd)
	}
	if _, cc := balanceOf(t, e, tok, "100"); cc != 24000 {
		t.Errorf("100 alacak: %v", cc)
	}
}

func TestCannotCancelInvoiceWithAllocations(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	inv := e.invoiceAt(t, tok, cid, "2026-08-01", "2026-09-01", 10000)
	_, p := e.pay(t, tok, cid, 5000, "2026-09-05")
	id := inv["id"].(string)
	if code, m, _ := e.call("POST", "/invoices/"+id+"/cancel", nil, tok, nil); code != 409 || m["error"].(map[string]any)["code"] != "has_payments" {
		t.Fatalf("mahsup edilmiş fatura iptal edilememeli: %d %v", code, m)
	}
	e.call("DELETE", "/payments/"+p["id"].(string), nil, tok, nil)
	if code, _, _ := e.call("POST", "/invoices/"+id+"/cancel", nil, tok, nil); code != 200 {
		t.Fatalf("ödeme silinince iptal edilebilmeli: %d", code)
	}
}

func TestCancelledInvoiceIsSkippedByAllocation(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	i1 := e.invoiceAt(t, tok, cid, "2026-08-01", "2026-09-01", 10000)
	i2 := e.invoiceAt(t, tok, cid, "2026-08-10", "2026-09-10", 10000)
	e.call("POST", "/invoices/"+i1["id"].(string)+"/cancel", nil, tok, nil)
	_, p := e.pay(t, tok, cid, 12000, "2026-09-05")
	if a := e.invoice(t, tok, i2["id"].(string)); a["open"] != 0.0 {
		t.Fatalf("iptal edilen atlanmalı, ikinci fatura kapanmalı: %v (%v)", a, p)
	}
}

func TestPaymentInClosedPeriodRejected(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	e.invoiceAt(t, tok, cid, "2026-08-01", "2026-09-01", 10000)
	e.call("POST", "/periods/close", map[string]string{"through": "2026-09-30"}, tok, nil)
	code, m := e.pay(t, tok, cid, 5000, "2026-09-15")
	if code != 409 || m["error"].(map[string]any)["code"] != "period_closed" {
		t.Fatalf("kapalı döneme ödeme reddedilmeli: %d %v", code, m)
	}
	// Reddedilen ödeme mahsup bırakmamalı
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM payment_allocations`).Scan(&n)
	if n != 0 {
		t.Fatalf("geri alınmış ödeme mahsup bırakmamalı: %d", n)
	}
}

func TestPaymentValidation(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cust := e.newContact(t, tok, "customer")
	supp := e.newContact(t, tok, "supplier")
	ok := map[string]any{"kind": "receipt", "contact_id": cust, "method": "cash", "date": "2026-09-05", "amount": 100}
	mut := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range ok {
			m[a] = b
		}
		m[k] = v
		return m
	}
	cases := map[string]struct {
		body map[string]any
		want int
	}{
		"sıfır tutar":      {mut("amount", 0), 400},
		"negatif tutar":    {mut("amount", -5), 400},
		"geçersiz tür":     {mut("kind", "gift"), 400},
		"geçersiz yöntem":  {mut("method", "bitcoin"), 400},
		"hatalı tarih":     {mut("date", "5 Eylül"), 400},
		"tedarikçiye tahs": {mut("contact_id", supp), 400},
		"bilinmeyen cari":  {mut("contact_id", "00000000-0000-0000-0000-000000000000"), 404},
		"geçerli":          {ok, 201},
	}
	for name, c := range cases {
		if code, m, _ := e.call("POST", "/payments", c.body, tok, nil); code != c.want {
			t.Errorf("%s: %d bekleniyordu, %d (%v)", name, c.want, code, m)
		}
	}
}

func TestPaymentIdempotency(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	e.invoiceAt(t, tok, cid, "2026-08-01", "2026-09-01", 10000)
	h := map[string]string{"Idempotency-Key": "odeme-1"}
	body := map[string]any{"kind": "receipt", "contact_id": cid, "method": "cash", "date": "2026-09-05", "amount": 7000}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, m, _ := e.call("POST", "/payments", body, tok, h); code != 201 {
				t.Errorf("201 bekleniyordu: %d %v", code, m)
			}
		}()
	}
	wg.Wait()
	var n int
	var sum int64
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*), coalesce(sum(amount),0) FROM payments`).Scan(&n, &sum)
	if n != 1 || sum != 7000 {
		t.Fatalf("8 eşzamanlı istek tek ödeme üretmeli: %d ödeme, toplam %d", n, sum)
	}
}

// T-3 yarış kanıtı: aynı cariye eşzamanlı tahsilatlar hiçbir faturayı fazla kapatamaz.
func TestConcurrentPaymentsNeverOverAllocate(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	const n = 10
	for i := 0; i < n; i++ {
		e.invoiceAt(t, tok, cid, fmt.Sprintf("2026-08-%02d", i+1), "2026-09-30", 10000) // brüt 12000
	}
	var wg sync.WaitGroup
	for i := 0; i < 15; i++ { // 15 x 9000 = 135000; faturalar toplamı 120000
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, m := e.pay(t, tok, cid, 9000, "2026-09-05"); code != 201 {
				t.Errorf("ödeme: %d %v", code, m)
			}
		}()
	}
	wg.Wait()
	ctx := context.Background()
	var over int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM invoices i WHERE
		(SELECT coalesce(sum(amount),0) FROM payment_allocations a WHERE a.invoice_id=i.id AND a.reversed_at IS NULL) > i.gross_total`).Scan(&over)
	if over != 0 {
		t.Fatalf("%d fatura fazla mahsup edilmiş", over)
	}
	var allocated, advance int64
	_ = e.pool.QueryRow(ctx, `SELECT coalesce(sum(amount),0) FROM payment_allocations WHERE reversed_at IS NULL`).Scan(&allocated)
	_ = e.pool.QueryRow(ctx, `SELECT coalesce(sum(amount),0) FROM payments`).Scan(&advance)
	if allocated != 120000 || advance-allocated != 15000 {
		t.Fatalf("mahsup %d (120000 bekleniyordu), avans %d (15000 bekleniyordu)", allocated, advance-allocated)
	}
	var open int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM invoices i WHERE i.gross_total >
		(SELECT coalesce(sum(amount),0) FROM payment_allocations a WHERE a.invoice_id=i.id AND a.reversed_at IS NULL)`).Scan(&open)
	if open != 0 {
		t.Fatalf("tüm faturalar kapanmış olmalı: %d açık", open)
	}
	_, tb, _ := e.call("GET", "/reports/trial-balance", nil, tok, nil)
	if tb["balanced"] != true {
		t.Fatalf("mizan dengesiz: %v", tb)
	}
}

func TestDatabaseRejectsOverAllocationViaRawSQL(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	inv := e.invoiceAt(t, tok, cid, "2026-08-01", "2026-09-01", 10000)
	_, p := e.pay(t, tok, cid, 12000, "2026-09-05")
	_ = p
	ctx := context.Background()
	tx, _ := e.pool.Begin(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO payment_allocations(business_id,payment_id,invoice_id,amount)
		VALUES ($1::uuid,$2::uuid,$3::uuid,1)`, e.user.BusinessID, p["id"], inv["id"]); err != nil {
		t.Fatal(err)
	}
	err := tx.Commit(ctx)
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "DD005" {
		t.Fatalf("aşırı mahsup DD005 ile reddedilmeli: %v", err)
	}
	for _, q := range []string{`UPDATE payment_allocations SET amount = 1`, `DELETE FROM payment_allocations`, `UPDATE payments SET amount = 1`, `DELETE FROM payments`} {
		if _, err := e.pool.Exec(ctx, q); err == nil {
			t.Errorf("%q reddedilmeliydi", q)
		}
	}
}

func TestAgingBuckets(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	asOf := "2026-10-25"
	// Her fatura ayrı cari: vade tarihleri rapor gününe göre 0,30,31,60,61,90,91 gün geçmiş + henüz vadesi gelmemiş
	type c struct {
		due    string
		bucket string
	}
	cases := []c{
		{"2026-11-05", "0-30"}, // vadesi gelmemiş
		{"2026-10-25", "0-30"}, // bugün vadesi dolan (0 gün)
		{"2026-09-25", "0-30"}, // 30 gün
		{"2026-09-24", "31-60"},
		{"2026-08-26", "31-60"}, // 60 gün
		{"2026-08-25", "61-90"},
		{"2026-07-27", "61-90"}, // 90 gün
		{"2026-07-26", "90+"},   // 91 gün
	}
	want := map[string]int64{"0-30": 0, "31-60": 0, "61-90": 0, "90+": 0}
	for i, cc := range cases {
		cid := e.newContact(t, tok, "customer")
		e.invoiceAt(t, tok, cid, "2026-01-02", cc.due, 1000*(i+1)) // brüt 1200*(i+1)
		want[cc.bucket] += int64(1200 * (i + 1))
	}
	code, m, _ := e.call("GET", "/reports/aging?as_of="+asOf+"&detail=1", nil, tok, nil)
	if code != 200 {
		t.Fatalf("rapor: %d %v", code, m)
	}
	got := m["buckets"].(map[string]any)
	var total int64
	for k, v := range want {
		total += v
		if got[k] != float64(v) {
			t.Errorf("kova %s: %d bekleniyordu, %v geldi", k, v, got[k])
		}
	}
	if m["total"] != float64(total) {
		t.Errorf("toplam: %v / %d", m["total"], total)
	}
	for _, inv := range m["invoices"].([]any) {
		x := inv.(map[string]any)
		days := int(x["days_past_due"].(float64))
		exp := map[bool]string{true: "0-30"}[days <= 30]
		switch {
		case days > 90:
			exp = "90+"
		case days > 60:
			exp = "61-90"
		case days > 30:
			exp = "31-60"
		}
		if x["bucket"] != exp {
			t.Errorf("%v: %d gün için %s bekleniyordu, %v geldi", x["number"], days, exp, x["bucket"])
		}
	}
}

func TestAgingReflectsPartialPaymentAndAsOfDate(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	e.invoiceAt(t, tok, cid, "2026-06-01", "2026-07-01", 10000) // brüt 12000, 116 gün gecikmiş (25 Eki)
	e.pay(t, tok, cid, 5000, "2026-09-10")
	_, now, _ := e.call("GET", "/reports/aging?as_of=2026-10-25", nil, tok, nil)
	if now["buckets"].(map[string]any)["90+"] != 7000.0 {
		t.Fatalf("açık 7000 olmalı: %v", now["buckets"])
	}
	// Ödemeden ÖNCEki bir tarihte rapor: ödeme henüz yapılmamıştı
	_, before, _ := e.call("GET", "/reports/aging?as_of=2026-09-01", nil, tok, nil)
	if before["buckets"].(map[string]any)["61-90"] != 12000.0 {
		t.Fatalf("1 Eylül itibarıyla 12000 açık ve 62 gün gecikmiş olmalı: %v", before["buckets"])
	}
	// Tamamen ödenen fatura raporda görünmez
	e.pay(t, tok, cid, 7000, "2026-10-01")
	_, paid, _ := e.call("GET", "/reports/aging?as_of=2026-10-25", nil, tok, nil)
	if paid["total"] != 0.0 {
		t.Fatalf("kapanan fatura raporda olmamalı: %v", paid["total"])
	}
}

func TestPayableAging(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "supplier")
	b := saleBody(cid, "2026-06-01")
	b["kind"] = "purchase"
	b["due_date"] = "2026-08-01"
	e.newInvoice(t, tok, b)
	_, m, _ := e.call("GET", "/reports/aging?kind=payable&as_of=2026-10-25", nil, tok, nil)
	if m["buckets"].(map[string]any)["61-90"] != 24000.0 { // 85 gün
		t.Fatalf("borç yaşlandırma: %v", m["buckets"])
	}
	_, r, _ := e.call("GET", "/reports/aging?as_of=2026-10-25", nil, tok, nil)
	if r["total"] != 0.0 {
		t.Fatalf("alacak raporu alış faturasını içermemeli: %v", r["total"])
	}
}

func TestStatementsAndCustomerRole(t *testing.T) {
	e := setup(t)
	owner := e.token(e.user)
	cid := e.newContact(t, owner, "customer")
	e.invoiceAt(t, owner, cid, "2026-08-01", "2026-09-01", 10000)
	e.pay(t, owner, cid, 5000, "2026-09-05")

	other := e.newContact(t, owner, "customer")
	e.invoiceAt(t, owner, other, "2026-08-01", "2026-09-01", 99900)

	cu := testutil.CreateUser(t, e.pool, e.user.BusinessID, "musteri@t.test", "x-parola-9", auth.RoleCustomer)
	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET contact_id=$1::uuid WHERE id=$2::uuid`, cid, cu.ID); err != nil {
		t.Fatal(err)
	}
	cust := e.token(cu)
	code, st, _ := e.call("GET", "/me/statement", nil, cust, nil)
	if code != 200 || st["balance"] != 7000.0 || len(st["rows"].([]any)) != 2 {
		t.Fatalf("müşteri ekstresi: %d %v", code, st)
	}
	// Müşteri başkasının ekstresini ve yönetim uçlarını göremez
	if code, _, _ := e.call("GET", "/contacts/"+other+"/statement", nil, cust, nil); code != 403 {
		t.Errorf("müşteri başka ekstreye erişemez: %d", code)
	}
	if code, _, _ := e.call("POST", "/payments", map[string]any{"kind": "receipt", "contact_id": cid, "method": "cash", "date": "2026-09-05", "amount": 1}, cust, nil); code != 403 {
		t.Errorf("müşteri ödeme giremez: %d", code)
	}
	// Çalışan/sahip /me/statement kullanamaz (müşteri rolüne özel)
	if code, _, _ := e.call("GET", "/me/statement", nil, owner, nil); code != 403 {
		t.Errorf("/me/statement yalnızca müşteri rolüne açık: %d", code)
	}
	// Personel için ekstre
	if code, st, _ := e.call("GET", "/contacts/"+cid+"/statement", nil, owner, nil); code != 200 || st["balance"] != 7000.0 {
		t.Errorf("personel ekstresi: %d %v", code, st)
	}
}

func TestVATSummaryMatchesLedger(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cust := e.newContact(t, tok, "customer")
	supp := e.newContact(t, tok, "supplier")
	e.invoiceAt(t, tok, cust, "2026-10-05", "2026-11-05", 10000) // KDV 2000
	b := saleBody(cust, "2026-10-06")
	b["lines"] = []map[string]any{{"description": "a", "quantity": "1", "unit_price": 5000, "vat_rate": 10}, {"description": "b", "quantity": "1", "unit_price": 3000, "vat_rate": 1}}
	e.newInvoice(t, tok, b) // KDV 500 + 30
	pb := saleBody(supp, "2026-10-07")
	pb["kind"] = "purchase"
	e.newInvoice(t, tok, pb) // alış KDV 4000
	cancelled := e.invoiceAt(t, tok, cust, "2026-10-08", "2026-11-08", 70000)
	e.call("POST", "/invoices/"+cancelled["id"].(string)+"/cancel", nil, tok, nil)

	_, m, _ := e.call("GET", "/reports/vat-summary?from=2026-10-01&to=2026-10-31", nil, tok, nil)
	if m["output_vat"] != 2530.0 || m["input_vat"] != 4000.0 || m["payable"] != -1470.0 {
		t.Fatalf("KDV özeti (iptal edilen hariç olmalı): %v", m)
	}
	// Defter ile tutarlılık: 391 net alacak - 191 net borç = ödenecek KDV
	d391, c391 := balanceOf(t, e, tok, "391")
	d191, c191 := balanceOf(t, e, tok, "191")
	if (c391-d391)-(d191-c191) != -1470 {
		t.Fatalf("defter KDV bakiyesi özetle uyuşmuyor: 391=%v/%v 191=%v/%v", d391, c391, d191, c191)
	}
}
