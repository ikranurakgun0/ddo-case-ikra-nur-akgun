package main

import (
	"fmt"
	"strings"

	"esnafdefter/internal/sim"
)

func checkHealth(e *env) {
	r := e.c.do("GET", "/health", "", nil, nil).must(200, "health")
	expect(r.str("status") == "ok" && r.str("db") == "up", "sağlık yanıtı beklenmedik: %v", r.body)
	nf := e.c.do("GET", "/olmayan-uc", "", nil, nil)
	expect(nf.code == 404 && nf.errCode() == "not_found", "bilinmeyen uç 404/not_found olmalı: %d %v", nf.code, nf.body)
}

func checkAuth(e *env) {
	bad := e.c.do("POST", "/auth/login", "", map[string]string{"email": sim.DemoEmail("owner", verifyBusiness), "password": "yanlis-parola"}, nil)
	expect(bad.code == 401 && bad.errCode() == "invalid_credentials", "yanlış parola 401 olmalı: %d %v", bad.code, bad.body)
	unk := e.c.do("POST", "/auth/login", "", map[string]string{"email": "yok@yok.demo", "password": "x"}, nil)
	expect(unk.code == 401 && unk.errCode() == "invalid_credentials", "bilinmeyen kullanıcı da aynı 401 olmalı: %d", unk.code)
	e.c.do("GET", "/me", "", nil, nil).must(401, "belirteçsiz /me")
	e.c.do("GET", "/me", "çöp.belirteç.değeri", nil, nil).must(401, "çöp belirteç")
	e.c.do("POST", "/auth/login", "", map[string]string{"email": "", "password": ""}, nil).must(400, "boş giriş doğrulaması")

	l := e.c.do("POST", "/auth/login", "", map[string]string{"email": sim.DemoEmail("accountant", verifyBusiness), "password": sim.DemoPassword}, nil).must(200, "giriş")
	me := e.c.do("GET", "/me", l.str("access_token"), nil, nil).must(200, "/me")
	expect(me.str("role") == "accountant", "rol muhasebeci olmalı: %v", me.body)
	r1 := l.str("refresh_token")
	n := e.c.do("POST", "/auth/refresh", "", map[string]string{"refresh_token": r1}, nil).must(200, "refresh")
	expect(n.str("refresh_token") != r1, "refresh belirteci döndürülmeli (rotasyon)")
	e.c.do("POST", "/auth/refresh", "", map[string]string{"refresh_token": r1}, nil).must(401, "eski refresh belirteci")
	e.c.do("POST", "/auth/refresh", "", map[string]string{"refresh_token": n.str("refresh_token")}, nil).must(401, "yeniden kullanım sonrası tüm oturumlar kapanmalı")
}

func checkRBAC(e *env) {
	body := invBody("sale", "00000000-0000-0000-0000-000000000000", day(0), day(30), line("x", "1", 100, 20))
	e.c.do("POST", "/invoices", e.cust, body, nil).must(403, "müşteri fatura kesemez")
	e.c.do("POST", "/invoices", "", body, nil).must(401, "belirteçsiz fatura")
	e.c.do("GET", "/contacts", e.cust, nil, nil).must(403, "müşteri cari listeleyemez")
	e.c.do("GET", "/reports/aging", e.cust, nil, nil).must(403, "müşteri yaşlandırma göremez")
	e.c.do("POST", "/payments", e.cust, map[string]any{"kind": "receipt"}, nil).must(403, "müşteri ödeme giremez")
	e.c.do("POST", "/periods/close", e.emp, map[string]string{"through": "2026-01-05"}, nil).must(403, "çalışan dönem kapatamaz")
	e.c.do("GET", "/reports/trial-balance", e.emp, nil, nil).must(403, "çalışan mizan göremez")
	e.c.do("PUT", "/settings", e.acct, map[string]any{"allow_negative_stock": true}, nil).must(403, "ayarı yalnızca sahip değiştirebilir")
	e.c.do("DELETE", "/payments/00000000-0000-0000-0000-000000000000", e.emp, nil, nil).must(403, "çalışan ödeme silemez")
	e.c.do("POST", "/invoices/00000000-0000-0000-0000-000000000000/cancel", e.emp, nil, nil).must(403, "çalışan iptal edemez")
	e.c.do("GET", "/me/statement", e.owner, nil, nil).must(403, "/me/statement yalnızca müşteri rolüne açık")

	// Çalışan fatura kesebilir (izinli rol)
	c := e.contact("customer")
	e.c.do("POST", "/invoices", e.emp, invBody("sale", c, day(0), day(30), line("x", "1", 1000, 20)), nil).must(201, "çalışan fatura kesebilmeli")

	// Çok kiracılık: başka işletmenin verisi görünmez
	l := e.c.do("POST", "/auth/login", "", map[string]string{"email": sim.DemoEmail("owner", 1), "password": sim.DemoPassword}, nil).must(200, "işletme 1 girişi")
	other := l.str("access_token")
	list := e.c.do("GET", "/invoices?limit=1", other, nil, nil).must(200, "işletme 1 faturaları")
	items := list.arr("items")
	expect(len(items) == 1, "işletme 1'in faturası olmalı (seed)")
	id := items[0].(map[string]any)["id"].(string)
	e.c.do("GET", "/invoices/"+id, e.owner, nil, nil).must(404, "başka işletmenin faturası görünmemeli")
	e.c.do("POST", "/invoices/"+id+"/cancel", e.owner, nil, nil).must(404, "başka işletmenin faturası iptal edilememeli")
	cl := e.c.do("GET", "/contacts?limit=1", other, nil, nil).must(200, "işletme 1 carileri")
	cid := cl.arr("items")[0].(map[string]any)["id"].(string)
	e.c.do("GET", "/contacts/"+cid, e.owner, nil, nil).must(404, "başka işletmenin carisi görünmemeli")
	e.c.do("POST", "/invoices", e.owner, invBody("sale", cid, day(0), day(30), line("x", "1", 1000, 20)), nil).must(404, "başka işletmenin carisine fatura kesilememeli")
}

func checkF1(e *env) {
	cust, supp, both := e.contact("customer"), e.contact("supplier"), e.contact("both")
	for id, kind := range map[string]string{cust: "customer", supp: "supplier", both: "both"} {
		r := e.c.do("GET", "/contacts/"+id, e.owner, nil, nil).must(200, "cari okuma")
		expect(r.str("kind") == kind && strings.HasPrefix(r.str("name"), "Doğrulama"), "cari alanları yanlış: %v", r.body)
	}
	e.c.do("POST", "/contacts", e.owner, map[string]any{"kind": "alien", "name": "x"}, nil).must(400, "geçersiz tür")
	e.c.do("POST", "/contacts", e.owner, map[string]any{"kind": "customer", "name": "  "}, nil).must(400, "boş ad")
	e.c.do("GET", "/contacts/olmayan", e.owner, nil, nil).must(404, "geçersiz id")

	seen := map[string]bool{}
	after := ""
	for page := 0; page < 4; page++ {
		path := "/contacts?limit=25"
		if after != "" {
			path += "&after=" + after
		}
		r := e.c.do("GET", path, e.owner, nil, nil).must(200, "cari sayfalama")
		items := r.arr("items")
		expect(len(items) == 25, "sayfada 25 kayıt bekleniyordu: %d", len(items))
		for _, it := range items {
			id := it.(map[string]any)["id"].(string)
			expect(!seen[id], "sayfalamada tekrar eden kayıt: %s", id)
			seen[id] = true
		}
		next, _ := r.body["next_after"].(string)
		expect(next != "", "next_after dönmeli (işletmede 600 cari var)")
		after = next
	}
	e.c.do("GET", "/contacts?limit=0", e.owner, nil, nil).must(400, "limit=0")
	e.c.do("GET", "/contacts?limit=999", e.owner, nil, nil).must(400, "limit=999")
}

func checkF2(e *env) {
	sku := e.sku("S")
	create := func(s, kind string, vat int) resp {
		return e.c.do("POST", "/products", e.owner, map[string]any{"sku": s, "name": "Doğrulama", "kind": kind, "vat_rate": vat, "sale_price": 1500}, nil)
	}
	g := create(sku, "product", 20).must(201, "ürün oluşturma")
	svc := create(e.sku("H"), "service", 10).must(201, "hizmet oluşturma")
	d := create(sku, "product", 20)
	expect(d.code == 409 && d.errCode() == "duplicate_sku", "tekrarlı SKU 409 olmalı: %d %v", d.code, d.body)
	v := create(e.sku("X"), "product", 18)
	expect(v.code == 400 && v.errCode() == "invalid_vat_rate", "KDV %%18 reddedilmeli: %d %v", v.code, v.body)
	create(e.sku("Y"), "gift", 20).must(400, "geçersiz tür")
	gp := e.c.do("GET", "/products/"+g.str("id"), e.owner, nil, nil).must(200, "ürün okuma")
	expect(gp.obj("stock") != nil && num(gp.obj("stock"), "quantity_milli") == 0, "ürün kartında stok bilgisi olmalı: %v", gp.body)
	sp := e.c.do("GET", "/products/"+svc.str("id"), e.owner, nil, nil).must(200, "hizmet okuma")
	expect(sp.obj("stock") == nil, "hizmet kartında stok olmamalı")
	l := e.c.do("GET", "/products?limit=5", e.owner, nil, nil).must(200, "ürün listesi")
	expect(len(l.arr("items")) == 5, "5 ürün bekleniyordu")
}

func checkF3(e *env) {
	cust, supp := e.contact("customer"), e.contact("supplier")
	p := e.product("product")

	pur := e.invoice(invBody("purchase", supp, day(-5), day(25), prodLine(p, "10", 10000, 20)))
	expect(pur.num("net_total") == 100000 && pur.num("vat_total") == 20000 && pur.num("gross_total") == 120000, "alış toplamları: %v", pur.body)
	expect(strings.HasPrefix(pur.str("number"), "AL-"), "alış serisi AL olmalı: %s", pur.str("number"))

	s1 := e.invoice(invBody("sale", cust, day(0), day(30), line("KDV hariç", "2", 10000, 20)))
	expect(s1.num("net_total") == 20000 && s1.num("vat_total") == 4000 && s1.num("gross_total") == 24000, "KDV hariç satış: %v", s1.body)
	inc := invBody("sale", cust, day(0), day(30), line("KDV dahil", "2.5", 12000, 20))
	inc["prices_include_vat"] = true
	s2 := e.invoice(inc)
	expect(s2.num("net_total") == 25000 && s2.num("vat_total") == 5000 && s2.num("gross_total") == 30000, "KDV dahil satış: %v", s2.body)
	got := e.getInvoice(s1.str("id"))
	expect(got.str("number") == s1.str("number") && got.num("open") == 24000, "okunan fatura farklı: %v", got.body)

	bad := e.c.do("POST", "/invoices", e.owner, invBody("sale", cust, day(0), day(30), line("x", "1", 100, 18)), nil)
	expect(bad.code == 400 && bad.errCode() == "invalid_vat_rate", "KDV %%18 reddedilmeli: %v", bad.body)
	mm := e.c.do("POST", "/invoices", e.owner, invBody("sale", supp, day(0), day(30), line("x", "1", 100, 20)), nil)
	expect(mm.code == 400 && mm.errCode() == "contact_kind_mismatch", "tedarikçiye satış reddedilmeli: %v", mm.body)

	c := e.c.do("POST", "/invoices/"+s1.str("id")+"/cancel", e.acct, nil, nil).must(200, "iptal")
	expect(c.str("status") == "cancelled" && c.num("open") == 0, "iptal sonrası durum: %v", c.body)
	again := e.c.do("POST", "/invoices/"+s1.str("id")+"/cancel", e.acct, nil, nil)
	expect(again.code == 409 && again.errCode() == "already_cancelled", "ikinci iptal 409: %v", again.body)
	l := e.c.do("GET", "/invoices?limit=5", e.owner, nil, nil).must(200, "fatura listesi")
	expect(len(l.arr("items")) == 5, "fatura listesi 5 kayıt dönmeli")
}

func checkF4(e *env) {
	cust, supp := e.contact("customer"), e.contact("supplier")
	sale := e.invoice(invBody("sale", cust, day(-3), day(27), line("x", "1", 10000, 20)))    // brüt 12000
	pur := e.invoice(invBody("purchase", supp, day(-3), day(27), line("y", "1", 20000, 20))) // brüt 24000
	bank0, cash0 := e.acctNet("102"), e.acctNet("100")

	r1 := e.pay("receipt", cust, "bank", day(0), 5000).must(201, "banka tahsilatı")
	expect(r1.num("allocated") == 5000 && r1.num("advance") == 0, "tahsilat mahsubu: %v", r1.body)
	r2 := e.pay("receipt", cust, "cash", day(0), 7000).must(201, "kasa tahsilatı")
	expect(r2.num("allocated") == 7000, "kasa tahsilatı mahsubu: %v", r2.body)
	d := e.pay("disbursement", supp, "cash", day(0), 24000).must(201, "ödeme")
	expect(d.num("allocated") == 24000, "ödeme mahsubu: %v", d.body)
	expect(e.acctNet("102")-bank0 == 5000, "banka (102) +5000 olmalı")
	expect(e.acctNet("100")-cash0 == 7000-24000, "kasa (100) -17000 olmalı")
	expect(e.getInvoice(sale.str("id")).num("open") == 0, "satış faturası kapanmalı")
	expect(e.getInvoice(pur.str("id")).num("open") == 0, "alış faturası kapanmalı")

	g := e.c.do("GET", "/payments/"+r1.str("id"), e.owner, nil, nil).must(200, "ödeme okuma")
	expect(len(g.arr("allocations")) == 1, "mahsup kaydı görünmeli: %v", g.body)
	e.c.do("GET", "/payments?limit=3", e.owner, nil, nil).must(200, "ödeme listesi")

	del := e.c.do("DELETE", "/payments/"+r1.str("id"), e.acct, nil, nil).must(200, "ödeme silme")
	expect(del.str("status") == "deleted", "ödeme silinmeli: %v", del.body)
	expect(e.acctNet("102")-bank0 == 0, "silinen tahsilatın kasa/banka etkisi geri alınmalı")
	expect(e.getInvoice(sale.str("id")).num("open") == 5000, "silinen tahsilat faturayı yeniden açmalı")
	e.c.do("DELETE", "/payments/"+r1.str("id"), e.acct, nil, nil).must(409, "ikinci silme")

	e.pay("receipt", cust, "cash", day(0), 0).must(400, "sıfır tutar")
	e.pay("receipt", supp, "cash", day(0), 100).must(400, "tedarikçiye tahsilat")
	e.pay("receipt", cust, "bitcoin", day(0), 100).must(400, "geçersiz yöntem")
}

func checkF5(e *env) {
	supp, cust := e.contact("supplier"), e.contact("customer")
	p := e.product("product")
	e.invoice(invBody("purchase", supp, day(-10), day(20), prodLine(p, "10", 10000, 20)))
	e.invoice(invBody("sale", cust, day(-5), day(25), prodLine(p, "4", 25000, 20)))
	r := e.c.do("GET", "/stock-movements?product_id="+p, e.owner, nil, nil).must(200, "stok hareketleri")
	items := r.arr("items")
	expect(len(items) == 2, "2 hareket bekleniyordu: %d", len(items))
	in, out := items[0].(map[string]any), items[1].(map[string]any)
	expect(in["direction"] == "in" && num(in, "quantity_milli") == 10000 && num(in, "value") == 100000, "giriş hareketi: %v", in)
	expect(out["direction"] == "out" && num(out, "quantity_milli") == 4000 && num(out, "value") == 40000, "çıkış hareketi: %v", out)
	q, v := e.stockOf(p)
	expect(q == 6000 && v == 60000, "stok 6 adet / 600,00 TL olmalı: %d/%d", q, v)
	rep := e.c.do("GET", "/reports/stock", e.owner, nil, nil).must(200, "stok raporu")
	found := false
	for _, it := range rep.arr("products") {
		m := it.(map[string]any)
		if m["product_id"] == p {
			found = true
			expect(num(m, "quantity_milli") == 6000 && num(m, "value") == 60000 && num(m, "avg_unit_cost") == 10000, "stok raporu satırı: %v", m)
		}
	}
	expect(found, "stok raporunda ürün yok")
}

func checkF6(e *env) {
	cust, supp := e.contact("customer"), e.contact("supplier")
	vat := func() (out, in int64) {
		r := e.c.do("GET", "/reports/vat-summary?from="+day(0)+"&to="+day(0), e.acct, nil, nil).must(200, "KDV özeti")
		return r.num("output_vat"), r.num("input_vat")
	}
	o0, i0 := vat()
	n391, n191 := e.acctNet("391"), e.acctNet("191")
	e.invoice(invBody("sale", cust, day(0), day(30), line("a", "1", 10000, 20), line("b", "1", 5000, 10), line("c", "1", 3000, 1)))
	e.invoice(invBody("purchase", supp, day(0), day(30), line("d", "1", 20000, 20)))
	cancelled := e.invoice(invBody("sale", cust, day(0), day(30), line("e", "1", 90000, 20)))
	e.c.do("POST", "/invoices/"+cancelled.str("id")+"/cancel", e.owner, nil, nil).must(200, "iptal")
	o1, i1 := vat()
	expect(o1-o0 == 2530 && i1-i0 == 4000, "KDV özeti farkı: hesaplanan %d (2530), indirilecek %d (4000); iptal edilen dahil olmamalı", o1-o0, i1-i0)
	expect(e.acctNet("391")-n391 == -2530 && e.acctNet("191")-n191 == 4000, "defter KDV bakiyeleri özetle tutarlı olmalı")
	e.trialBalanced()
	e.c.do("GET", "/reports/vat-summary?from=bozuk&to="+day(0), e.acct, nil, nil).must(400, "hatalı tarih")
}

func checkF7(e *env) {
	cust := e.contact("customer")
	e.invoice(invBody("sale", cust, day(-20), day(10), line("x", "1", 10000, 20)))
	e.pay("receipt", cust, "bank", day(-1), 5000).must(201, "tahsilat")
	st := e.c.do("GET", "/contacts/"+cust+"/statement", e.acct, nil, nil).must(200, "cari ekstresi")
	expect(st.num("balance") == 7000 && len(st.arr("rows")) == 2, "ekstre bakiyesi 7000 ve 2 satır olmalı: %v", st.body)
	ag := e.c.do("GET", "/reports/aging?as_of="+day(0), e.acct, nil, nil).must(200, "yaşlandırma")
	b := ag.obj("buckets")
	for _, k := range []string{"0-30", "31-60", "61-90", "90+"} {
		_, ok := b[k]
		expect(ok, "yaşlandırma kovası eksik: %s", k)
	}
	// Müşteri rolü yalnızca kendi ekstresini görür
	me := e.c.do("GET", "/me/statement", e.cust, nil, nil).must(200, "müşteri ekstresi")
	expect(me.str("contact_id") != "" && len(me.arr("rows")) > 0, "demo müşterinin ekstresinde hareket olmalı")
	e.c.do("GET", "/contacts/"+cust+"/statement", e.cust, nil, nil).must(403, "müşteri başka ekstreyi göremez")
	e.c.do("GET", "/reports/stock", e.owner, nil, nil).must(200, "stok raporu")
}

func checkT1(e *env) {
	cust, supp := e.contact("customer"), e.contact("supplier")
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, e.invoice(invBody("sale", cust, day(-i), day(30), line("x", "1.5", int64(10000+i*777), 20), line("y", "3", 999, 10))).str("id"))
	}
	for i := 0; i < 3; i++ {
		e.invoice(invBody("purchase", supp, day(-i), day(30), line("z", "2", int64(5000+i*313), 1)))
	}
	for _, id := range ids[:3] {
		e.c.do("POST", "/invoices/"+id+"/cancel", e.owner, nil, nil).must(200, "iptal")
	}
	e.trialBalanced()

	// İptal silme değildir: fatura durur, ters kayıt eklenir ve hesaplar sıfırlanır
	j := e.c.do("GET", "/invoices/"+ids[0]+"/journal", e.owner, nil, nil).must(200, "yevmiye")
	entries := j.arr("entries")
	expect(len(entries) == 2, "orijinal + ters kayıt bekleniyordu: %d", len(entries))
	net := map[string]int64{}
	for _, en := range entries {
		var d, c int64
		for _, l := range en.(map[string]any)["lines"].([]any) {
			lm := l.(map[string]any)
			d += num(lm, "debit")
			c += num(lm, "credit")
			net[lm["account"].(string)] += num(lm, "debit") - num(lm, "credit")
		}
		expect(d == c && d > 0, "her yevmiye kaydında borç=alacak olmalı: %d/%d", d, c)
	}
	for acc, n := range net {
		expect(n == 0, "iptal edilen faturanın %s hesabı sıfırlanmalı: %d", acc, n)
	}
	expect(e.getInvoice(ids[0]).str("status") == "cancelled", "iptal edilen fatura okunabilmeli")

	// Kapatılmış dönem kilitli (seed her işletmenin 31 Mart 2026'ya kadar olan dönemini kapatır)
	cp := e.c.do("POST", "/invoices", e.owner, invBody("sale", cust, "2026-02-10", "2026-03-10", line("x", "1", 1000, 20)), nil)
	expect(cp.code == 409 && cp.errCode() == "period_closed", "kapalı döneme fatura 409/period_closed olmalı: %d %v", cp.code, cp.body)
	re := e.c.do("POST", "/periods/close", e.owner, map[string]string{"through": "2026-02-01"}, nil)
	expect(re.code == 409 && re.errCode() == "period_not_advancing", "dönem geri açılamamalı: %d %v", re.code, re.body)
	per := e.c.do("GET", "/periods", e.owner, nil, nil).must(200, "dönem")
	cl, _ := per.body["closed_through"].(string)
	expect(cl >= "2026-03-31", "kapanış tarihi >= 2026-03-31 olmalı: %q", cl)
	keep := e.invoice(invBody("sale", cust, day(0), day(30), line("x", "1", 1000, 20)))
	cc := e.c.do("POST", "/invoices/"+keep.str("id")+"/cancel", e.owner, map[string]string{"date": "2026-02-15"}, nil)
	expect(cc.code == 409 && cc.errCode() == "period_closed", "kapalı döneme ters kayıt reddedilmeli: %d %v", cc.code, cc.body)
	expect(e.getInvoice(keep.str("id")).str("status") == "issued", "başarısız iptal hiçbir şeyi değiştirmemeli")
	e.trialBalanced()
}

func checkT2(e *env) {
	cust := e.contact("customer")
	series := "V" + e.run
	const n = 30
	rs := parallel(n, func(i int) resp {
		b := invBody("sale", cust, day(0), day(30), line("x", "1", int64(1000+i), 20))
		b["series"] = series
		return e.c.do("POST", "/invoices", e.owner, b, nil)
	})
	var seqs []int64
	numbers := map[string]bool{}
	for _, r := range rs {
		r.must(201, "eşzamanlı fatura")
		seqs = append(seqs, r.num("seq"))
		numbers[r.str("number")] = true
	}
	for i, s := range sortedInts(seqs) {
		expect(s == int64(i+1), "numaralar 1..%d boşluksuz ve çiftsiz olmalı; %d. numara %d", n, i+1, s)
	}
	expect(len(numbers) == n, "numaralar benzersiz olmalı")

	// Başarısız istek numara tüketmez
	bad := invBody("sale", cust, "2026-02-10", "2026-03-10", line("x", "1", 1000, 20))
	bad["series"] = series
	e.c.do("POST", "/invoices", e.owner, bad, nil).must(409, "kapalı dönem")
	ok := invBody("sale", cust, day(0), day(30), line("x", "1", 1000, 20))
	ok["series"] = series
	nx := e.invoice(ok)
	expect(nx.num("seq") == n+1, "başarısız istek numara tüketmemeli: %d (beklenen %d)", nx.num("seq"), n+1)

	// KDV: bilinen sonuçlar
	a := e.invoice(invBody("sale", cust, day(0), day(30), line("a", "2", 10000, 20)))
	expect(a.num("net_total") == 20000 && a.num("vat_total") == 4000 && a.num("gross_total") == 24000, "KDV hariç: %v", a.body)
	incl := invBody("sale", cust, day(0), day(30), line("b", "1", 12000, 20))
	incl["prices_include_vat"] = true
	b := e.invoice(incl)
	expect(b.num("net_total") == 10000 && b.num("vat_total") == 2000, "KDV dahil: %v", b.body)
	three := e.invoice(invBody("sale", cust, day(0), day(30), line("a", "1", 10000, 1), line("b", "1", 10000, 10), line("c", "1", 10000, 20)))
	expect(three.num("vat_total") == 100+1000+2000, "üç oran: %v", three.body)
	// 0,5 kuruş yarım yukarı
	h := e.invoice(invBody("sale", cust, day(0), day(30), line("h", "1", 5, 10), line("h2", "1", 10000, 20)))
	expect(h.num("vat_total") == 1+2000, "0,5 kuruş yukarı yuvarlanmalı: %v", h.body)

	// İskonto: elle hesaplanmış örnek (satır tutarları 15000 ve 1999; 500 genel iskonto -> 441 + 59)
	d := invBody("sale", cust, day(0), day(30), line("u1", "3", 5000, 10), line("u2", "1", 1999, 20))
	d["general_discount"] = 500
	di := e.invoice(d)
	expect(di.num("net_total") == 16499 && di.num("vat_total") == 1844 && di.num("gross_total") == 18343, "iskontolu fatura: %v", di.body)
	var share, sn, sv, sg int64
	for _, l := range di.arr("lines") {
		lm := l.(map[string]any)
		share += num(lm, "allocated_discount")
		sn += num(lm, "net")
		sv += num(lm, "vat")
		sg += num(lm, "gross")
		expect(num(lm, "net")+num(lm, "vat") == num(lm, "gross"), "satırda net+kdv=brüt olmalı: %v", lm)
	}
	expect(share == 500, "iskonto payları toplamı genel iskontoya eşit olmalı: %d", share)
	expect(sn == di.num("net_total") && sv == di.num("vat_total") && sg == di.num("gross_total"), "fatura toplamı satır toplamlarına eşit olmalı")

	// Kuruş yuvarlaması toplamı bozmamalı: 7 eşit satır, 1 TL iskonto
	var ls []map[string]any
	for i := 0; i < 7; i++ {
		ls = append(ls, line(fmt.Sprintf("s%d", i), "1", 3333, 20))
	}
	e7 := invBody("sale", cust, day(0), day(30), ls...)
	e7["general_discount"] = 100
	r7 := e.invoice(e7)
	var s7 int64
	for _, l := range r7.arr("lines") {
		s7 += num(l.(map[string]any), "allocated_discount")
	}
	expect(s7 == 100 && r7.num("net_total") == 7*3333-100, "7 satır / 100 kuruş iskonto toplamı bozmamalı: %d %v", s7, r7.num("net_total"))
	expect(e.c.do("POST", "/invoices", e.owner, func() map[string]any {
		x := invBody("sale", cust, day(0), day(30), line("q", "1", 100, 20))
		x["general_discount"] = 101
		return x
	}(), nil).errCode() == "discount_exceeds_total", "iskonto tutarı aşamaz")
}

func checkT3(e *env) {
	cust := e.contact("customer")
	i1 := e.invoice(invBody("sale", cust, day(-40), day(-10), line("a", "1", 10000, 20))) // 12000
	i2 := e.invoice(invBody("sale", cust, day(-30), day(0), line("b", "1", 20000, 20)))   // 24000
	i3 := e.invoice(invBody("sale", cust, day(-20), day(10), line("c", "1", 30000, 20)))  // 36000

	p := e.pay("receipt", cust, "bank", day(0), 20000).must(201, "kısmi tahsilat")
	al := p.arr("allocations")
	expect(len(al) == 2 && num(al[0].(map[string]any), "amount") == 12000 && num(al[1].(map[string]any), "amount") == 8000, "FIFO dağılımı 12000+8000 olmalı: %v", al)
	expect(al[0].(map[string]any)["invoice_id"] == i1.str("id"), "en eski fatura önce kapanmalı")
	expect(e.getInvoice(i1.str("id")).num("open") == 0 && e.getInvoice(i2.str("id")).num("open") == 16000 && e.getInvoice(i3.str("id")).num("open") == 36000, "açık tutarlar yanlış")

	// Fazla ödeme avans olur
	big := e.pay("receipt", cust, "cash", day(0), 100000).must(201, "fazla ödeme")
	expect(big.num("allocated") == 52000 && big.num("advance") == 48000, "avans: %v", big.body)
	// Silince mahsuplar geri alınır
	e.c.do("DELETE", "/payments/"+big.str("id"), e.owner, nil, nil).must(200, "silme")
	expect(e.getInvoice(i2.str("id")).num("open") == 16000 && e.getInvoice(i3.str("id")).num("open") == 36000, "silinen ödeme mahsupları geri almalı")
	// Avans, sonradan kesilen faturaya mahsup edilir
	adv := e.pay("receipt", cust, "cash", day(0), 10000).must(201, "tahsilat")
	_ = adv
	e.trialBalanced()

	// Yaşlandırma: kova sınırları
	ag := e.contact("customer")
	type tc struct {
		due    int
		bucket string
	}
	cases := []tc{{5, "0-30"}, {-30, "0-30"}, {-31, "31-60"}, {-60, "31-60"}, {-61, "61-90"}, {-90, "61-90"}, {-91, "90+"}}
	want := map[string]int64{}
	for i, c := range cases {
		issue := -150
		if i > 0 {
			issue = -140 + i
		}
		e.invoice(invBody("sale", ag, day(issue), day(c.due), line("y", "1", 1000, 20)))
		want[c.bucket] += 1200
	}
	rep := e.c.do("GET", "/reports/aging?as_of="+day(0)+"&detail=1", e.acct, nil, nil).must(200, "yaşlandırma")
	var mine map[string]any
	for _, c := range rep.arr("contacts") {
		if c.(map[string]any)["contact_id"] == ag {
			mine = c.(map[string]any)["buckets"].(map[string]any)
		}
	}
	expect(mine != nil, "cari yaşlandırma raporunda yok")
	for k, v := range want {
		expect(int64(mine[k].(float64)) == v, "kova %s: %d bekleniyordu, %v geldi", k, v, mine[k])
	}
	// Kısmi ödeme en eski faturadan düşer (ilk fatura 90+ kovasında)
	e.pay("receipt", ag, "cash", day(0), 500).must(201, "kısmi")
	rep = e.c.do("GET", "/reports/aging?as_of="+day(0), e.acct, nil, nil).must(200, "yaşlandırma")
	for _, c := range rep.arr("contacts") {
		if c.(map[string]any)["contact_id"] == ag {
			b := c.(map[string]any)["buckets"].(map[string]any)
			expect(int64(b["0-30"].(float64)) == want["0-30"]-500, "ilk (en eski) fatura 0-30 kovasında; 500 kuruş düşmeli: %v", b)
		}
	}

	// Eşzamanlı ödemeler hiçbir faturayı fazla kapatamaz
	cc := e.contact("customer")
	var invs []string
	for i := 0; i < 10; i++ {
		invs = append(invs, e.invoice(invBody("sale", cc, day(-20+i), day(10), line("k", "1", 10000, 20))).str("id")) // 12000 x 10
	}
	rs := parallel(15, func(int) resp { return e.pay("receipt", cc, "bank", day(0), 9000) }) // 135000 > 120000
	for _, r := range rs {
		r.must(201, "eşzamanlı tahsilat")
	}
	var paid int64
	for _, id := range invs {
		in := e.getInvoice(id)
		expect(in.num("paid") <= in.num("gross_total"), "fatura fazla mahsup edilmiş: %v", in.body)
		paid += in.num("paid")
	}
	expect(paid == 120000, "tüm faturalar kapanmalı (120000): %d", paid)
	st := e.c.do("GET", "/contacts/"+cc+"/statement", e.acct, nil, nil).must(200, "ekstre")
	expect(st.num("balance") == 120000-135000, "ekstre bakiyesi -15000 (avans) olmalı: %d", st.num("balance"))
	e.trialBalanced()
}

func checkT4(e *env) {
	supp, cust := e.contact("supplier"), e.contact("customer")
	p := e.product("product")
	in1 := e.invoice(invBody("purchase", supp, day(-30), day(0), prodLine(p, "10", 10000, 20)))
	e.invoice(invBody("purchase", supp, day(-29), day(0), prodLine(p, "10", 20000, 20)))
	e.invoice(invBody("sale", cust, day(-20), day(10), prodLine(p, "5", 25000, 20)))
	costs := e.outCosts(p)
	expect(len(costs) == 1 && costs[0] == 75000, "ortalama (150 TL) x 5 = 750 TL olmalı: %v", costs)
	q, v := e.stockOf(p)
	expect(q == 15000 && v == 225000, "stok 15 adet / 2250 TL olmalı: %d/%d", q, v)
	smm := e.acctNet("621")

	// Geçmişe dönük alış: satıştan ÖNCEki tarihe 10 x 300 TL -> ortalama 200 TL -> maliyet 1000 TL
	e.invoice(invBody("purchase", supp, day(-25), day(0), prodLine(p, "10", 30000, 20)))
	costs = e.outCosts(p)
	expect(len(costs) == 1 && costs[0] == 100000, "geçmişe dönük alış sonrası maliyet 1000 TL olmalı: %v", costs)
	expect(e.acctNet("621")-smm == 25000, "SMM düzeltme kaydı +250 TL olmalı: %d", e.acctNet("621")-smm)
	q, v = e.stockOf(p)
	expect(q == 25000 && v == 500000, "stok 25 adet / 5000 TL olmalı: %d/%d", q, v)

	// Alış faturası düzeltmesi: 100 TL -> 120 TL; maliyet (120000+200000+300000)/30*5 = 103333
	fix := e.c.do("POST", "/invoices/"+in1.str("id")+"/correct", e.acct, func() map[string]any {
		b := invBody("purchase", supp, day(-30), day(0), prodLine(p, "10", 12000, 20))
		return b
	}(), nil).must(201, "alış faturası düzeltme")
	_ = fix
	costs = e.outCosts(p)
	expect(len(costs) == 1 && costs[0] == 103333, "düzeltme sonrası maliyet 1033,33 TL olmalı: %v", costs)

	// Yetersiz stok
	over := e.c.do("POST", "/invoices", e.owner, invBody("sale", cust, day(-10), day(20), prodLine(p, "1000", 25000, 20)), nil)
	expect(over.code == 409 && over.errCode() == "insufficient_stock", "yetersiz stok 409 olmalı: %d %v", over.code, over.body)
	e.trialBalanced()

	// Eşzamanlı satış: 10 birimlik stok için 15 satış, her biri FARKLI seriden (yalnızca ürün kilidi korur)
	p2 := e.product("product")
	e.invoice(invBody("purchase", supp, day(-15), day(0), prodLine(p2, "10", 10000, 20)))
	rs := parallel(15, func(i int) resp {
		b := invBody("sale", cust, day(-1), day(20), prodLine(p2, "1", 25000, 20))
		b["series"] = fmt.Sprintf("R%s%d", e.run[:3], i)
		return e.c.do("POST", "/invoices", e.owner, b, nil)
	})
	ok, rej := 0, 0
	for _, r := range rs {
		switch {
		case r.code == 201:
			ok++
		case r.code == 409 && r.errCode() == "insufficient_stock":
			rej++
		default:
			expect(false, "beklenmeyen yanıt: %d %v", r.code, r.body)
		}
	}
	expect(ok == 10 && rej == 5, "10 satış başarılı, 5'i reddedilmeli: %d/%d", ok, rej)
	q, v = e.stockOf(p2)
	expect(q == 0 && v == 0, "stok tam bitmeli (negatif olmamalı): %d/%d", q, v)
	c2 := e.outCosts(p2)
	var total int64
	for _, c := range c2 {
		total += c
	}
	expect(len(c2) == 10 && total == 100000, "toplam çıkış maliyeti alış değerine eşit olmalı (100000): %d", total)

	// 153 hesabı stok değerine eşit: bu çalıştırmanın ürünleri için kontrol (tüm işletme değil; ürün bazında)
	e.trialBalanced()
}

func checkIdempotency(e *env) {
	cust := e.contact("customer")
	h := map[string]string{"Idempotency-Key": "doğrulama-" + e.run + "-fatura"}
	body := invBody("sale", cust, day(0), day(30), line("x", "1", 10000, 20))
	a := e.c.do("POST", "/invoices", e.owner, body, h).must(201, "ilk istek")
	b := e.c.do("POST", "/invoices", e.owner, body, h).must(201, "tekrar istek")
	expect(a.str("id") == b.str("id") && b.hdr.Get("Idempotent-Replay") == "true", "tekrar istek aynı faturayı dönmeli: %v / %v", a.str("id"), b.str("id"))
	other := invBody("sale", cust, day(0), day(30), line("x", "1", 99999, 20))
	d := e.c.do("POST", "/invoices", e.owner, other, h)
	expect(d.code == 422 && d.errCode() == "idempotency_key_reused", "aynı anahtar farklı gövde 422 olmalı: %d %v", d.code, d.body)

	// Eşzamanlı 8 aynı ödeme isteği tek ödeme üretmeli
	ph := map[string]string{"Idempotency-Key": "doğrulama-" + e.run + "-odeme"}
	pb := map[string]any{"kind": "receipt", "contact_id": cust, "method": "cash", "date": day(0), "amount": 5000}
	rs := parallel(8, func(int) resp { return e.c.do("POST", "/payments", e.owner, pb, ph) })
	ids := map[string]bool{}
	for _, r := range rs {
		r.must(201, "eşzamanlı idempotent ödeme")
		ids[r.str("id")] = true
	}
	expect(len(ids) == 1, "8 eşzamanlı istek tek ödeme üretmeli: %d farklı", len(ids))
	st := e.c.do("GET", "/contacts/"+cust+"/statement", e.owner, nil, nil).must(200, "ekstre")
	expect(st.num("balance") == 12000-5000, "ekstre bakiyesi 7000 olmalı (tek ödeme): %d", st.num("balance"))
}
