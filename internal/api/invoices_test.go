package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"esnafdefter/internal/auth"
	"esnafdefter/internal/testutil"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func (e *env) token(u testutil.User) string {
	tok, _ := auth.NewTokenIssuer(testSecret, time.Hour).IssueAccess(u.ID, u.BusinessID, u.Role)
	return tok
}

// call başlık ve ham gövde döndüren genel istek yardımcısı.
func (e *env) call(method, path string, body any, bearer string, hdr map[string]string) (int, map[string]any, http.Header) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, nil
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m, resp.Header
}

func (e *env) newContact(t *testing.T, tok, kind string) string {
	t.Helper()
	code, m, _ := e.call("POST", "/contacts", map[string]any{"kind": kind, "name": "Test " + kind}, tok, nil)
	if code != 201 {
		t.Fatalf("cari oluşturulamadı: %d %v", code, m)
	}
	return m["id"].(string)
}

func saleBody(contact, issue string) map[string]any {
	return map[string]any{
		"kind": "sale", "contact_id": contact, "issue_date": issue, "due_date": "2026-12-31",
		"lines": []map[string]any{{"description": "Ürün A", "quantity": "2", "unit_price": 10000, "vat_rate": 20}},
	}
}

func (e *env) newInvoice(t *testing.T, tok string, body map[string]any) map[string]any {
	t.Helper()
	code, m, _ := e.call("POST", "/invoices", body, tok, nil)
	if code != 201 {
		t.Fatalf("fatura oluşturulamadı: %d %v", code, m)
	}
	return m
}

func balanceOf(t *testing.T, e *env, tok, code string) (debit, credit float64) {
	t.Helper()
	c, m, _ := e.call("GET", "/reports/trial-balance", nil, tok, nil)
	if c != 200 {
		t.Fatalf("mizan: %d %v", c, m)
	}
	for _, a := range m["accounts"].([]any) {
		acc := a.(map[string]any)
		if acc["code"] == code {
			return acc["debit"].(float64), acc["credit"].(float64)
		}
	}
	t.Fatalf("hesap %s yok", code)
	return
}

func TestSaleInvoiceTotalsAndJournal(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	body := saleBody(cid, "2026-10-10")
	body["general_discount"] = 500
	body["lines"] = []map[string]any{
		{"description": "Ürün A", "quantity": "3", "unit_price": 5000, "vat_rate": 10},
		{"description": "Ürün B", "quantity": "1", "unit_price": 1999, "vat_rate": 20},
	}
	inv := e.newInvoice(t, tok, body)
	// Elle hesap: satır tutarları 15000 + 1999, 500 iskonto -> paylar 441 + 59 (en büyük kalan)
	if inv["net_total"] != 16499.0 || inv["vat_total"] != 1844.0 || inv["gross_total"] != 18343.0 {
		t.Fatalf("toplamlar: %v", inv)
	}
	if inv["number"] != "A-2026-000001" {
		t.Fatalf("numara: %v", inv["number"])
	}
	if d, _ := balanceOf(t, e, tok, "120"); d != 18343 {
		t.Errorf("120 borç: %v", d)
	}
	if _, c := balanceOf(t, e, tok, "600"); c != 16499 {
		t.Errorf("600 alacak: %v", c)
	}
	if _, c := balanceOf(t, e, tok, "391"); c != 1844 {
		t.Errorf("391 alacak: %v", c)
	}
}

func TestPurchaseInvoiceJournalAndSeries(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "supplier")
	body := saleBody(cid, "2026-10-10")
	body["kind"] = "purchase"
	inv := e.newInvoice(t, tok, body)
	if inv["series"] != "AL" || inv["number"] != "AL-2026-000001" {
		t.Fatalf("alış serisi: %v", inv)
	}
	if d, _ := balanceOf(t, e, tok, "153"); d != 20000 {
		t.Errorf("153 borç: %v", d)
	}
	if d, _ := balanceOf(t, e, tok, "191"); d != 4000 {
		t.Errorf("191 borç: %v", d)
	}
	if _, c := balanceOf(t, e, tok, "320"); c != 24000 {
		t.Errorf("320 alacak: %v", c)
	}
}

func TestVATInclusiveInvoiceViaAPI(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	body := saleBody(cid, "2026-10-10")
	body["prices_include_vat"] = true
	body["lines"] = []map[string]any{{"description": "KDV dahil", "quantity": "2.5", "unit_price": 12000, "vat_rate": 20}}
	inv := e.newInvoice(t, tok, body)
	// 2,5 x 120,00 = 300,00 brüt -> net 250,00, KDV 50,00
	if inv["gross_total"] != 30000.0 || inv["net_total"] != 25000.0 || inv["vat_total"] != 5000.0 {
		t.Fatalf("KDV dahil: %v", inv)
	}
}

func TestCancelInvoiceReversesJournal(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	inv := e.newInvoice(t, tok, saleBody(cid, "2026-10-10"))
	id := inv["id"].(string)

	code, m, _ := e.call("POST", "/invoices/"+id+"/cancel", nil, tok, nil)
	if code != 200 || m["status"] != "cancelled" {
		t.Fatalf("iptal: %d %v", code, m)
	}
	for _, acc := range []string{"120", "600", "391"} {
		d, c := balanceOf(t, e, tok, acc)
		if d != c {
			t.Errorf("hesap %s ters kayıttan sonra sıfırlanmalı: borç %v alacak %v", acc, d, c)
		}
	}
	if code, m, _ := e.call("POST", "/invoices/"+id+"/cancel", nil, tok, nil); code != 409 || m["error"].(map[string]any)["code"] != "already_cancelled" {
		t.Errorf("ikinci iptal 409 olmalı: %d %v", code, m)
	}
	code, m, _ = e.call("GET", "/invoices/"+id+"/journal", nil, tok, nil)
	if code != 200 || len(m["entries"].([]any)) != 2 {
		t.Errorf("orijinal + ters kayıt bekleniyordu: %d %v", code, m)
	}
}

func TestCancelRespectsClosedPeriod(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	inv := e.newInvoice(t, tok, saleBody(cid, "2026-09-15"))
	if code, m, _ := e.call("POST", "/periods/close", map[string]string{"through": "2026-09-30"}, tok, nil); code != 200 {
		t.Fatalf("dönem kapatma: %d %v", code, m)
	}
	id := inv["id"].(string)
	code, m, _ := e.call("POST", "/invoices/"+id+"/cancel", map[string]string{"date": "2026-09-20"}, tok, nil)
	if code != 409 || m["error"].(map[string]any)["code"] != "period_closed" {
		t.Fatalf("kapalı döneme ters kayıt reddedilmeli: %d %v", code, m)
	}
	// Başarısız iptal hiçbir şeyi değiştirmemeli
	_, got, _ := e.call("GET", "/invoices/"+id, nil, tok, nil)
	if got["status"] != "issued" {
		t.Fatalf("fatura hâlâ 'issued' olmalı: %v", got["status"])
	}
	// Açık bir tarihle iptal edilebilir
	if code, _, _ := e.call("POST", "/invoices/"+id+"/cancel", map[string]string{"date": "2026-10-05"}, tok, nil); code != 200 {
		t.Fatalf("açık tarihle iptal çalışmalı: %d", code)
	}
}

func TestClosedPeriodRejectsNewInvoiceAndCannotReopen(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	e.call("POST", "/periods/close", map[string]string{"through": "2026-09-30"}, tok, nil)
	code, m, _ := e.call("POST", "/invoices", saleBody(cid, "2026-09-15"), tok, nil)
	if code != 409 || m["error"].(map[string]any)["code"] != "period_closed" {
		t.Fatalf("kapalı dönem faturası 409 olmalı: %d %v", code, m)
	}
	if code, _, _ := e.call("POST", "/periods/close", map[string]string{"through": "2026-08-31"}, tok, nil); code != 409 {
		t.Fatalf("dönem geri açılamamalı: %d", code)
	}
	if code, _, _ := e.call("POST", "/periods/close", map[string]string{"through": "2999-01-01"}, tok, nil); code != 400 {
		t.Fatalf("gelecek tarih kapatılamamalı: %d", code)
	}
}

// T-2 kanıtı: eşzamanlı üretimde numaralar boşluksuz ve çiftsiz.
func TestInvoiceNumbersAreGapFreeUnderConcurrency(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	const n = 40
	var mu sync.Mutex
	var seqs []int
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, m, _ := e.call("POST", "/invoices", saleBody(cid, "2026-10-10"), tok, nil)
			if code != 201 {
				t.Errorf("istek başarısız: %d %v", code, m)
				return
			}
			mu.Lock()
			seqs = append(seqs, int(m["seq"].(float64)))
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Ints(seqs)
	if len(seqs) != n {
		t.Fatalf("%d fatura bekleniyordu: %d", n, len(seqs))
	}
	for i, s := range seqs {
		if s != i+1 {
			t.Fatalf("numara dizisi 1..%d olmalı; %d. eleman %d: %v", n, i+1, s, seqs)
		}
	}
}

func TestFailedInvoiceDoesNotConsumeNumber(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	e.call("POST", "/periods/close", map[string]string{"through": "2026-09-30"}, tok, nil)
	for i := 0; i < 3; i++ { // numara alındıktan SONRA (yevmiye aşamasında) başarısız olan istekler
		if code, _, _ := e.call("POST", "/invoices", saleBody(cid, "2026-09-15"), tok, nil); code != 409 {
			t.Fatalf("409 bekleniyordu: %d", code)
		}
	}
	inv := e.newInvoice(t, tok, saleBody(cid, "2026-10-10"))
	if inv["seq"] != 1.0 {
		t.Fatalf("başarısız denemeler numara tüketmemeli; seq=%v", inv["seq"])
	}
}

func TestCountersAreIndependentPerSeriesAndYear(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "both")
	a := e.newInvoice(t, tok, saleBody(cid, "2025-06-01"))
	b := e.newInvoice(t, tok, saleBody(cid, "2026-06-01"))
	body := saleBody(cid, "2026-06-02")
	body["series"] = "B"
	c := e.newInvoice(t, tok, body)
	if a["number"] != "A-2025-000001" || b["number"] != "A-2026-000001" || c["number"] != "B-2026-000001" {
		t.Fatalf("sayaçlar bağımsız olmalı: %v %v %v", a["number"], b["number"], c["number"])
	}
}

func TestIdempotentRetryReturnsSameInvoice(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	h := map[string]string{"Idempotency-Key": "siparis-123"}
	c1, m1, _ := e.call("POST", "/invoices", saleBody(cid, "2026-10-10"), tok, h)
	c2, m2, hd2 := e.call("POST", "/invoices", saleBody(cid, "2026-10-10"), tok, h)
	if c1 != 201 || c2 != 201 || m1["id"] != m2["id"] || hd2.Get("Idempotent-Replay") != "true" {
		t.Fatalf("aynı yanıt bekleniyordu: %d %v | %d %v", c1, m1, c2, m2)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM invoices`).Scan(&n)
	if n != 1 {
		t.Fatalf("tek fatura bekleniyordu: %d", n)
	}
}

func TestIdempotencyConcurrentSameKeyCreatesOneInvoice(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	h := map[string]string{"Idempotency-Key": "cift-tiklama"}
	var wg sync.WaitGroup
	ids := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, m, _ := e.call("POST", "/invoices", saleBody(cid, "2026-10-10"), tok, h)
			if code != 201 {
				t.Errorf("201 bekleniyordu: %d %v", code, m)
				return
			}
			ids <- m["id"].(string)
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM invoices`).Scan(&n)
	if n != 1 || len(seen) != 1 {
		t.Fatalf("10 eşzamanlı istek tek fatura üretmeli: veritabanı=%d, farklı yanıt=%d", n, len(seen))
	}
}

func TestIdempotencyKeyWithDifferentBodyIs422(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	h := map[string]string{"Idempotency-Key": "k1"}
	e.call("POST", "/invoices", saleBody(cid, "2026-10-10"), tok, h)
	other := saleBody(cid, "2026-10-10")
	other["general_discount"] = 100
	code, m, _ := e.call("POST", "/invoices", other, tok, h)
	if code != 422 || m["error"].(map[string]any)["code"] != "idempotency_key_reused" {
		t.Fatalf("422 bekleniyordu: %d %v", code, m)
	}
}

func TestFailedRequestDoesNotBurnIdempotencyKey(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	h := map[string]string{"Idempotency-Key": "k-hata"}
	bad := saleBody(cid, "2026-10-10")
	bad["lines"] = []map[string]any{{"description": "x", "quantity": "1", "unit_price": 100, "vat_rate": 18}}
	if code, _, _ := e.call("POST", "/invoices", bad, tok, h); code != 400 {
		t.Fatalf("400 bekleniyordu: %d", code)
	}
	if code, _, _ := e.call("POST", "/invoices", saleBody(cid, "2026-10-10"), tok, h); code != 201 {
		t.Fatalf("düzeltilmiş istek aynı anahtarla çalışmalı: %d", code)
	}
}

func TestTenantIsolation(t *testing.T) {
	e := setup(t)
	tokA := e.token(e.user)
	cidA := e.newContact(t, tokA, "customer")
	invA := e.newInvoice(t, tokA, saleBody(cidA, "2026-10-10"))

	bizB := testutil.CreateBusiness(t, e.pool, "Diğer Esnaf", "9999999999")
	userB := testutil.CreateUser(t, e.pool, bizB, "b@diger.test", "parola-b", auth.RoleOwner)
	tokB := e.token(userB)

	if code, _, _ := e.call("GET", "/invoices/"+invA["id"].(string), nil, tokB, nil); code != 404 {
		t.Errorf("başka işletmenin faturası 404 olmalı: %d", code)
	}
	if code, _, _ := e.call("GET", "/contacts/"+cidA, nil, tokB, nil); code != 404 {
		t.Errorf("başka işletmenin carisi 404 olmalı: %d", code)
	}
	if code, _, _ := e.call("POST", "/invoices/"+invA["id"].(string)+"/cancel", nil, tokB, nil); code != 404 {
		t.Errorf("başka işletmenin faturası iptal edilememeli: %d", code)
	}
	if code, m, _ := e.call("POST", "/invoices", saleBody(cidA, "2026-10-10"), tokB, nil); code != 404 {
		t.Errorf("başka işletmenin carisiyle fatura kesilememeli: %d %v", code, m)
	}
	_, list, _ := e.call("GET", "/invoices", nil, tokB, nil)
	if len(list["items"].([]any)) != 0 {
		t.Errorf("B'nin listesi boş olmalı: %v", list)
	}
	if d, _ := balanceOf(t, e, tokB, "120"); d != 0 {
		t.Errorf("B'nin mizanında A'nın hareketi görünmemeli: %v", d)
	}
}

func TestRoleMatrix(t *testing.T) {
	e := setup(t)
	biz := e.user.BusinessID
	acc := e.token(testutil.CreateUser(t, e.pool, biz, "muh@t.test", "x-parola-1", auth.RoleAccountant))
	emp := e.token(testutil.CreateUser(t, e.pool, biz, "calisan@t.test", "x-parola-2", auth.RoleEmployee))
	cus := e.token(testutil.CreateUser(t, e.pool, biz, "musteri@t.test", "x-parola-3", auth.RoleCustomer))
	owner := e.token(e.user)
	cid := e.newContact(t, owner, "customer")

	type tc struct {
		name, method, path, tok string
		body                    any
		want                    int
	}
	inv := e.newInvoice(t, owner, saleBody(cid, "2026-10-10"))
	invPath := "/invoices/" + inv["id"].(string)
	cases := []tc{
		{"müşteri fatura kesemez", "POST", "/invoices", cus, saleBody(cid, "2026-10-10"), 403},
		{"müşteri cari listeleyemez", "GET", "/contacts", cus, nil, 403},
		{"müşteri mizan göremez", "GET", "/reports/trial-balance", cus, nil, 403},
		{"çalışan fatura kesebilir", "POST", "/invoices", emp, saleBody(cid, "2026-10-10"), 201},
		{"çalışan iptal edemez", "POST", invPath + "/cancel", emp, nil, 403},
		{"çalışan dönem kapatamaz", "POST", "/periods/close", emp, map[string]string{"through": "2026-01-31"}, 403},
		{"çalışan mizan göremez", "GET", "/reports/trial-balance", emp, nil, 403},
		{"çalışan faturayı görebilir", "GET", invPath, emp, nil, 200},
		{"muhasebeci mizan görebilir", "GET", "/reports/trial-balance", acc, nil, 200},
		{"muhasebeci dönem kapatabilir", "POST", "/periods/close", acc, map[string]string{"through": "2026-01-31"}, 200},
		{"muhasebeci iptal edebilir", "POST", invPath + "/cancel", acc, nil, 200},
		{"belirteçsiz 401", "GET", "/invoices", "", nil, 401},
	}
	for _, c := range cases {
		if code, m, _ := e.call(c.method, c.path, c.body, c.tok, nil); code != c.want {
			t.Errorf("%s: %d bekleniyordu, %d geldi (%v)", c.name, c.want, code, m)
		}
	}
}

func TestInvoiceValidationErrors(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cust := e.newContact(t, tok, "customer")
	supp := e.newContact(t, tok, "supplier")
	mk := func(mut func(map[string]any)) map[string]any {
		b := saleBody(cust, "2026-10-10")
		mut(b)
		return b
	}
	line := func(q string, price, rate int) []map[string]any {
		return []map[string]any{{"description": "x", "quantity": q, "unit_price": price, "vat_rate": rate}}
	}
	cases := map[string]struct {
		body map[string]any
		want int
		code string
	}{
		"KDV %18":              {mk(func(b map[string]any) { b["lines"] = line("1", 100, 18) }), 400, "invalid_vat_rate"},
		"4 ondalıklı miktar":   {mk(func(b map[string]any) { b["lines"] = line("1.2345", 100, 20) }), 400, "validation_error"},
		"satırsız":             {mk(func(b map[string]any) { b["lines"] = []map[string]any{} }), 400, "validation_error"},
		"vade < fatura tarihi": {mk(func(b map[string]any) { b["due_date"] = "2026-10-01" }), 400, "validation_error"},
		"hatalı tarih":         {mk(func(b map[string]any) { b["issue_date"] = "10.10.2026" }), 400, "validation_error"},
		"iskonto > toplam":     {mk(func(b map[string]any) { b["general_discount"] = 999999 }), 400, "discount_exceeds_total"},
		"sıfır toplam":         {mk(func(b map[string]any) { b["lines"] = line("1", 0, 20) }), 400, "zero_total"},
		"yanlış tür":           {mk(func(b map[string]any) { b["kind"] = "gift" }), 400, "validation_error"},
		"tedarikçiye satış":    {mk(func(b map[string]any) { b["contact_id"] = supp }), 400, "contact_kind_mismatch"},
		"bilinmeyen alan":      {mk(func(b map[string]any) { b["hack"] = true }), 400, "invalid_json"},
		"geçersiz cari id":     {mk(func(b map[string]any) { b["contact_id"] = "abc" }), 400, "validation_error"},
	}
	for name, c := range cases {
		code, m, _ := e.call("POST", "/invoices", c.body, tok, nil)
		got := ""
		if em, ok := m["error"].(map[string]any); ok {
			got, _ = em["code"].(string)
		}
		if code != c.want || got != c.code {
			t.Errorf("%s: %d/%s bekleniyordu, %d/%s geldi", name, c.want, c.code, code, got)
		}
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM invoices`).Scan(&n)
	if n != 0 {
		t.Errorf("hatalı isteklerden fatura oluşmamalı: %d", n)
	}
}

func TestInvoicesAreImmutableInDatabase(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	e.newInvoice(t, tok, saleBody(cid, "2026-10-10"))
	for _, q := range []string{
		`UPDATE invoices SET gross_total = gross_total + 1`,
		`UPDATE invoices SET number = 99`,
		`UPDATE invoices SET status = 'issued'`,
		`DELETE FROM invoices`,
		`UPDATE invoice_lines SET net_amount = 0`,
		`DELETE FROM invoice_lines`,
	} {
		if _, err := e.pool.Exec(context.Background(), q); err == nil {
			t.Errorf("%q reddedilmeliydi", q)
		}
	}
}

func TestDatabaseRejectsInvoiceWhoseTotalsMismatchLines(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	cid := testutil.CreateContact(t, e.pool, e.user.BusinessID, "customer", "C")
	tx, _ := e.pool.Begin(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO invoices(business_id,kind,contact_id,series,year,number,issue_date,due_date,
		prices_include_vat,general_discount,net_total,vat_total,gross_total)
		VALUES ($1::uuid,'sale',$2::uuid,'A',2026,1,'2026-10-10','2026-10-10',false,0,100,0,100)`, e.user.BusinessID, cid); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invoice_lines(invoice_id,business_id,line_no,description,quantity_milli,unit_price,
		vat_rate,net_amount,vat_amount,gross_amount)
		SELECT id,business_id,1,'x',1000,50,20,50,0,50 FROM invoices`); err != nil {
		t.Fatal(err)
	}
	err := tx.Commit(ctx)
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "DD004" {
		t.Fatalf("toplam uyuşmazlığı DD004 ile reddedilmeliydi: %v", err)
	}
}

// T-1 kanıtı: karışık, eşzamanlı iş yükünden sonra defterler dengede kalır.
func TestBooksStayBalancedAfterConcurrentMixedWorkload(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cust := e.newContact(t, tok, "customer")
	supp := e.newContact(t, tok, "supplier")
	var wg sync.WaitGroup
	ids := make(chan string, 60)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := saleBody(cust, "2026-10-10")
			if i%3 == 0 {
				b = saleBody(supp, "2026-10-10")
				b["kind"] = "purchase"
			}
			b["general_discount"] = i * 7
			b["lines"] = []map[string]any{
				{"description": "a", "quantity": fmt.Sprintf("%d.%03d", i+1, i*37%1000), "unit_price": 1234 + i*111, "vat_rate": []int{1, 10, 20}[i%3]},
				{"description": "b", "quantity": "1", "unit_price": 999 + i, "vat_rate": 20, "discount": i},
			}
			code, m, _ := e.call("POST", "/invoices", b, tok, nil)
			if code != 201 {
				t.Errorf("fatura %d: %d %v", i, code, m)
				return
			}
			ids <- m["id"].(string)
		}(i)
	}
	wg.Wait()
	close(ids)
	var all []string
	for id := range ids {
		all = append(all, id)
	}
	for i, id := range all { // yarısını eşzamanlı iptal et
		if i%2 == 0 {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				if code, m, _ := e.call("POST", "/invoices/"+id+"/cancel", nil, tok, nil); code != 200 {
					t.Errorf("iptal: %d %v", code, m)
				}
			}(id)
		}
	}
	wg.Wait()

	c, tb, _ := e.call("GET", "/reports/trial-balance", nil, tok, nil)
	if c != 200 || tb["balanced"] != true || tb["total_debit"] != tb["total_credit"] {
		t.Fatalf("mizan dengesiz: %v", tb)
	}
	// Bağımsız SQL denetimleri
	ctx := context.Background()
	var diff int64
	_ = e.pool.QueryRow(ctx, `SELECT coalesce(sum(debit),0) - coalesce(sum(credit),0) FROM journal_lines`).Scan(&diff)
	if diff != 0 {
		t.Fatalf("toplam borç - toplam alacak = %d", diff)
	}
	var bad int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT entry_id FROM journal_lines GROUP BY entry_id
		HAVING sum(debit) <> sum(credit)) x`).Scan(&bad)
	if bad != 0 {
		t.Fatalf("%d dengesiz yevmiye kaydı var", bad)
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM journal_entries WHERE source_type='reversal'`).Scan(&n)
	if n != 15 {
		t.Fatalf("15 ters kayıt bekleniyordu: %d", n)
	}
}

func TestContactValidationAndPagination(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	if code, _, _ := e.call("POST", "/contacts", map[string]any{"kind": "alien", "name": "x"}, tok, nil); code != 400 {
		t.Errorf("geçersiz tür 400 olmalı: %d", code)
	}
	if code, _, _ := e.call("POST", "/contacts", map[string]any{"kind": "customer", "name": "  "}, tok, nil); code != 400 {
		t.Errorf("boş ad 400 olmalı: %d", code)
	}
	for i := 0; i < 5; i++ {
		e.newContact(t, tok, "customer")
	}
	seen := map[string]bool{}
	after := ""
	for page := 0; page < 10; page++ {
		path := "/contacts?limit=2"
		if after != "" {
			path += "&after=" + after
		}
		_, m, _ := e.call("GET", path, nil, tok, nil)
		for _, it := range m["items"].([]any) {
			id := it.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("sayfalamada tekrar eden kayıt: %s", id)
			}
			seen[id] = true
		}
		next, _ := m["next_after"].(string)
		if next == "" {
			break
		}
		after = next
	}
	if len(seen) != 5 {
		t.Fatalf("5 cari bekleniyordu: %d", len(seen))
	}
	if code, _, _ := e.call("GET", "/contacts?limit=999", nil, tok, nil); code != 400 {
		t.Errorf("limit sınırı: %d", code)
	}
}
