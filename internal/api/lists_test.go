package api_test

import (
	"fmt"
	"testing"

	"esnafdefter/internal/auth"
	"esnafdefter/internal/testutil"
)

// walk bir liste ucunu next_after ile sonuna kadar gezer ve tüm kayıtları sırayla döner.
func (e *env) walk(t *testing.T, tok, path string, limit int) []map[string]any {
	t.Helper()
	var all []map[string]any
	after := ""
	sep := "?"
	for i := 0; i < len(path); i++ {
		if path[i] == '?' {
			sep = "&"
		}
	}
	for page := 0; page < 100; page++ {
		url := fmt.Sprintf("%s%slimit=%d", path, sep, limit)
		if after != "" {
			url += "&after=" + after
		}
		code, m, _ := e.call("GET", url, nil, tok, nil)
		if code != 200 {
			t.Fatalf("%s: %d %v", url, code, m)
		}
		for _, it := range m["items"].([]any) {
			all = append(all, it.(map[string]any))
		}
		next, _ := m["next_after"].(string)
		if next == "" {
			return all
		}
		after = next
	}
	t.Fatal("sayfalama sonlanmadı")
	return nil
}

func TestInvoicesListedNewestFirstWithoutGapsOrDuplicates(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	var created []string
	for i := 0; i < 11; i++ {
		created = append(created, e.newInvoice(t, tok, saleBody(cid, "2026-10-10"))["id"].(string))
	}
	got := e.walk(t, tok, "/invoices", 4) // 4+4+3: sayfa sınırlarında kayıp/tekrar olmamalı
	if len(got) != 11 {
		t.Fatalf("11 fatura bekleniyordu: %d", len(got))
	}
	for i, inv := range got {
		want := created[len(created)-1-i] // en yeni önce
		if inv["id"] != want {
			t.Fatalf("%d. sıradaki fatura en yeni -> eski sırada olmalı", i+1)
		}
	}
}

func TestInvoiceFilters(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cust, supp := e.newContact(t, tok, "customer"), e.newContact(t, tok, "supplier")
	s1 := e.newInvoice(t, tok, saleBody(cust, "2026-10-10"))
	e.newInvoice(t, tok, saleBody(cust, "2026-10-10"))
	pb := saleBody(supp, "2026-10-10")
	pb["kind"] = "purchase"
	e.newInvoice(t, tok, pb)
	e.call("POST", "/invoices/"+s1["id"].(string)+"/cancel", nil, tok, nil)

	count := func(q string) int { return len(e.walk(t, tok, "/invoices"+q, 50)) }
	cases := map[string]int{"": 3, "?kind=sale": 2, "?kind=purchase": 1, "?status=cancelled": 1, "?status=issued": 2,
		"?contact_id=" + supp: 1, "?kind=sale&status=issued": 1}
	for q, want := range cases {
		if got := count(q); got != want {
			t.Errorf("%q: %d bekleniyordu, %d geldi", q, want, got)
		}
	}
	for _, bad := range []string{"?kind=gift", "?status=x", "?contact_id=abc", "?after=!!!", "?after=YWJj"} {
		if code, _, _ := e.call("GET", "/invoices"+bad, nil, tok, nil); code != 400 {
			t.Errorf("%q 400 olmalı: %d", bad, code)
		}
	}
}

func TestContactsSortedByNameSearchAndKindFilter(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	mk := func(kind, name string) {
		if code, m, _ := e.call("POST", "/contacts", map[string]any{"kind": kind, "name": name}, tok, nil); code != 201 {
			t.Fatalf("%d %v", code, m)
		}
	}
	mk("customer", "Zeytin Market")
	mk("customer", "Ahmet Bakkal")
	mk("supplier", "Mehmet Toptan")
	mk("both", "Çınar Ticaret")
	mk("customer", "Bakır Atölyesi")

	all := e.walk(t, tok, "/contacts", 2) // 3 sayfa
	var names []string
	for _, c := range all {
		names = append(names, c["name"].(string))
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("cariler ada göre sıralı olmalı: %v", names)
		}
	}
	if len(names) != 5 {
		t.Fatalf("5 cari bekleniyordu: %v", names)
	}
	if got := len(e.walk(t, tok, "/contacts?q=bak", 50)); got != 2 { // Bakkal + Bakır, büyük/küçük harf duyarsız
		t.Errorf("q=bak 2 sonuç vermeli: %d", got)
	}
	if got := len(e.walk(t, tok, "/contacts?kind=customer", 50)); got != 4 { // müşteri 3 + both
		t.Errorf("kind=customer müşteri ve both dönmeli: %d", got)
	}
	if got := len(e.walk(t, tok, "/contacts?kind=supplier", 50)); got != 2 { // tedarikçi + both
		t.Errorf("kind=supplier tedarikçi ve both dönmeli: %d", got)
	}
	if got := len(e.walk(t, tok, "/contacts?q=%25", 50)); got != 0 { // % joker olarak yorumlanmamalı
		t.Errorf("q=%% joker olmamalı: %d", got)
	}
}

func TestProductsSortedBySKUAndSearch(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	for _, sku := range []string{"C-3", "A-1", "B-2", "A-10"} {
		e.product(t, tok, sku, "product")
	}
	e.product(t, tok, "HIZMET-1", "service")
	all := e.walk(t, tok, "/products", 2)
	var skus []string
	for _, p := range all {
		skus = append(skus, p["sku"].(string))
	}
	for i := 1; i < len(skus); i++ {
		if skus[i-1] > skus[i] {
			t.Fatalf("ürünler SKU'ya göre sıralı olmalı: %v", skus)
		}
	}
	if got := len(e.walk(t, tok, "/products?q=a-1", 50)); got != 2 { // A-1, A-10
		t.Errorf("q=a-1: %d", got)
	}
	if got := len(e.walk(t, tok, "/products?kind=service", 50)); got != 1 {
		t.Errorf("kind=service: %d", got)
	}
}

func TestPaymentsListNewestFirstAndContactFilter(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	a, b := e.newContact(t, tok, "customer"), e.newContact(t, tok, "customer")
	var ids []string
	for i := 0; i < 5; i++ {
		c := a
		if i%2 == 1 {
			c = b
		}
		_, p := e.pay(t, tok, c, 1000+i, "2026-10-10")
		ids = append(ids, p["id"].(string))
	}
	got := e.walk(t, tok, "/payments", 2)
	if len(got) != 5 || got[0]["id"] != ids[4] || got[4]["id"] != ids[0] {
		t.Fatalf("ödemeler en yeni -> eski olmalı")
	}
	if n := len(e.walk(t, tok, "/payments?contact_id="+b, 50)); n != 2 {
		t.Errorf("contact_id süzgeci: %d", n)
	}
	if code, _, _ := e.call("GET", "/payments?kind=x", nil, tok, nil); code != 400 {
		t.Errorf("geçersiz kind 400 olmalı: %d", code)
	}
}

func TestDashboardMatchesLedger(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cust, supp := e.newContact(t, tok, "customer"), e.newContact(t, tok, "supplier")
	p := e.product(t, tok, "D-1", "product")
	e.newInvoice(t, tok, buyBody(supp, p, "2026-10-01", "10", 10000)) // alış: net 100000, brüt 120000
	e.newInvoice(t, tok, sellBody(cust, p, "2026-10-02", "4"))        // satış: net 100000 (4 x 25000), brüt 120000
	e.pay(t, tok, cust, 50000, "2026-10-03")
	e.call("POST", "/payments", map[string]any{"kind": "disbursement", "contact_id": supp, "method": "cash", "date": "2026-10-03", "amount": 20000}, tok, nil)

	code, d, _ := e.call("GET", "/dashboard", nil, tok, nil)
	if code != 200 {
		t.Fatalf("pano: %d %v", code, d)
	}
	n := func(k string) float64 { return d[k].(float64) }
	if n("receivable") != 70000 || n("payable") != 100000 {
		t.Errorf("açık alacak 70000 / borç 100000 olmalı: %v / %v", n("receivable"), n("payable"))
	}
	if n("cash") != 50000-20000 {
		t.Errorf("kasa+banka 30000 olmalı: %v", n("cash"))
	}
	// stok değeri = 153 bakiyesi = 6 adet x 100 TL
	if n("stock_value") != 60000 || e.acct(t, tok, "153") != 60000 {
		t.Errorf("stok değeri 60000 olmalı: %v", n("stock_value"))
	}
	counts := d["counts"].(map[string]any)
	if counts["invoices"] != 2.0 || counts["payments"] != 2.0 || counts["contacts"] != 2.0 || counts["products"] != 1.0 {
		t.Errorf("sayılar: %v", counts)
	}
	if len(d["recent_invoices"].([]any)) != 2 || len(d["recent_payments"].([]any)) != 2 {
		t.Errorf("son hareketler: %v", d)
	}
	// Müşteri rolü panoyu göremez
	cu := e.token(testutilUser(t, e, "mus@t.test", auth.RoleCustomer))
	if code, _, _ := e.call("GET", "/dashboard", nil, cu, nil); code != 403 {
		t.Errorf("müşteri pano göremez: %d", code)
	}
}

func TestMeIncludesBusinessAndEmail(t *testing.T) {
	e := setup(t)
	_, m, _ := e.call("GET", "/me", nil, e.token(e.user), nil)
	if m["email"] != e.user.Email || m["business_name"] != "Deneme Market" || m["role"] != "owner" {
		t.Fatalf("/me: %v", m)
	}
}

func testutilUser(t *testing.T, e *env, email string, role auth.Role) testutil.User {
	t.Helper()
	return testutil.CreateUser(t, e.pool, e.user.BusinessID, email, "parola-123-xyz", role)
}

// Aynı adı taşıyan cariler (ad sıralamasında beraberlik): sayfa sınırlarında satır atlanmamalı/tekrarlanmamalı.
// ORDER BY id, çıktıdaki id::text sütununa bağlanırsa sıralama ile keyset koşulu (uuid) tutarsız kalırdı.
func TestPaginationStableWithTiedSortKeys(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	want := map[string]bool{}
	for i := 0; i < 25; i++ {
		_, m, _ := e.call("POST", "/contacts", map[string]any{"kind": "customer", "name": "Aynı Ad"}, tok, nil)
		want[m["id"].(string)] = true
		_, p, _ := e.call("POST", "/products", map[string]any{"sku": fmt.Sprintf("S-%d", i), "name": "x", "kind": "product", "vat_rate": 20}, tok, nil)
		_ = p
	}
	for _, size := range []int{1, 2, 3, 7} {
		got := e.walk(t, tok, "/contacts", size)
		seen := map[string]bool{}
		for _, c := range got {
			id := c["id"].(string)
			if seen[id] {
				t.Fatalf("limit=%d: tekrar eden kayıt %s", size, id)
			}
			seen[id] = true
		}
		if len(seen) != len(want) {
			t.Fatalf("limit=%d: %d kayıt bekleniyordu, %d geldi", size, len(want), len(seen))
		}
	}
	// Faturalar: aynı created_at'e düşen kayıtlar için de aynı garanti (tek transaction'da 2 satır eklemek mümkün değil;
	// bu yüzden sıralamanın uuid ile yapıldığını plan düzeyinde değil, davranış düzeyinde doğruluyoruz).
	cid := e.newContact(t, tok, "customer")
	for i := 0; i < 12; i++ {
		e.newInvoice(t, tok, saleBody(cid, "2026-10-10"))
	}
	for _, size := range []int{1, 5} {
		got := e.walk(t, tok, "/invoices", size)
		seen := map[string]bool{}
		for _, c := range got {
			seen[c["id"].(string)] = true
		}
		if len(got) != 12 || len(seen) != 12 {
			t.Fatalf("limit=%d: 12 benzersiz fatura bekleniyordu: %d/%d", size, len(got), len(seen))
		}
	}
}
