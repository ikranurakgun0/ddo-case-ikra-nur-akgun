package sim

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"esnafdefter/internal/invoicing"
	"esnafdefter/internal/payments"
)

// RunLive seed bittikten sonra, arayüzde sistemin "canlı" olduğu görünsün diye belirli aralıklarla
// gerçek bir iş olayı (satış, tahsilat veya alış) üretir. Son işletme (doğrulama işletmesi) hariçtir:
// `make verify` onun üzerinde çalıştığı için ona dokunulmaz. Olaylar bugünün tarihlidir.
func RunLive(ctx context.Context, pool *pgxpool.Pool, interval time.Duration, log *slog.Logger) error {
	rows, err := pool.Query(ctx, `SELECT id::text FROM businesses ORDER BY tax_no`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) > 1 {
		ids = ids[:len(ids)-1]
	}
	if len(ids) == 0 {
		return errors.New("canlı akış için işletme yok")
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	t := time.NewTicker(interval)
	defer t.Stop()
	log.Info("canlı akış başladı", "aralik", interval.String(), "isletme", len(ids))
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		id := ids[rng.Intn(len(ids))]
		var kind string
		var err error
		switch r := rng.Intn(100); {
		case r < 55:
			kind, err = "satis", liveSale(ctx, pool, id, rng)
		case r < 90:
			kind, err = "tahsilat", liveReceipt(ctx, pool, id, rng)
		default:
			kind, err = "alis", livePurchase(ctx, pool, id, rng)
		}
		if err != nil && !tolerated(err) {
			log.Warn("canlı olay başarısız", "tur", kind, "err", err)
		} else if err == nil {
			log.Info("canlı olay", "tur", kind, "isletme", id)
		}
	}
}

func today() time.Time { return time.Now().UTC().Truncate(24 * time.Hour) }

func ownerOf(ctx context.Context, pool *pgxpool.Pool, biz string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id::text FROM users WHERE business_id=$1::uuid AND role='owner' LIMIT 1`, biz).Scan(&id)
	return id, err
}

func liveTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func liveSale(ctx context.Context, pool *pgxpool.Pool, biz string, rng *rand.Rand) error {
	owner, err := ownerOf(ctx, pool, biz)
	if err != nil {
		return err
	}
	var contact string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM contacts WHERE business_id=$1::uuid AND kind <> 'supplier' ORDER BY random() LIMIT 1`, biz).Scan(&contact); err != nil {
		return err
	}
	var pid, name string
	var vat int
	var price, avail int64
	err = pool.QueryRow(ctx, `SELECT p.id::text, p.name, p.vat_rate, p.sale_price,
		coalesce((SELECT sum(CASE m.direction WHEN 'in' THEN m.quantity_milli ELSE -m.quantity_milli END) FROM stock_movements m WHERE m.product_id=p.id AND m.active),0)::bigint
		FROM products p WHERE p.business_id=$1::uuid AND p.kind='product' AND p.sale_price > 0
		ORDER BY random() LIMIT 1`, biz).Scan(&pid, &name, &vat, &price, &avail)
	if err != nil {
		return err
	}
	qty := int64(1+rng.Intn(3)) * 1000
	if avail < qty {
		return nil // stok yok; bu turu atla
	}
	d := today()
	return liveTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := invoicing.CreateAndFinalize(ctx, tx, invoicing.CreateParams{BusinessID: biz, UserID: owner, Kind: "sale",
			ContactID: contact, IssueDate: d, DueDate: d.AddDate(0, 0, 30),
			Lines: []invoicing.LineInput{{ProductID: pid, Description: name, QuantityMilli: qty, UnitPrice: price, VATRate: vat}}})
		return err
	})
}

func liveReceipt(ctx context.Context, pool *pgxpool.Pool, biz string, rng *rand.Rand) error {
	owner, err := ownerOf(ctx, pool, biz)
	if err != nil {
		return err
	}
	var contact string
	var open int64
	err = pool.QueryRow(ctx, `SELECT i.contact_id::text, i.gross_total - coalesce((SELECT sum(a.amount) FROM payment_allocations a
			WHERE a.invoice_id=i.id AND a.reversed_at IS NULL),0)::bigint AS open
		FROM invoices i WHERE i.business_id=$1::uuid AND i.kind='sale' AND i.status='issued'
		  AND i.gross_total > coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.invoice_id=i.id AND a.reversed_at IS NULL),0)
		ORDER BY random() LIMIT 1`, biz).Scan(&contact, &open)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	amount := open
	if rng.Intn(100) < 40 {
		amount = open/2 + 1
	}
	return liveTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := payments.CreateTx(ctx, tx, payments.CreateParams{BusinessID: biz, UserID: owner, Kind: "receipt",
			ContactID: contact, Method: []string{"cash", "bank"}[rng.Intn(2)], Date: today(), Amount: amount})
		return err
	})
}

func livePurchase(ctx context.Context, pool *pgxpool.Pool, biz string, rng *rand.Rand) error {
	owner, err := ownerOf(ctx, pool, biz)
	if err != nil {
		return err
	}
	var contact, pid, name string
	var vat int
	var price int64
	if err := pool.QueryRow(ctx, `SELECT id::text FROM contacts WHERE business_id=$1::uuid AND kind <> 'customer' ORDER BY random() LIMIT 1`, biz).Scan(&contact); err != nil {
		return err
	}
	if err := pool.QueryRow(ctx, `SELECT id::text, name, vat_rate, sale_price*6/10 FROM products WHERE business_id=$1::uuid AND kind='product' AND sale_price > 0
		ORDER BY random() LIMIT 1`, biz).Scan(&pid, &name, &vat, &price); err != nil {
		return err
	}
	d := today()
	return liveTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := invoicing.CreateAndFinalize(ctx, tx, invoicing.CreateParams{BusinessID: biz, UserID: owner, Kind: "purchase",
			ContactID: contact, IssueDate: d, DueDate: d.AddDate(0, 0, 30),
			Lines: []invoicing.LineInput{{ProductID: pid, Description: name, QuantityMilli: int64(10+rng.Intn(40)) * 1000, UnitPrice: price, VATRate: vat}}})
		return err
	})
}
