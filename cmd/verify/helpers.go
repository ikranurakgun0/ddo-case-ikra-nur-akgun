package main

import "fmt"

func line(desc, qty string, price int64, vat int) map[string]any {
	return map[string]any{"description": desc, "quantity": qty, "unit_price": price, "vat_rate": vat}
}

func prodLine(pid, qty string, price int64, vat int) map[string]any {
	return map[string]any{"product_id": pid, "description": "ürün", "quantity": qty, "unit_price": price, "vat_rate": vat}
}

func invBody(kind, contact, issue, due string, lines ...map[string]any) map[string]any {
	return map[string]any{"kind": kind, "contact_id": contact, "issue_date": issue, "due_date": due, "lines": lines}
}

func (e *env) contact(kind string) string {
	r := e.c.do("POST", "/contacts", e.owner, map[string]any{"kind": kind, "name": fmt.Sprintf("Doğrulama %s %s-%d", kind, e.run, e.next())}, nil).must(201, "cari oluşturma")
	return r.str("id")
}

func (e *env) product(kind string) string {
	r := e.c.do("POST", "/products", e.owner, map[string]any{"sku": e.sku("P"), "name": "Doğrulama ürünü", "kind": kind, "vat_rate": 20, "sale_price": 20000}, nil).must(201, "ürün oluşturma")
	return r.str("id")
}

func (e *env) invoice(body map[string]any) resp {
	return e.c.do("POST", "/invoices", e.owner, body, nil).must(201, "fatura oluşturma")
}

func (e *env) getInvoice(id string) resp {
	return e.c.do("GET", "/invoices/"+id, e.owner, nil, nil).must(200, "fatura okuma")
}

func (e *env) pay(kind, contact, method, date string, amount int64) resp {
	return e.c.do("POST", "/payments", e.owner, map[string]any{"kind": kind, "contact_id": contact, "method": method, "date": date, "amount": amount}, nil)
}

// acctNet hesabın net bakiyesini (borç - alacak) döner.
func (e *env) acctNet(code string) int64 {
	r := e.c.do("GET", "/reports/trial-balance", e.owner, nil, nil).must(200, "mizan")
	for _, a := range r.arr("accounts") {
		m := a.(map[string]any)
		if m["code"] == code {
			return num(m, "debit") - num(m, "credit")
		}
	}
	panic(failure{"hesap bulunamadı: " + code})
}

func (e *env) trialBalanced() {
	r := e.c.do("GET", "/reports/trial-balance", e.owner, nil, nil).must(200, "mizan")
	expect(r.body["balanced"] == true && r.num("total_debit") == r.num("total_credit"),
		"mizan dengesiz: borç %d alacak %d", r.num("total_debit"), r.num("total_credit"))
}

func (e *env) stockOf(pid string) (qty, value int64) {
	r := e.c.do("GET", "/products/"+pid, e.owner, nil, nil).must(200, "ürün okuma")
	s := r.obj("stock")
	return num(s, "quantity_milli"), num(s, "value")
}

// outCosts ürünün çıkış hareketlerinin maliyetlerini (id sırasıyla) döner.
func (e *env) outCosts(pid string) []int64 {
	r := e.c.do("GET", "/stock-movements?product_id="+pid+"&limit=200", e.owner, nil, nil).must(200, "stok hareketleri")
	var out []int64
	for _, it := range r.arr("items") {
		m := it.(map[string]any)
		if m["direction"] == "out" && m["active"] == true {
			out = append(out, num(m, "value"))
		}
	}
	return out
}
