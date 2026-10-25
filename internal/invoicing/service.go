package invoicing

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"esnafdefter/internal/ledger"
)

var (
	ErrNotFound         = errors.New("fatura bulunamadı")
	ErrAlreadyCancelled = errors.New("fatura zaten iptal edilmiş")
)

var seriesRe = regexp.MustCompile(`^[A-Z0-9]{1,8}$`)

const dateLayout = "2006-01-02"

type CreateParams struct {
	BusinessID      string
	UserID          string
	Kind            string // sale | purchase
	ContactID       string
	Series          string // boşsa satış için "A", alış için "AL"
	IssueDate       time.Time
	DueDate         time.Time
	IncludesVAT     bool
	GeneralDiscount int64
	Lines           []LineInput
}

type InvoiceLine struct {
	LineNo            int    `json:"line_no"`
	Description       string `json:"description"`
	QuantityMilli     int64  `json:"quantity_milli"`
	UnitPrice         int64  `json:"unit_price"`
	VATRate           int    `json:"vat_rate"`
	Discount          int64  `json:"discount"`
	AllocatedDiscount int64  `json:"allocated_discount"`
	Net               int64  `json:"net"`
	VAT               int64  `json:"vat"`
	Gross             int64  `json:"gross"`
}

type Invoice struct {
	ID               string        `json:"id"`
	Kind             string        `json:"kind"`
	Number           string        `json:"number"` // A-2026-000001
	Series           string        `json:"series"`
	Year             int           `json:"year"`
	Seq              int64         `json:"seq"`
	ContactID        string        `json:"contact_id"`
	IssueDate        string        `json:"issue_date"`
	DueDate          string        `json:"due_date"`
	Status           string        `json:"status"`
	PricesIncludeVAT bool          `json:"prices_include_vat"`
	GeneralDiscount  int64         `json:"general_discount"`
	NetTotal         int64         `json:"net_total"`
	VATTotal         int64         `json:"vat_total"`
	GrossTotal       int64         `json:"gross_total"`
	Lines            []InvoiceLine `json:"lines,omitempty"`
}

func FormatNumber(series string, year int, seq int64) string {
	return fmt.Sprintf("%s-%d-%06d", series, year, seq)
}

// CreateTx faturayı, satırlarını ve yevmiye kaydını TEK transaction içinde yazar.
// Fatura numarası transaction'ın sonuna doğru, kayıtlardan hemen önce alınır; hata olursa
// transaction geri alınır ve numara da geri döner (boşluk oluşmaz).
func CreateTx(ctx context.Context, tx pgx.Tx, p CreateParams) (*Invoice, error) {
	if p.Kind != "sale" && p.Kind != "purchase" {
		return nil, verr("validation_error", "kind 'sale' veya 'purchase' olmalı")
	}
	if p.Series == "" {
		if p.Kind == "sale" {
			p.Series = "A"
		} else {
			p.Series = "AL"
		}
	}
	if !seriesRe.MatchString(p.Series) {
		return nil, verr("validation_error", "seri 1-8 karakter, büyük harf/rakam olmalı")
	}
	if p.DueDate.Before(p.IssueDate) {
		return nil, verr("validation_error", "vade tarihi fatura tarihinden önce olamaz")
	}
	if y := p.IssueDate.Year(); y < 2000 || y > 2100 {
		return nil, verr("validation_error", "fatura tarihi geçersiz")
	}

	res, err := Calculate(p.Lines, p.GeneralDiscount, p.IncludesVAT)
	if err != nil {
		return nil, err
	}

	var ckind string
	err = tx.QueryRow(ctx, `SELECT kind FROM contacts WHERE id = $1::uuid AND business_id = $2::uuid`, p.ContactID, p.BusinessID).Scan(&ckind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, verr("contact_not_found", "cari bulunamadı")
	}
	if err != nil {
		return nil, err
	}
	if (p.Kind == "sale" && ckind == "supplier") || (p.Kind == "purchase" && ckind == "customer") {
		return nil, verr("contact_kind_mismatch", "cari türü bu fatura türüne uygun değil")
	}

	year := p.IssueDate.Year()
	var seq int64
	// Satır kilidi transaction sonuna kadar tutulur: aynı seri/yıl için faturalar sıraya girer.
	err = tx.QueryRow(ctx,
		`INSERT INTO invoice_counters(business_id, series, year, last_no) VALUES ($1::uuid, $2, $3, 1)
		 ON CONFLICT (business_id, series, year) DO UPDATE SET last_no = invoice_counters.last_no + 1
		 RETURNING last_no`, p.BusinessID, p.Series, year).Scan(&seq)
	if err != nil {
		return nil, err
	}

	inv := &Invoice{
		Kind: p.Kind, Series: p.Series, Year: year, Seq: seq, Number: FormatNumber(p.Series, year, seq),
		ContactID: p.ContactID, IssueDate: p.IssueDate.Format(dateLayout), DueDate: p.DueDate.Format(dateLayout),
		Status: "issued", PricesIncludeVAT: p.IncludesVAT, GeneralDiscount: p.GeneralDiscount,
		NetTotal: res.Net, VATTotal: res.VAT, GrossTotal: res.Gross,
	}
	err = tx.QueryRow(ctx,
		`INSERT INTO invoices(business_id, kind, contact_id, series, year, number, issue_date, due_date,
		                      prices_include_vat, general_discount, net_total, vat_total, gross_total, created_by)
		 VALUES ($1::uuid,$2,$3::uuid,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,'')::uuid) RETURNING id::text`,
		p.BusinessID, p.Kind, p.ContactID, p.Series, year, seq, p.IssueDate, p.DueDate,
		p.IncludesVAT, p.GeneralDiscount, res.Net, res.VAT, res.Gross, p.UserID).Scan(&inv.ID)
	if err != nil {
		return nil, err
	}
	for i, l := range res.Lines {
		if _, err := tx.Exec(ctx,
			`INSERT INTO invoice_lines(invoice_id, business_id, line_no, description, quantity_milli, unit_price,
			                           vat_rate, line_discount, allocated_discount, net_amount, vat_amount, gross_amount)
			 VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			inv.ID, p.BusinessID, i+1, l.Description, l.QuantityMilli, l.UnitPrice, l.VATRate, l.Discount,
			l.AllocatedDiscount, l.Net, l.VAT, l.Gross); err != nil {
			return nil, err
		}
		inv.Lines = append(inv.Lines, InvoiceLine{
			LineNo: i + 1, Description: l.Description, QuantityMilli: l.QuantityMilli, UnitPrice: l.UnitPrice,
			VATRate: l.VATRate, Discount: l.Discount, AllocatedDiscount: l.AllocatedDiscount,
			Net: l.Net, VAT: l.VAT, Gross: l.Gross,
		})
	}

	entry := ledger.Entry{
		BusinessID: p.BusinessID, Date: p.IssueDate, SourceType: "invoice", SourceID: inv.ID, CreatedBy: p.UserID,
	}
	if p.Kind == "sale" {
		entry.Description = "Satış faturası " + inv.Number
		entry.Lines = []ledger.Line{{Account: "120", Debit: res.Gross, ContactID: p.ContactID}, {Account: "600", Credit: res.Net}}
		if res.VAT > 0 {
			entry.Lines = append(entry.Lines, ledger.Line{Account: "391", Credit: res.VAT})
		}
	} else {
		entry.Description = "Alış faturası " + inv.Number
		entry.Lines = []ledger.Line{{Account: "153", Debit: res.Net}, {Account: "320", Credit: res.Gross, ContactID: p.ContactID}}
		if res.VAT > 0 {
			entry.Lines = append(entry.Lines, ledger.Line{Account: "191", Debit: res.VAT})
		}
	}
	if _, err := ledger.Post(ctx, tx, entry); err != nil {
		return nil, err
	}
	return inv, nil
}

// CancelTx faturayı iptal eder: fatura silinmez, yevmiye kaydı ters kayıtla kapatılır.
// date ters kaydın tarihidir; kapatılmış bir döneme denk gelirse ledger.ErrPeriodClosed döner.
func CancelTx(ctx context.Context, tx pgx.Tx, businessID, userID, invoiceID string, date time.Time) error {
	var status string
	err := tx.QueryRow(ctx,
		`SELECT status FROM invoices WHERE id = $1::uuid AND business_id = $2::uuid FOR UPDATE`, invoiceID, businessID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == "cancelled" {
		return ErrAlreadyCancelled
	}
	var entryID string
	if err := tx.QueryRow(ctx,
		`SELECT id::text FROM journal_entries
		  WHERE business_id = $1::uuid AND source_type = 'invoice' AND source_id = $2::uuid AND reverses_id IS NULL`,
		businessID, invoiceID).Scan(&entryID); err != nil {
		return err
	}
	if _, err := ledger.Reverse(ctx, tx, businessID, entryID, date, userID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE invoices SET status = 'cancelled', cancelled_at = now() WHERE id = $1::uuid`, invoiceID)
	return err
}

const invoiceCols = `id::text, kind, series, year, number, contact_id::text, issue_date, due_date, status,
	prices_include_vat, general_discount, net_total, vat_total, gross_total`

func scanInvoice(row pgx.Row) (*Invoice, error) {
	var i Invoice
	var issue, due time.Time
	if err := row.Scan(&i.ID, &i.Kind, &i.Series, &i.Year, &i.Seq, &i.ContactID, &issue, &due, &i.Status,
		&i.PricesIncludeVAT, &i.GeneralDiscount, &i.NetTotal, &i.VATTotal, &i.GrossTotal); err != nil {
		return nil, err
	}
	i.Number = FormatNumber(i.Series, i.Year, i.Seq)
	i.IssueDate, i.DueDate = issue.Format(dateLayout), due.Format(dateLayout)
	return &i, nil
}

// Get faturayı satırlarıyla getirir; yalnızca verilen işletmeye aitse bulunur.
func Get(ctx context.Context, q ledger.Querier, businessID, id string) (*Invoice, error) {
	inv, err := scanInvoice(q.QueryRow(ctx,
		`SELECT `+invoiceCols+` FROM invoices WHERE id = $1::uuid AND business_id = $2::uuid`, id, businessID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx,
		`SELECT line_no, description, quantity_milli, unit_price, vat_rate, line_discount, allocated_discount,
		        net_amount, vat_amount, gross_amount
		   FROM invoice_lines WHERE invoice_id = $1::uuid ORDER BY line_no`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l InvoiceLine
		if err := rows.Scan(&l.LineNo, &l.Description, &l.QuantityMilli, &l.UnitPrice, &l.VATRate, &l.Discount,
			&l.AllocatedDiscount, &l.Net, &l.VAT, &l.Gross); err != nil {
			return nil, err
		}
		inv.Lines = append(inv.Lines, l)
	}
	return inv, rows.Err()
}

// List keyset sayfalama kullanır (id'ye göre artan; after = önceki sayfanın son id'si).
func List(ctx context.Context, q ledger.Querier, businessID, after string, limit int) ([]Invoice, error) {
	rows, err := q.Query(ctx,
		`SELECT `+invoiceCols+` FROM invoices
		  WHERE business_id = $1::uuid AND ($2 = '' OR id > $2::uuid)
		  ORDER BY id LIMIT $3`, businessID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invoice{}
	for rows.Next() {
		var i Invoice
		var issue, due time.Time
		if err := rows.Scan(&i.ID, &i.Kind, &i.Series, &i.Year, &i.Seq, &i.ContactID, &issue, &due, &i.Status,
			&i.PricesIncludeVAT, &i.GeneralDiscount, &i.NetTotal, &i.VATTotal, &i.GrossTotal); err != nil {
			return nil, err
		}
		i.Number = FormatNumber(i.Series, i.Year, i.Seq)
		i.IssueDate, i.DueDate = issue.Format(dateLayout), due.Format(dateLayout)
		out = append(out, i)
	}
	return out, rows.Err()
}
