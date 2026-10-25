package api_test

import (
	"context"
	"encoding/json"
	"testing"

	"esnafdefter/internal/auth"
	"esnafdefter/internal/testutil"
)

// auditRows verilen eylem/varlık için denetim kayıtlarını (yeniden eskiye) HTTP üzerinden getirir.
func (e *env) auditRows(t *testing.T, tok, query string) []map[string]any {
	t.Helper()
	code, m, _ := e.call("GET", "/audit-log"+query, nil, tok, nil)
	if code != 200 {
		t.Fatalf("denetim kaydı: %d %v", code, m)
	}
	var out []map[string]any
	for _, it := range m["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

func (e *env) auditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInvoiceLifecycleIsAudited(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	inv := e.newInvoice(t, tok, saleBody(cid, "2026-10-10"))
	id := inv["id"].(string)

	rows := e.auditRows(t, tok, "?entity=invoice&entity_id="+id)
	if len(rows) != 1 || rows[0]["action"] != "invoice_create" {
		t.Fatalf("oluşturma kaydı bekleniyordu: %v", rows)
	}
	r := rows[0]
	if r["actor_email"] != e.user.Email || r["actor_id"] != e.user.ID {
		t.Errorf("kim: %v / %v", r["actor_email"], r["actor_id"])
	}
	nv := r["new_value"].(map[string]any)
	if nv["number"] != inv["number"] || nv["gross_total"] != inv["gross_total"] || nv["kind"] != "sale" {
		t.Errorf("neyi (yeni değer): %v", nv)
	}
	if r["at"] == nil || r["at"] == "" {
		t.Errorf("ne zaman eksik")
	}

	e.call("POST", "/invoices/"+id+"/cancel", map[string]string{"date": "2026-10-12"}, tok, nil)
	rows = e.auditRows(t, tok, "?entity=invoice&entity_id="+id)
	if len(rows) != 2 || rows[0]["action"] != "invoice_cancel" { // yeniden eskiye
		t.Fatalf("iptal kaydı en üstte olmalı: %v", rows)
	}
	ov, nw := rows[0]["old_value"].(map[string]any), rows[0]["new_value"].(map[string]any)
	if ov["status"] != "issued" || nw["status"] != "cancelled" || nw["reversal_date"] != "2026-10-12" {
		t.Errorf("eski/yeni değer: %v → %v", ov, nw)
	}
}

func TestPaymentLifecycleIsAudited(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	e.newInvoice(t, tok, saleBody(cid, "2026-10-01")) // brüt 24000
	_, p := e.pay(t, tok, cid, 30000, "2026-10-02")   // 24000 mahsup + 6000 avans
	id := p["id"].(string)
	rows := e.auditRows(t, tok, "?entity=payment&entity_id="+id)
	if len(rows) != 1 || rows[0]["action"] != "payment_create" {
		t.Fatalf("ödeme kaydı: %v", rows)
	}
	nv := rows[0]["new_value"].(map[string]any)
	if nv["amount"] != 30000.0 || nv["allocated"] != 24000.0 || nv["advance"] != 6000.0 {
		t.Errorf("yeni değer: %v", nv)
	}
	e.call("DELETE", "/payments/"+id, nil, tok, nil)
	rows = e.auditRows(t, tok, "?entity=payment&entity_id="+id)
	if len(rows) != 2 || rows[0]["action"] != "payment_delete" {
		t.Fatalf("silme kaydı en üstte olmalı: %v", rows)
	}
	if rows[0]["old_value"].(map[string]any)["status"] != "active" || rows[0]["new_value"].(map[string]any)["status"] != "deleted" {
		t.Errorf("eski/yeni: %v", rows[0])
	}
	// avans mahsubu: faturası olmayan yeni bir cariye tahsilat tamamen avans olur; sonra fatura kesilip avans mahsup edilir
	cid2 := e.newContact(t, tok, "customer")
	_, p2 := e.pay(t, tok, cid2, 5000, "2026-10-03")
	if p2["advance"] != 5000.0 {
		t.Fatalf("tamamı avans olmalı: %v", p2)
	}
	e.newInvoice(t, tok, saleBody(cid2, "2026-10-04"))
	if code, m, _ := e.call("POST", "/payments/"+p2["id"].(string)+"/apply-advance", nil, tok, nil); code != 200 {
		t.Fatalf("avans mahsubu: %d %v", code, m)
	}
	rows = e.auditRows(t, tok, "?action=payment_apply_advance")
	if len(rows) != 1 || rows[0]["new_value"].(map[string]any)["applied"] != 5000.0 {
		t.Errorf("avans mahsubu denetlenmeli: %v", rows)
	}
}

func TestPeriodCloseAndSettingsAreAudited(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	e.call("POST", "/periods/close", map[string]string{"through": "2026-09-30"}, tok, nil)
	e.call("POST", "/periods/close", map[string]string{"through": "2026-10-10"}, tok, nil)
	rows := e.auditRows(t, tok, "?action=period_close")
	if len(rows) != 2 {
		t.Fatalf("2 dönem kapatma kaydı bekleniyordu: %v", rows)
	}
	last := rows[0]
	if last["old_value"].(map[string]any)["closed_through"] != "2026-09-30" || last["new_value"].(map[string]any)["closed_through"] != "2026-10-10" {
		t.Errorf("eski/yeni kapanış tarihi: %v", last)
	}
	if rows[1]["old_value"] != nil {
		t.Errorf("ilk kapanışın eski değeri boş olmalı: %v", rows[1]["old_value"])
	}
	// Başarısız (geri açma) denemesi kayıt bırakmaz
	if code, _, _ := e.call("POST", "/periods/close", map[string]string{"through": "2026-01-01"}, tok, nil); code != 409 {
		t.Fatalf("409 bekleniyordu: %d", code)
	}
	if n := e.auditCount(t, "period_close"); n != 2 {
		t.Errorf("reddedilen kapanış denetim kaydı bırakmamalı: %d", n)
	}
}

func TestContactAndProductCreationAudited(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	e.newContact(t, tok, "customer")
	e.product(t, tok, "AUD-1", "product")
	if e.auditCount(t, "contact_create") != 1 || e.auditCount(t, "product_create") != 1 {
		t.Fatal("cari ve ürün oluşturma denetlenmeli")
	}
	// Tekrarlı SKU reddedilir ve ikinci bir kayıt bırakmaz
	e.call("POST", "/products", map[string]any{"sku": "AUD-1", "name": "x", "kind": "product", "vat_rate": 20}, tok, nil)
	if n := e.auditCount(t, "product_create"); n != 1 {
		t.Errorf("reddedilen ürün kayıt bırakmamalı: %d", n)
	}
}

// Denetim kaydı işle AYNI transaction'dadır: başarısız işlem iz bırakmaz.
func TestFailedOperationsLeaveNoAuditTrail(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	e.call("POST", "/periods/close", map[string]string{"through": "2026-09-30"}, tok, nil)
	before := e.auditCount(t, "invoice_create")

	if code, _, _ := e.call("POST", "/invoices", saleBody(cid, "2026-09-15"), tok, nil); code != 409 { // kapalı dönem
		t.Fatalf("409 bekleniyordu: %d", code)
	}
	bad := saleBody(cid, "2026-10-10")
	bad["lines"] = []map[string]any{{"description": "x", "quantity": "1", "unit_price": 100, "vat_rate": 18}}
	e.call("POST", "/invoices", bad, tok, nil) // 400
	p := e.product(t, tok, "AUD-S", "product")
	if code, _ := e.sell(tok, cid, p, "2026-10-10", "5"); code != 409 { // yetersiz stok: numara alındıktan sonra geri alınır
		t.Fatalf("409 bekleniyordu: %d", code)
	}
	if after := e.auditCount(t, "invoice_create"); after != before {
		t.Fatalf("başarısız işlemler denetim kaydı bırakmamalı: %d → %d", before, after)
	}
	// Başarılı bir işlem ise tam 1 kayıt bırakır
	e.newInvoice(t, tok, saleBody(cid, "2026-10-10"))
	if after := e.auditCount(t, "invoice_create"); after != before+1 {
		t.Fatalf("başarılı işlem tam 1 kayıt bırakmalı: %d", after-before)
	}
}

func TestPurchaseCorrectionIsAudited(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	supp := e.newContact(t, tok, "supplier")
	p := e.product(t, tok, "AUD-C", "product")
	orig := e.newInvoice(t, tok, buyBody(supp, p, "2026-10-01", "10", 10000))
	code, m, _ := e.call("POST", "/invoices/"+orig["id"].(string)+"/correct", buyBody(supp, p, "2026-10-01", "10", 12000), tok, nil)
	if code != 201 {
		t.Fatalf("düzeltme: %d %v", code, m)
	}
	rows := e.auditRows(t, tok, "?entity=invoice&entity_id="+orig["id"].(string))
	actions := map[string]bool{}
	for _, r := range rows {
		actions[r["action"].(string)] = true
	}
	for _, a := range []string{"invoice_create", "invoice_cancel", "invoice_correct"} {
		if !actions[a] {
			t.Errorf("%s kaydı eksik: %v", a, actions)
		}
	}
}

func TestAuditLogAccessAndIsolation(t *testing.T) {
	e := setup(t)
	owner := e.token(e.user)
	emp := e.token(testutil.CreateUser(t, e.pool, e.user.BusinessID, "e@a.test", "parola-aaa-1", auth.RoleEmployee))
	cus := e.token(testutil.CreateUser(t, e.pool, e.user.BusinessID, "m@a.test", "parola-aaa-2", auth.RoleCustomer))
	e.newContact(t, owner, "customer")
	if code, _, _ := e.call("GET", "/audit-log", nil, emp, nil); code != 403 {
		t.Errorf("çalışan denetim kaydını görememeli: %d", code)
	}
	if code, _, _ := e.call("GET", "/audit-log", nil, cus, nil); code != 403 {
		t.Errorf("müşteri denetim kaydını görememeli: %d", code)
	}
	if code, _, _ := e.call("GET", "/audit-log", nil, "", nil); code != 401 {
		t.Errorf("belirteçsiz 401 olmalı: %d", code)
	}
	for _, bad := range []string{"?limit=0", "?limit=999", "?after=abc"} {
		if code, _, _ := e.call("GET", "/audit-log"+bad, nil, owner, nil); code != 400 {
			t.Errorf("%s 400 olmalı: %d", bad, code)
		}
	}
	// Başka işletme göremez
	bizB := testutil.CreateBusiness(t, e.pool, "B", "7777777777")
	tokB := e.token(testutil.CreateUser(t, e.pool, bizB, "b@b.test", "parola-bbb-1", auth.RoleOwner))
	if rows := e.auditRows(t, tokB, ""); len(rows) != 0 {
		t.Errorf("başka işletmenin denetim kaydı sızmamalı: %v", rows)
	}
	// Sayfalama: tüm kayıtlar tekrarsız gezilebilir
	for i := 0; i < 7; i++ {
		e.newContact(t, owner, "customer")
	}
	seen := map[float64]bool{}
	after := ""
	for page := 0; page < 20; page++ {
		url := "/audit-log?limit=3"
		if after != "" {
			url += "&after=" + after
		}
		_, m, _ := e.call("GET", url, nil, owner, nil)
		for _, it := range m["items"].([]any) {
			id := it.(map[string]any)["id"].(float64)
			if seen[id] {
				t.Fatalf("tekrarlı kayıt: %v", id)
			}
			seen[id] = true
		}
		next, _ := m["next_after"].(string)
		if next == "" {
			break
		}
		after = next
	}
	if len(seen) < 8 {
		t.Errorf("en az 8 kayıt bekleniyordu: %d", len(seen))
	}
}

func TestAuditLogRemainsAppendOnlyAfterNewWrites(t *testing.T) {
	e := setup(t)
	tok := e.token(e.user)
	cid := e.newContact(t, tok, "customer")
	inv := e.newInvoice(t, tok, saleBody(cid, "2026-10-10"))
	_ = inv
	for _, q := range []string{`UPDATE audit_log SET action = 'x'`, `DELETE FROM audit_log`, `TRUNCATE audit_log`} {
		if _, err := e.pool.Exec(context.Background(), q); err == nil {
			t.Errorf("%q reddedilmeliydi", q)
		}
	}
	var raw []byte
	_ = e.pool.QueryRow(context.Background(), `SELECT new_value FROM audit_log WHERE action='invoice_create'`).Scan(&raw)
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m["gross_total"] == nil {
		t.Errorf("yeni değer JSON olmalı: %s", raw)
	}
}
