package sim

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"esnafdefter/internal/auth"
	"esnafdefter/internal/invoicing"
	"esnafdefter/internal/ledger"
	"esnafdefter/internal/payments"
	"esnafdefter/internal/stock"
)

var (
	nameA    = []string{"Yıldız", "Güneş", "Anadolu", "Ege", "Marmara", "Karadeniz", "Akdeniz", "Doğu", "Batı", "Kuzey", "Güney", "Bereket", "Umut", "Özgür", "Hilal", "Çınar"}
	nameB    = []string{"Market", "Gıda", "Tekstil", "Hırdavat", "Eczane", "Kırtasiye", "Mobilya", "Elektronik", "Nakliyat", "Ticaret", "Züccaciye", "Boya", "Oto Yedek", "Kuruyemiş", "Ambalaj"}
	items    = []string{"Çay", "Şeker", "Un", "Makarna", "Zeytinyağı", "Tuz", "Deterjan", "Kâğıt Havlu", "Defter", "Kalem", "Vida", "Boya Kovası", "Fırça", "Ampul", "Kablo", "Pil", "Su 5L", "Bardak", "Tabak", "Çanta"}
	bizNames = []string{"Yıldız Toptan Gıda", "Anadolu Hırdavat", "Ege Kırtasiye", "Marmara Züccaciye", "Karadeniz Ticaret"}
)

type simContact struct {
	id, kind, name string
	owed           int64 // tahmini açık bakiye (olay seçimi için; kesin değer veri tabanındadır)
}

type simProduct struct {
	id, sku, name string
	goods         bool
	vat           int
	price, cost   int64 // kuruş / birim
	avail         int64 // tahmini stok (binde bir birim)
}

type saleRef struct {
	id        string
	contact   *simContact
	gross     int64
	lines     []refLine
	cancelled bool
}

type purchaseRef struct {
	id         string
	contact    *simContact
	issue, due time.Time
	lines      []refLine
	cancelled  bool
}

type refLine struct {
	p     *simProduct
	qty   int64
	price int64
	vat   int
}

type payRef struct {
	id      string
	contact *simContact
	amount  int64
	deleted bool
}

type biz struct {
	pool  *pgxpool.Pool
	cfg   Config
	n     int // 1..N
	hash  string
	log   *slog.Logger
	rng   *rand.Rand
	id    string
	owner string

	customers, suppliers []*simContact
	goods, services      []*simProduct
	sales                []*saleRef
	purchases            []*purchaseRef
	pays                 []*payRef
	done, target         int
}

func newBiz(pool *pgxpool.Pool, cfg Config, n int, hash string, log *slog.Logger) *biz {
	return &biz{pool: pool, cfg: cfg, n: n, hash: hash, log: log,
		rng:    rand.New(rand.NewSource(cfg.Seed*1_000_003 + int64(n)*7919)),
		target: cfg.Events / cfg.Businesses}
}

func (b *biz) dateFor(i int) time.Time {
	span := int(b.cfg.End.Sub(b.cfg.Start).Hours() / 24)
	return b.cfg.Start.AddDate(0, 0, i*span/b.target)
}

func (b *biz) tx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// tolerated, durumdan kaynaklanan ve simülasyonda beklenen (atlanabilir) hatalardır.
func tolerated(err error) bool {
	var ie *stock.InsufficientError
	return errors.Is(err, invoicing.ErrHasPayments) || errors.Is(err, invoicing.ErrAlreadyCancelled) ||
		errors.Is(err, ledger.ErrPeriodClosed) || errors.Is(err, payments.ErrAlreadyDeleted) ||
		errors.As(err, &ie)
}

func (b *biz) setup(ctx context.Context) error {
	name := bizNames[(b.n-1)%len(bizNames)]
	taxNo := fmt.Sprintf("%010d", 1000000000+b.n*7919)
	if err := b.pool.QueryRow(ctx, `INSERT INTO businesses(name, tax_no) VALUES ($1,$2) RETURNING id::text`, name, taxNo).Scan(&b.id); err != nil {
		return err
	}
	nContacts := b.cfg.Contacts / b.cfg.Businesses
	for i := 0; i < nContacts; i++ {
		kind := "customer"
		switch {
		case i%12 == 11:
			kind = "both"
		case i%4 == 3:
			kind = "supplier"
		}
		c := &simContact{kind: kind, name: fmt.Sprintf("%s %s %d", nameA[i%len(nameA)], nameB[(i/len(nameA))%len(nameB)], i+1)}
		if err := b.pool.QueryRow(ctx,
			`INSERT INTO contacts(business_id, kind, name, tax_no) VALUES ($1::uuid,$2,$3,$4) RETURNING id::text`,
			b.id, kind, c.name, fmt.Sprintf("%011d", int64(b.n)*100000+int64(i)+1)).Scan(&c.id); err != nil {
			return err
		}
		if kind != "supplier" {
			b.customers = append(b.customers, c)
		}
		if kind != "customer" {
			b.suppliers = append(b.suppliers, c)
		}
	}
	vats := []int{20, 20, 20, 20, 20, 20, 10, 10, 10, 1}
	for i := 0; i < 60; i++ {
		goods := i < 50
		p := &simProduct{goods: goods, vat: vats[b.rng.Intn(len(vats))]}
		p.price = int64(500 + b.rng.Intn(49500))
		p.cost = p.price * int64(55+b.rng.Intn(25)) / 100
		kind := "product"
		p.sku = fmt.Sprintf("U%d-%03d", b.n, i+1)
		p.name = fmt.Sprintf("%s %d", items[i%len(items)], i/len(items)+1)
		if !goods {
			kind = "service"
			p.sku = fmt.Sprintf("H%d-%03d", b.n, i-49)
			p.name = fmt.Sprintf("Hizmet %d", i-49)
		}
		if err := b.pool.QueryRow(ctx,
			`INSERT INTO products(business_id, sku, name, kind, unit, vat_rate, sale_price) VALUES ($1::uuid,$2,$3,$4,'adet',$5,$6) RETURNING id::text`,
			b.id, p.sku, p.name, kind, p.vat, p.price).Scan(&p.id); err != nil {
			return err
		}
		if goods {
			b.goods = append(b.goods, p)
		} else {
			b.services = append(b.services, p)
		}
	}
	for i, role := range Roles {
		var contact any
		if role == auth.RoleCustomer {
			contact = b.customers[0].id // demo müşterisi: ilk cari
		}
		var uid string
		if err := b.pool.QueryRow(ctx,
			`INSERT INTO users(business_id, email, password_hash, role, contact_id) VALUES ($1::uuid,$2,$3,$4,$5::uuid) RETURNING id::text`,
			b.id, DemoEmail(role, b.n), b.hash, string(role), contact).Scan(&uid); err != nil {
			return err
		}
		if i == 0 {
			b.owner = uid
		}
	}
	return nil
}

func (b *biz) run(ctx context.Context) error {
	if err := b.setup(ctx); err != nil {
		return err
	}
	attempts := 0
	for b.done < b.target {
		if attempts++; attempts > b.target*4 {
			return fmt.Errorf("hedef hareket sayısına ulaşılamadı (%d/%d)", b.done, b.target)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		date := b.dateFor(b.done)
		var err error
		switch r := b.rng.Intn(1000); {
		case r < 420, r >= 970:
			err = b.sale(ctx, date)
		case r < 530:
			err = b.purchase(ctx, date, nil, 0)
		case r < 800:
			err = b.receipt(ctx, date)
		case r < 850:
			err = b.disbursement(ctx, date)
		case r < 890:
			err = b.cancelSale(ctx, date)
		case r < 910:
			err = b.deletePayment(ctx, date)
		case r < 930:
			err = b.correctPurchase(ctx, date)
		default:
			err = b.purchase(ctx, date, nil, 5+b.rng.Intn(36)) // geçmişe dönük alış
		}
		if err != nil && !tolerated(err) {
			return fmt.Errorf("hareket %d: %w", b.done, err)
		}
	}
	return nil
}

func (b *biz) pickGoods() *simProduct { return b.goods[b.rng.Intn(len(b.goods))] }

func (b *biz) pickCustomer() *simContact {
	if b.rng.Intn(100) < 8 {
		return b.customers[0]
	}
	return b.customers[b.rng.Intn(len(b.customers))]
}

func (b *biz) price(base int64) int64 { return base * int64(90+b.rng.Intn(21)) / 100 }

// sale satış faturası keser; stok yetmezse önce alış yapar (o da bir hareket sayılır).
func (b *biz) sale(ctx context.Context, date time.Time) error {
	c := b.pickCustomer()
	n := 1 + b.rng.Intn(4)
	var lines []invoicing.LineInput
	var refs []refLine
	need := map[*simProduct]int64{}
	var order []*simProduct
	var base int64
	for i := 0; i < n; i++ {
		var p *simProduct
		var qty int64
		if b.rng.Intn(100) < 15 {
			p = b.services[b.rng.Intn(len(b.services))]
			qty = int64(1+b.rng.Intn(3)) * 1000
		} else {
			p = b.pickGoods()
			qty = int64(1+b.rng.Intn(20)) * 1000
			if b.rng.Intn(100) < 20 {
				qty += 500
			}
			if _, ok := need[p]; !ok {
				order = append(order, p)
			}
			need[p] += qty
		}
		price := b.price(p.price)
		var disc int64
		gross := qty * price / 1000
		if b.rng.Intn(100) < 12 && gross > 10 {
			disc = b.rng.Int63n(gross/10 + 1)
		}
		base += gross - disc
		lines = append(lines, invoicing.LineInput{ProductID: p.id, Description: p.name, QuantityMilli: qty, UnitPrice: price, VATRate: p.vat, Discount: disc})
		refs = append(refs, refLine{p: p, qty: qty, price: price, vat: p.vat})
	}
	for _, p := range order {
		if p.avail < need[p] {
			if err := b.purchase(ctx, date, p, 0); err != nil {
				return err
			}
		}
	}
	var gd int64
	if b.rng.Intn(100) < 10 && base > 100 {
		gd = b.rng.Int63n(base / 20)
	}
	incl := b.rng.Intn(100) < 20
	due := date.AddDate(0, 0, []int{15, 30, 45, 60}[b.rng.Intn(4)])
	var inv *invoicing.Invoice
	err := b.tx(ctx, func(tx pgx.Tx) (err error) {
		inv, err = invoicing.CreateAndFinalize(ctx, tx, invoicing.CreateParams{
			BusinessID: b.id, UserID: b.owner, Kind: "sale", ContactID: c.id, IssueDate: date, DueDate: due,
			IncludesVAT: incl, GeneralDiscount: gd, Lines: lines})
		return
	})
	if err != nil {
		var ve *invoicing.ValidationError
		if errors.As(err, &ve) { // örn. sıfır toplam: simülasyonda atlanır
			return nil
		}
		return err
	}
	for _, p := range order {
		p.avail -= need[p]
	}
	c.owed += inv.GrossTotal
	b.sales = append(b.sales, &saleRef{id: inv.ID, contact: c, gross: inv.GrossTotal, lines: refs})
	if len(b.sales) > 300 {
		b.sales = b.sales[len(b.sales)-300:]
	}
	b.done++
	return nil
}

// purchase alış faturası keser. forced verilirse o ürünü stoklar; back>0 ise geçmişe dönük tarihlidir.
func (b *biz) purchase(ctx context.Context, date time.Time, forced *simProduct, back int) error {
	if back > 0 {
		date = date.AddDate(0, 0, -back)
		if date.Before(b.cfg.Start) {
			date = b.cfg.Start
		}
	}
	s := b.suppliers[b.rng.Intn(len(b.suppliers))]
	var lines []invoicing.LineInput
	var refs []refLine
	add := func(p *simProduct) {
		var qty int64
		if p.goods {
			qty = int64(20+b.rng.Intn(280)) * 1000
		} else {
			qty = 1000
		}
		price := b.price(p.cost)
		lines = append(lines, invoicing.LineInput{ProductID: p.id, Description: p.name, QuantityMilli: qty, UnitPrice: price, VATRate: p.vat})
		refs = append(refs, refLine{p: p, qty: qty, price: price, vat: p.vat})
	}
	if forced != nil {
		add(forced)
		lines[0].QuantityMilli += 200_000 // hedef ürün için tampon
		refs[0].qty = lines[0].QuantityMilli
	} else {
		for i, n := 0, 1+b.rng.Intn(3); i < n; i++ {
			if b.rng.Intn(100) < 10 {
				add(b.services[b.rng.Intn(len(b.services))])
			} else {
				add(b.pickGoods())
			}
		}
	}
	due := date.AddDate(0, 0, []int{15, 30, 60}[b.rng.Intn(3)])
	var inv *invoicing.Invoice
	err := b.tx(ctx, func(tx pgx.Tx) (err error) {
		inv, err = invoicing.CreateAndFinalize(ctx, tx, invoicing.CreateParams{
			BusinessID: b.id, UserID: b.owner, Kind: "purchase", ContactID: s.id, IssueDate: date, DueDate: due, Lines: lines})
		return
	})
	if err != nil {
		return err
	}
	for _, r := range refs {
		if r.p.goods {
			r.p.avail += r.qty
		}
	}
	s.owed += inv.GrossTotal
	b.purchases = append(b.purchases, &purchaseRef{id: inv.ID, contact: s, issue: date, due: due, lines: refs})
	if len(b.purchases) > 100 {
		b.purchases = b.purchases[len(b.purchases)-100:]
	}
	b.done++
	return nil
}

func pickOwing(rng *rand.Rand, list []*simContact) *simContact {
	var cands []*simContact
	for _, c := range list {
		if c.owed > 0 {
			cands = append(cands, c)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	return cands[rng.Intn(len(cands))]
}

func (b *biz) pay(ctx context.Context, date time.Time, kind string, c *simContact) error {
	var amount int64
	switch r := b.rng.Intn(100); {
	case r < 60:
		amount = c.owed
	case r < 85:
		amount = c.owed * int64(30+b.rng.Intn(41)) / 100
	default:
		amount = c.owed + c.owed*int64(b.rng.Intn(20))/100 + 1
	}
	if amount <= 0 {
		return nil
	}
	method := []string{"cash", "bank", "bank"}[b.rng.Intn(3)]
	var p *payments.Payment
	err := b.tx(ctx, func(tx pgx.Tx) (err error) {
		p, err = payments.CreateTx(ctx, tx, payments.CreateParams{BusinessID: b.id, UserID: b.owner, Kind: kind,
			ContactID: c.id, Method: method, Date: date, Amount: amount})
		return
	})
	if err != nil {
		return err
	}
	c.owed -= amount
	b.pays = append(b.pays, &payRef{id: p.ID, contact: c, amount: amount})
	if len(b.pays) > 300 {
		b.pays = b.pays[len(b.pays)-300:]
	}
	b.done++
	return nil
}

func (b *biz) receipt(ctx context.Context, date time.Time) error {
	c := pickOwing(b.rng, b.customers)
	if c == nil {
		return b.sale(ctx, date)
	}
	return b.pay(ctx, date, "receipt", c)
}

func (b *biz) disbursement(ctx context.Context, date time.Time) error {
	c := pickOwing(b.rng, b.suppliers)
	if c == nil {
		return b.purchase(ctx, date, nil, 0)
	}
	return b.pay(ctx, date, "disbursement", c)
}

func (b *biz) cancelSale(ctx context.Context, date time.Time) error {
	if len(b.sales) == 0 {
		return nil
	}
	s := b.sales[b.rng.Intn(len(b.sales))]
	if s.cancelled {
		return nil
	}
	err := b.tx(ctx, func(tx pgx.Tx) error {
		return invoicing.CancelAndFinalize(ctx, tx, b.id, b.owner, s.id, date)
	})
	if err != nil {
		if errors.Is(err, invoicing.ErrHasPayments) {
			s.cancelled = true // mahsup edilmiş; bir daha denenmesin
		}
		return err
	}
	s.cancelled = true
	for _, l := range s.lines {
		if l.p.goods {
			l.p.avail += l.qty
		}
	}
	s.contact.owed -= s.gross
	b.done++
	return nil
}

func (b *biz) deletePayment(ctx context.Context, date time.Time) error {
	if len(b.pays) == 0 {
		return nil
	}
	p := b.pays[b.rng.Intn(len(b.pays))]
	if p.deleted {
		return nil
	}
	err := b.tx(ctx, func(tx pgx.Tx) error {
		return payments.DeleteTx(ctx, tx, b.id, b.owner, p.id, date)
	})
	if err != nil {
		return err
	}
	p.deleted = true
	p.contact.owed += p.amount
	b.done++
	return nil
}

// correctPurchase bir alış faturasını (aynı miktar, farklı fiyat) düzeltir; maliyetler yeniden hesaplanır.
func (b *biz) correctPurchase(ctx context.Context, date time.Time) error {
	if len(b.purchases) == 0 {
		return nil
	}
	old := b.purchases[b.rng.Intn(len(b.purchases))]
	if old.cancelled {
		return nil
	}
	var lines []invoicing.LineInput
	var refs []refLine
	for _, l := range old.lines {
		price := l.price * int64(90+b.rng.Intn(21)) / 100
		lines = append(lines, invoicing.LineInput{ProductID: l.p.id, Description: l.p.name, QuantityMilli: l.qty, UnitPrice: price, VATRate: l.vat})
		refs = append(refs, refLine{p: l.p, qty: l.qty, price: price, vat: l.vat})
	}
	var inv *invoicing.Invoice
	err := b.tx(ctx, func(tx pgx.Tx) (err error) {
		inv, err = invoicing.CorrectAndFinalize(ctx, tx, old.id, invoicing.CreateParams{
			BusinessID: b.id, UserID: b.owner, Kind: "purchase", ContactID: old.contact.id,
			IssueDate: old.issue, DueDate: old.due, Lines: lines}, date)
		return
	})
	if err != nil {
		if errors.Is(err, invoicing.ErrHasPayments) {
			old.cancelled = true
		}
		return err
	}
	old.cancelled = true
	b.purchases = append(b.purchases, &purchaseRef{id: inv.ID, contact: old.contact, issue: old.issue, due: old.due, lines: refs})
	b.done++
	return nil
}
