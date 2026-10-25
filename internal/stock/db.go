package stock

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"esnafdefter/internal/ledger"
)

// InsufficientError stok negatife düşecekken ve politika buna izin vermiyorsa döner.
type InsufficientError struct {
	SKU, Name string
	MinQty    int64
}

func (e *InsufficientError) Error() string {
	return fmt.Sprintf("yetersiz stok: %s (%s)", e.Name, e.SKU)
}

// Set bir transaction içinde etkilenen ürünlerin kümesidir.
type Set map[string]struct{}

func (s Set) Add(id string) { s[id] = struct{}{} }

func (s Set) Sorted() []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LockProducts ürünleri id sırasıyla kilitler (sıralı kilit = kilitlenme/deadlock yok).
// Aynı ürünün hareketlerini değiştiren işlemler bu kilitle sıraya girer; böylece
// "iki eşzamanlı satış son stoku birlikte alamaz".
func LockProducts(ctx context.Context, tx pgx.Tx, businessID string, ids []string) (map[string]string, error) {
	kinds := map[string]string{}
	if len(ids) == 0 {
		return kinds, nil
	}
	rows, err := tx.Query(ctx,
		`SELECT id::text, kind FROM products
		  WHERE business_id = $1::uuid AND id = ANY($2::text[]::uuid[]) ORDER BY id FOR UPDATE`, businessID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			return nil, err
		}
		kinds[id] = kind
	}
	return kinds, rows.Err()
}

func InsertMovement(ctx context.Context, tx pgx.Tx, businessID, productID, direction string, date time.Time,
	qtyMilli, value int64, invoiceID string, lineNo int) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO stock_movements(business_id, product_id, direction, movement_date, quantity_milli, value, invoice_id, line_no)
		 VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6,$7::uuid,$8)`,
		businessID, productID, direction, date, qtyMilli, value, invoiceID, lineNo)
	return err
}

// ProductsOfInvoice faturanın stok hareketlerindeki ürünleri (id sırasıyla) döner.
func ProductsOfInvoice(ctx context.Context, tx pgx.Tx, businessID, invoiceID string) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT DISTINCT product_id::text FROM stock_movements
		  WHERE business_id = $1::uuid AND invoice_id = $2::uuid ORDER BY 1`, businessID, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeactivateInvoice faturanın stok hareketlerini iptal eder (silmez).
func DeactivateInvoice(ctx context.Context, tx pgx.Tx, businessID, invoiceID string) error {
	_, err := tx.Exec(ctx,
		`UPDATE stock_movements SET active = false
		  WHERE business_id = $1::uuid AND invoice_id = $2::uuid AND active`, businessID, invoiceID)
	return err
}

// Finalize etkilenen ürünlerin maliyetlerini baştan oynatarak yeniden hesaplar.
//   - Negatif stok politikası kapalıysa ve herhangi bir anda stok eksiye iniyorsa InsufficientError döner.
//   - Maliyeti değişen her çıkış için audit_log'a eski/yeni değer yazılır.
//   - Toplam maliyet farkı SMM (621) / Ticari Mallar (153) düzeltme kaydı olarak postDate tarihine yazılır.
//
// Çağıran, ürünleri önceden LockProducts ile kilitlemiş olmalıdır.
func Finalize(ctx context.Context, tx pgx.Tx, businessID, userID string, touched Set, postDate time.Time) error {
	if len(touched) == 0 {
		return nil
	}
	var allowNeg bool
	if err := tx.QueryRow(ctx, `SELECT allow_negative_stock FROM business_settings WHERE business_id = $1::uuid`, businessID).Scan(&allowNeg); err != nil {
		return err
	}
	for _, pid := range touched.Sorted() {
		if err := recompute(ctx, tx, businessID, userID, pid, allowNeg, postDate); err != nil {
			return err
		}
	}
	return nil
}

func recompute(ctx context.Context, tx pgx.Tx, businessID, userID, pid string, allowNeg bool, postDate time.Time) error {
	rows, err := tx.Query(ctx,
		`SELECT id, direction = 'out', quantity_milli, value, active FROM stock_movements
		  WHERE business_id = $1::uuid AND product_id = $2::uuid
		  ORDER BY movement_date, (direction = 'out'), id`, businessID, pid)
	if err != nil {
		return err
	}
	var moves []Move
	old := map[int64]int64{}
	for rows.Next() {
		var m Move
		var value int64
		if err := rows.Scan(&m.ID, &m.Out, &m.Qty, &value, &m.Active); err != nil {
			rows.Close()
			return err
		}
		if m.Out {
			old[m.ID] = value
		} else {
			m.InValue = value
		}
		moves = append(moves, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	res := Replay(moves)
	if res.MinQty < 0 && !allowNeg {
		var sku, name string
		_ = tx.QueryRow(ctx, `SELECT sku, name FROM products WHERE id = $1::uuid`, pid).Scan(&sku, &name)
		return &InsufficientError{SKU: sku, Name: name, MinQty: res.MinQty}
	}

	var delta int64
	for _, m := range moves {
		if !m.Out {
			continue
		}
		next := res.Cost[m.ID]
		prev := old[m.ID]
		if next == prev {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE stock_movements SET value = $2 WHERE id = $1`, m.ID, next); err != nil {
			return err
		}
		if prev != 0 { // ilk maliyet ataması değişiklik değildir; gerçek yeniden hesaplar denetim kaydına yazılır
			if _, err := tx.Exec(ctx,
				`INSERT INTO audit_log(business_id, actor_id, action, entity, entity_id, old_value, new_value)
				 VALUES ($1::uuid, NULLIF($2,'')::uuid, 'stock_cost_recalc', 'stock_movement', $3,
				         jsonb_build_object('value', $4::bigint), jsonb_build_object('value', $5::bigint))`,
				businessID, userID, fmt.Sprint(m.ID), prev, next); err != nil {
				return err
			}
		}
		delta += next - prev
	}
	if delta == 0 {
		return nil
	}
	var sku string
	_ = tx.QueryRow(ctx, `SELECT sku FROM products WHERE id = $1::uuid`, pid).Scan(&sku)
	lines := []ledger.Line{{Account: "621", Debit: delta}, {Account: "153", Credit: delta}}
	if delta < 0 {
		lines = []ledger.Line{{Account: "153", Debit: -delta}, {Account: "621", Credit: -delta}}
	}
	_, err = ledger.Post(ctx, tx, ledger.Entry{
		BusinessID: businessID, Date: postDate, Description: "Maliyet hesabı: " + sku,
		SourceType: "stock", SourceID: pid, CreatedBy: userID, Lines: lines,
	})
	return err
}

type ProductStock struct {
	ProductID     string `json:"product_id"`
	SKU           string `json:"sku"`
	Name          string `json:"name"`
	Unit          string `json:"unit"`
	QuantityMilli int64  `json:"quantity_milli"`
	Value         int64  `json:"value"`
	AvgUnitCost   int64  `json:"avg_unit_cost"`
}

type StockReport struct {
	Products   []ProductStock `json:"products"`
	TotalValue int64          `json:"total_value"`
}

// Report ürün bazında stok miktarı ve değerini döner (yalnızca etkin hareketler).
func Report(ctx context.Context, q ledger.Querier, businessID string) (*StockReport, error) {
	rows, err := q.Query(ctx,
		`SELECT p.id::text, p.sku, p.name, p.unit,
		        coalesce(sum(CASE WHEN m.active THEN CASE m.direction WHEN 'in' THEN m.quantity_milli ELSE -m.quantity_milli END END),0)::bigint,
		        coalesce(sum(CASE WHEN m.active THEN CASE m.direction WHEN 'in' THEN m.value ELSE -m.value END END),0)::bigint
		   FROM products p LEFT JOIN stock_movements m ON m.product_id = p.id
		  WHERE p.business_id = $1::uuid AND p.kind = 'product'
		  GROUP BY p.id ORDER BY p.sku`, businessID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rep := &StockReport{Products: []ProductStock{}}
	for rows.Next() {
		var p ProductStock
		if err := rows.Scan(&p.ProductID, &p.SKU, &p.Name, &p.Unit, &p.QuantityMilli, &p.Value); err != nil {
			return nil, err
		}
		p.AvgUnitCost = AvgUnitCost(p.QuantityMilli, p.Value)
		rep.TotalValue += p.Value
		rep.Products = append(rep.Products, p)
	}
	return rep, rows.Err()
}

type MovementRow struct {
	ID            int64  `json:"id"`
	ProductID     string `json:"product_id"`
	Direction     string `json:"direction"`
	Date          string `json:"date"`
	QuantityMilli int64  `json:"quantity_milli"`
	Value         int64  `json:"value"`
	Active        bool   `json:"active"`
	InvoiceID     string `json:"invoice_id"`
}

// Movements keyset sayfalama (id'ye göre). desc=true ise en yeni hareket önce gelir ve after, son görülen id'dir
// (after=0 "baştan" demektir).
func Movements(ctx context.Context, q ledger.Querier, businessID, productID string, after int64, limit int, desc bool) ([]MovementRow, error) {
	order, cmp := "ASC", ">"
	if desc {
		order, cmp = "DESC", "<"
		if after == 0 {
			after = 1 << 62
		}
	}
	rows, err := q.Query(ctx,
		`SELECT id, product_id::text, direction, movement_date, quantity_milli, value, active, invoice_id::text
		   FROM stock_movements
		  WHERE business_id = $1::uuid AND ($2 = '' OR product_id = $2::uuid) AND id `+cmp+` $3
		  ORDER BY id `+order+` LIMIT $4`, businessID, productID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MovementRow{}
	for rows.Next() {
		var m MovementRow
		var d time.Time
		if err := rows.Scan(&m.ID, &m.ProductID, &m.Direction, &d, &m.QuantityMilli, &m.Value, &m.Active, &m.InvoiceID); err != nil {
			return nil, err
		}
		m.Date = d.Format("2006-01-02")
		out = append(out, m)
	}
	return out, rows.Err()
}

var ErrNotFound = errors.New("ürün bulunamadı")
