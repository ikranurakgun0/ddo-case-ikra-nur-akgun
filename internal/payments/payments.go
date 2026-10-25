// Package payments tahsilat/ödeme kaydı, FIFO mahsup, avans ve geri alma işlemlerini içerir.
package payments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"esnafdefter/internal/invoicing"
	"esnafdefter/internal/ledger"
)

var (
	ErrNotFound        = errors.New("ödeme bulunamadı")
	ErrAlreadyDeleted  = errors.New("ödeme zaten silinmiş")
	ErrNoAdvance       = errors.New("mahsup edilecek avans yok")
	ErrAllocationLimit = errors.New("mahsup sınırı aşıldı")
)

const (
	maxAmount  = 10_000_000_000_000 // 100 milyar TL (kuruş)
	dateLayout = "2006-01-02"
)

type Allocation struct {
	InvoiceID     string `json:"invoice_id"`
	InvoiceNumber string `json:"invoice_number"`
	Amount        int64  `json:"amount"`
}

type Payment struct {
	ID          string       `json:"id"`
	Kind        string       `json:"kind"` // receipt | disbursement
	ContactID   string       `json:"contact_id"`
	Method      string       `json:"method"` // cash | bank
	Date        string       `json:"date"`
	Amount      int64        `json:"amount"`
	Status      string       `json:"status"`
	Allocated   int64        `json:"allocated"`
	Advance     int64        `json:"advance"` // mahsup edilmeyen (avans) kısım
	Allocations []Allocation `json:"allocations,omitempty"`
}

type CreateParams struct {
	BusinessID string
	UserID     string
	Kind       string
	ContactID  string
	Method     string
	Date       time.Time
	Amount     int64
}

type openInvoice struct {
	ID, Number string
	Open       int64
}

func invoiceKind(paymentKind string) string {
	if paymentKind == "receipt" {
		return "sale"
	}
	return "purchase"
}

func cashAccount(method string) string {
	if method == "cash" {
		return "100"
	}
	return "102"
}

// MapError mahsup sınırlarını aşan veritabanı hatalarını anlamlı hataya çevirir.
func MapError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "DD005" {
		return fmt.Errorf("%w: %s", ErrAllocationLimit, pe.Message)
	}
	return ledger.MapError(err)
}

// openInvoices cari için açık faturaları en eskiden yeniye (FIFO) listeler.
// Sıra: fatura tarihi, oluşturulma zamanı, id. Çağıran cari kilidini almış olmalıdır.
func openInvoices(ctx context.Context, tx pgx.Tx, businessID, contactID, kind string) ([]openInvoice, error) {
	rows, err := tx.Query(ctx,
		`SELECT i.id::text, i.series, i.year, i.number,
		        i.gross_total - coalesce((SELECT sum(a.amount) FROM payment_allocations a
		                                   WHERE a.invoice_id = i.id AND a.reversed_at IS NULL), 0)::bigint
		   FROM invoices i
		  WHERE i.business_id = $1::uuid AND i.contact_id = $2::uuid AND i.kind = $3 AND i.status = 'issued'
		  ORDER BY i.issue_date, i.created_at, i.id`, businessID, contactID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []openInvoice
	for rows.Next() {
		var o openInvoice
		var series string
		var year int
		var seq int64
		if err := rows.Scan(&o.ID, &series, &year, &seq, &o.Open); err != nil {
			return nil, err
		}
		o.Number = invoicing.FormatNumber(series, year, seq)
		if o.Open > 0 {
			out = append(out, o)
		}
	}
	return out, rows.Err()
}

// allocateFIFO tutarı açık faturalara en eskiden başlayarak dağıtır ve mahsup kayıtlarını yazar.
func allocateFIFO(ctx context.Context, tx pgx.Tx, businessID, paymentID string, invs []openInvoice, amount int64) ([]Allocation, int64, error) {
	var out []Allocation
	var total int64
	for _, inv := range invs {
		if amount == 0 {
			break
		}
		take := inv.Open
		if amount < take {
			take = amount
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO payment_allocations(business_id, payment_id, invoice_id, amount) VALUES ($1::uuid,$2::uuid,$3::uuid,$4)`,
			businessID, paymentID, inv.ID, take); err != nil {
			return nil, 0, MapError(err)
		}
		out = append(out, Allocation{InvoiceID: inv.ID, InvoiceNumber: inv.Number, Amount: take})
		amount -= take
		total += take
	}
	return out, total, nil
}

// CreateTx ödemeyi kaydeder, açık faturalara FIFO ile mahsup eder, kalanı avans olarak tutar
// ve yevmiye kaydını atar. Hepsi çağıranın transaction'ı içindedir.
func CreateTx(ctx context.Context, tx pgx.Tx, p CreateParams) (*Payment, error) {
	if p.Kind != "receipt" && p.Kind != "disbursement" {
		return nil, invoicing.NewValidationError("validation_error", "kind 'receipt' veya 'disbursement' olmalı")
	}
	if p.Method != "cash" && p.Method != "bank" {
		return nil, invoicing.NewValidationError("validation_error", "method 'cash' veya 'bank' olmalı")
	}
	if p.Amount <= 0 || p.Amount > maxAmount {
		return nil, invoicing.NewValidationError("validation_error", "tutar sıfırdan büyük olmalı")
	}
	if y := p.Date.Year(); y < 2000 || y > 2100 {
		return nil, invoicing.NewValidationError("validation_error", "tarih geçersiz")
	}
	var ckind string
	err := tx.QueryRow(ctx, `SELECT kind FROM contacts WHERE id = $1::uuid AND business_id = $2::uuid`, p.ContactID, p.BusinessID).Scan(&ckind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invoicing.NewValidationError("contact_not_found", "cari bulunamadı")
	}
	if err != nil {
		return nil, err
	}
	if (p.Kind == "receipt" && ckind == "supplier") || (p.Kind == "disbursement" && ckind == "customer") {
		return nil, invoicing.NewValidationError("contact_kind_mismatch", "cari türü bu ödeme türüne uygun değil")
	}

	// Aynı carinin ödemeleri/iptalleri sıraya girer: iki eşzamanlı tahsilat aynı faturayı fazla kapatamaz.
	if err := invoicing.LockContact(ctx, tx, p.BusinessID, p.ContactID); err != nil {
		return nil, err
	}

	pay := &Payment{Kind: p.Kind, ContactID: p.ContactID, Method: p.Method, Date: p.Date.Format(dateLayout),
		Amount: p.Amount, Status: "active"}
	err = tx.QueryRow(ctx,
		`INSERT INTO payments(business_id, kind, contact_id, method, pay_date, amount, created_by)
		 VALUES ($1::uuid,$2,$3::uuid,$4,$5,$6,NULLIF($7,'')::uuid) RETURNING id::text`,
		p.BusinessID, p.Kind, p.ContactID, p.Method, p.Date, p.Amount, p.UserID).Scan(&pay.ID)
	if err != nil {
		return nil, err
	}

	invs, err := openInvoices(ctx, tx, p.BusinessID, p.ContactID, invoiceKind(p.Kind))
	if err != nil {
		return nil, err
	}
	allocs, allocated, err := allocateFIFO(ctx, tx, p.BusinessID, pay.ID, invs, p.Amount)
	if err != nil {
		return nil, err
	}
	pay.Allocations, pay.Allocated, pay.Advance = allocs, allocated, p.Amount-allocated

	cash := cashAccount(p.Method)
	e := ledger.Entry{BusinessID: p.BusinessID, Date: p.Date, SourceType: "payment", SourceID: pay.ID, CreatedBy: p.UserID}
	if p.Kind == "receipt" {
		e.Description = "Tahsilat"
		e.Lines = []ledger.Line{{Account: cash, Debit: p.Amount}}
		if allocated > 0 {
			e.Lines = append(e.Lines, ledger.Line{Account: "120", Credit: allocated, ContactID: p.ContactID})
		}
		if pay.Advance > 0 {
			e.Lines = append(e.Lines, ledger.Line{Account: "340", Credit: pay.Advance, ContactID: p.ContactID})
		}
	} else {
		e.Description = "Ödeme"
		e.Lines = []ledger.Line{{Account: cash, Credit: p.Amount}}
		if allocated > 0 {
			e.Lines = append(e.Lines, ledger.Line{Account: "320", Debit: allocated, ContactID: p.ContactID})
		}
		if pay.Advance > 0 {
			e.Lines = append(e.Lines, ledger.Line{Account: "159", Debit: pay.Advance, ContactID: p.ContactID})
		}
	}
	if _, err := ledger.Post(ctx, tx, e); err != nil {
		return nil, err
	}
	return pay, nil
}

// ApplyAdvanceTx ödemenin mahsup edilmemiş (avans) kısmını, sonradan kesilmiş açık faturalara FIFO ile mahsup eder.
// Avans hesabından cari hesaba aktarma kaydı atılır (340 -> 120 ya da 320 <- 159).
func ApplyAdvanceTx(ctx context.Context, tx pgx.Tx, businessID, userID, paymentID string, date time.Time) (*Payment, error) {
	var contactID string
	err := tx.QueryRow(ctx, `SELECT contact_id::text FROM payments WHERE id = $1::uuid AND business_id = $2::uuid`, paymentID, businessID).Scan(&contactID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := invoicing.LockContact(ctx, tx, businessID, contactID); err != nil {
		return nil, err
	}
	var kind, status string
	var amount, allocated int64
	if err := tx.QueryRow(ctx,
		`SELECT p.kind, p.status, p.amount,
		        coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.payment_id = p.id AND a.reversed_at IS NULL),0)::bigint
		   FROM payments p WHERE p.id = $1::uuid FOR UPDATE`, paymentID).Scan(&kind, &status, &amount, &allocated); err != nil {
		return nil, err
	}
	if status != "active" {
		return nil, ErrAlreadyDeleted
	}
	advance := amount - allocated
	if advance <= 0 {
		return nil, ErrNoAdvance
	}
	invs, err := openInvoices(ctx, tx, businessID, contactID, invoiceKind(kind))
	if err != nil {
		return nil, err
	}
	_, moved, err := allocateFIFO(ctx, tx, businessID, paymentID, invs, advance)
	if err != nil {
		return nil, err
	}
	if moved > 0 {
		e := ledger.Entry{BusinessID: businessID, Date: date, Description: "Avans mahsubu", SourceType: "payment", SourceID: paymentID, CreatedBy: userID}
		if kind == "receipt" {
			e.Lines = []ledger.Line{{Account: "340", Debit: moved, ContactID: contactID}, {Account: "120", Credit: moved, ContactID: contactID}}
		} else {
			e.Lines = []ledger.Line{{Account: "320", Debit: moved, ContactID: contactID}, {Account: "159", Credit: moved, ContactID: contactID}}
		}
		if _, err := ledger.Post(ctx, tx, e); err != nil {
			return nil, err
		}
	}
	return Get(ctx, tx, businessID, paymentID)
}

// DeleteTx ödemeyi siler: yevmiye kayıtları ters kayıtla kapatılır, mahsuplar geri alınır
// (faturalar yeniden açılır). Ödeme satırı silinmez, status='deleted' olur.
func DeleteTx(ctx context.Context, tx pgx.Tx, businessID, userID, paymentID string, date time.Time) error {
	var contactID string
	err := tx.QueryRow(ctx, `SELECT contact_id::text FROM payments WHERE id = $1::uuid AND business_id = $2::uuid`, paymentID, businessID).Scan(&contactID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := invoicing.LockContact(ctx, tx, businessID, contactID); err != nil {
		return err
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM payments WHERE id = $1::uuid FOR UPDATE`, paymentID).Scan(&status); err != nil {
		return err
	}
	if status == "deleted" {
		return ErrAlreadyDeleted
	}
	rows, err := tx.Query(ctx,
		`SELECT e.id::text FROM journal_entries e
		  WHERE e.business_id = $1::uuid AND e.source_type = 'payment' AND e.source_id = $2::uuid AND e.reverses_id IS NULL
		    AND NOT EXISTS (SELECT 1 FROM journal_entries r WHERE r.reverses_id = e.id)
		  ORDER BY e.created_at, e.id`, businessID, paymentID)
	if err != nil {
		return err
	}
	var entries []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range entries {
		if _, err := ledger.Reverse(ctx, tx, businessID, id, date, userID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE payment_allocations SET reversed_at = now() WHERE payment_id = $1::uuid AND reversed_at IS NULL`, paymentID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE payments SET status = 'deleted', deleted_at = now() WHERE id = $1::uuid`, paymentID)
	return err
}

const paymentCols = `p.id::text, p.kind, p.contact_id::text, p.method, p.pay_date, p.amount, p.status,
	CASE WHEN p.status = 'active' THEN coalesce((SELECT sum(a.amount) FROM payment_allocations a
	                                              WHERE a.payment_id = p.id AND a.reversed_at IS NULL),0) ELSE 0 END::bigint`

type scanner interface{ Scan(dest ...any) error }

func scanPayment(r scanner) (*Payment, error) {
	var p Payment
	var d time.Time
	if err := r.Scan(&p.ID, &p.Kind, &p.ContactID, &p.Method, &d, &p.Amount, &p.Status, &p.Allocated); err != nil {
		return nil, err
	}
	p.Date = d.Format(dateLayout)
	if p.Status == "active" {
		p.Advance = p.Amount - p.Allocated
	}
	return &p, nil
}

// Get ödemeyi mahsuplarıyla getirir.
func Get(ctx context.Context, q ledger.Querier, businessID, id string) (*Payment, error) {
	p, err := scanPayment(q.QueryRow(ctx, `SELECT `+paymentCols+` FROM payments p WHERE p.id = $1::uuid AND p.business_id = $2::uuid`, id, businessID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx,
		`SELECT a.invoice_id::text, i.series, i.year, i.number, a.amount
		   FROM payment_allocations a JOIN invoices i ON i.id = a.invoice_id
		  WHERE a.payment_id = $1::uuid AND a.reversed_at IS NULL ORDER BY a.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Allocation
		var series string
		var year int
		var seq int64
		if err := rows.Scan(&a.InvoiceID, &series, &year, &seq, &a.Amount); err != nil {
			return nil, err
		}
		a.InvoiceNumber = invoicing.FormatNumber(series, year, seq)
		p.Allocations = append(p.Allocations, a)
	}
	return p, rows.Err()
}

// List keyset sayfalama (id'ye göre artan).
func List(ctx context.Context, q ledger.Querier, businessID, after string, limit int) ([]Payment, error) {
	rows, err := q.Query(ctx,
		`SELECT `+paymentCols+` FROM payments p
		  WHERE p.business_id = $1::uuid AND ($2 = '' OR p.id > $2::uuid) ORDER BY p.id LIMIT $3`, businessID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Payment{}
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
