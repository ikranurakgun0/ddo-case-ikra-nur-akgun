package invoicing

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"esnafdefter/internal/ledger"
	"esnafdefter/internal/stock"
)

var (
	ErrNotFound         = errors.New("fatura bulunamadı")
	ErrAlreadyCancelled = errors.New("fatura zaten iptal edilmiş")
	ErrHasPayments      = errors.New("fatura tahsilat/ödeme ile mahsup edilmiş")
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
	ProductID         string `json:"product_id,omitempty"`
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
	ContactName      string        `json:"contact_name"`
	IssueDate        string        `json:"issue_date"`
	DueDate          string        `json:"due_date"`
	Status           string        `json:"status"`
	PricesIncludeVAT bool          `json:"prices_include_vat"`
	GeneralDiscount  int64         `json:"general_discount"`
	NetTotal         int64         `json:"net_total"`
	VATTotal         int64         `json:"vat_total"`
	GrossTotal       int64         `json:"gross_total"`
	CreatedAt        time.Time     `json:"created_at"`
	Paid             int64         `json:"paid"` // mahsup edilen tutar
	Open             int64         `json:"open"` // kalan tutar (iptal edilmişse 0)
	Lines            []InvoiceLine `json:"lines,omitempty"`
}

func FormatNumber(series string, year int, seq int64) string {
	return fmt.Sprintf("%s-%d-%06d", series, year, seq)
}

// LockContact carinin satırını kilitler. Tahsilat mahsubu, fatura iptali gibi aynı carinin
// bakiyesini değiştiren işlemleri sıraya sokar. NO KEY UPDATE: fatura eklemeyi (FK) engellemez.
func LockContact(ctx context.Context, tx pgx.Tx, businessID, contactID string) error {
	var one int
	err := tx.QueryRow(ctx,
		`SELECT 1 FROM contacts WHERE id = $1::uuid AND business_id = $2::uuid FOR NO KEY UPDATE`, contactID, businessID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return verr("contact_not_found", "cari bulunamadı")
	}
	return err
}

// CreateAndFinalize faturayı oluşturur ve stok maliyetlerini aynı transaction içinde kesinleştirir.
func CreateAndFinalize(ctx context.Context, tx pgx.Tx, p CreateParams) (*Invoice, error) {
	touched := stock.Set{}
	inv, err := CreateTx(ctx, tx, p, touched)
	if err != nil {
		return nil, err
	}
	if err := stock.Finalize(ctx, tx, p.BusinessID, p.UserID, touched, p.IssueDate); err != nil {
		return nil, err
	}
	return inv, nil
}

// CancelAndFinalize faturayı iptal eder ve stok maliyetlerini kesinleştirir.
func CancelAndFinalize(ctx context.Context, tx pgx.Tx, businessID, userID, invoiceID string, date time.Time) error {
	touched := stock.Set{}
	if err := CancelTx(ctx, tx, businessID, userID, invoiceID, date, touched); err != nil {
		return err
	}
	return stock.Finalize(ctx, tx, businessID, userID, touched, date)
}

// CorrectAndFinalize bir ALIŞ faturasını düzeltir: eski fatura iptal edilir (ters kayıt), yenisi
// kesilir. Geçmişe dönük alış değiştiği için sonraki tüm çıkışların maliyeti ve SMM kayıtları,
// ara durumda stok eksiye düşse bile işlem sonunda tek seferde yeniden hesaplanır.
func CorrectAndFinalize(ctx context.Context, tx pgx.Tx, originalID string, p CreateParams, cancelDate time.Time) (*Invoice, error) {
	var kind, contact string
	err := tx.QueryRow(ctx, `SELECT kind, contact_id::text FROM invoices WHERE id = $1::uuid AND business_id = $2::uuid`,
		originalID, p.BusinessID).Scan(&kind, &contact)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if kind != "purchase" || p.Kind != "purchase" {
		return nil, verr("not_correctable", "yalnızca alış faturaları düzeltilebilir")
	}
	if contact != p.ContactID {
		return nil, verr("contact_mismatch", "düzeltme aynı cariye ait olmalı")
	}
	touched := stock.Set{}
	if err := CancelTx(ctx, tx, p.BusinessID, p.UserID, originalID, cancelDate, touched); err != nil {
		return nil, err
	}
	inv, err := CreateTx(ctx, tx, p, touched)
	if err != nil {
		return nil, err
	}
	if err := stock.Finalize(ctx, tx, p.BusinessID, p.UserID, touched, cancelDate); err != nil {
		return nil, err
	}
	return inv, nil
}

// CreateTx faturayı, satırlarını, stok hareketlerini ve yevmiye kaydını TEK transaction içinde yazar.
// Fatura numarası kayıtlardan hemen önce alınır; hata olursa transaction geri alınır ve numara
// da geri döner (boşluk oluşmaz). Stok maliyetleri çağıranın stock.Finalize çağrısıyla kesinleşir.
func CreateTx(ctx context.Context, tx pgx.Tx, p CreateParams, touched stock.Set) (*Invoice, error) {
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

	// Ürünleri doğrula ve kilitle (id sırasıyla; sayaçtan ÖNCE, sayaç kilidi kısa kalsın).
	seen := map[string]bool{}
	var pids []string
	for i, l := range p.Lines {
		if l.ProductID == "" {
			continue
		}
		if !uuidRe.MatchString(l.ProductID) {
			return nil, verr("product_not_found", "satır %d: ürün bulunamadı", i+1)
		}
		if !seen[l.ProductID] {
			seen[l.ProductID] = true
			pids = append(pids, l.ProductID)
		}
	}
	kinds, err := stock.LockProducts(ctx, tx, p.BusinessID, pids)
	if err != nil {
		return nil, err
	}
	for i, l := range p.Lines {
		if l.ProductID == "" {
			continue
		}
		if _, ok := kinds[l.ProductID]; !ok {
			return nil, verr("product_not_found", "satır %d: ürün bulunamadı", i+1)
		}
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
		NetTotal: res.Net, VATTotal: res.VAT, GrossTotal: res.Gross, Open: res.Gross,
	}
	err = tx.QueryRow(ctx,
		`INSERT INTO invoices(business_id, kind, contact_id, series, year, number, issue_date, due_date,
		                      prices_include_vat, general_discount, net_total, vat_total, gross_total, created_by)
		 VALUES ($1::uuid,$2,$3::uuid,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,'')::uuid) RETURNING id::text, created_at`,
		p.BusinessID, p.Kind, p.ContactID, p.Series, year, seq, p.IssueDate, p.DueDate,
		p.IncludesVAT, p.GeneralDiscount, res.Net, res.VAT, res.Gross, p.UserID).Scan(&inv.ID, &inv.CreatedAt)
	if err != nil {
		return nil, err
	}

	var stockNet, otherNet int64 // alış yevmiyesinde 153 (stok) ve 770 (diğer) ayrımı için
	for i, l := range res.Lines {
		if _, err := tx.Exec(ctx,
			`INSERT INTO invoice_lines(invoice_id, business_id, line_no, product_id, description, quantity_milli, unit_price,
			                           vat_rate, line_discount, allocated_discount, net_amount, vat_amount, gross_amount)
			 VALUES ($1::uuid,$2::uuid,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			inv.ID, p.BusinessID, i+1, l.ProductID, l.Description, l.QuantityMilli, l.UnitPrice, l.VATRate, l.Discount,
			l.AllocatedDiscount, l.Net, l.VAT, l.Gross); err != nil {
			return nil, err
		}
		inv.Lines = append(inv.Lines, InvoiceLine{
			LineNo: i + 1, ProductID: l.ProductID, Description: l.Description, QuantityMilli: l.QuantityMilli,
			UnitPrice: l.UnitPrice, VATRate: l.VATRate, Discount: l.Discount, AllocatedDiscount: l.AllocatedDiscount,
			Net: l.Net, VAT: l.VAT, Gross: l.Gross,
		})
		if l.ProductID != "" && kinds[l.ProductID] == "product" {
			dir, value := "out", int64(0)
			if p.Kind == "purchase" {
				dir, value = "in", l.Net
				stockNet += l.Net
			}
			if err := stock.InsertMovement(ctx, tx, p.BusinessID, l.ProductID, dir, p.IssueDate, l.QuantityMilli, value, inv.ID, i+1); err != nil {
				return nil, err
			}
			touched.Add(l.ProductID)
		} else {
			otherNet += l.Net
		}
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
		entry.Lines = []ledger.Line{{Account: "320", Credit: res.Gross, ContactID: p.ContactID}}
		if stockNet > 0 {
			entry.Lines = append(entry.Lines, ledger.Line{Account: "153", Debit: stockNet})
		}
		if otherNet > 0 {
			entry.Lines = append(entry.Lines, ledger.Line{Account: "770", Debit: otherNet})
		}
		if res.VAT > 0 {
			entry.Lines = append(entry.Lines, ledger.Line{Account: "191", Debit: res.VAT})
		}
	}
	if _, err := ledger.Post(ctx, tx, entry); err != nil {
		return nil, err
	}
	return inv, nil
}

// CancelTx faturayı iptal eder: fatura silinmez, yevmiye kaydı ters kayıtla kapatılır, stok
// hareketleri pasifleştirilir. Mahsup edilmiş fatura iptal edilemez (önce tahsilat silinmeli).
// date ters kaydın tarihidir; kapatılmış bir döneme denk gelirse ledger.ErrPeriodClosed döner.
func CancelTx(ctx context.Context, tx pgx.Tx, businessID, userID, invoiceID string, date time.Time, touched stock.Set) error {
	var contactID string
	err := tx.QueryRow(ctx, `SELECT contact_id::text FROM invoices WHERE id = $1::uuid AND business_id = $2::uuid`,
		invoiceID, businessID).Scan(&contactID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// Kilit sırası: cari -> fatura -> ürünler. Tahsilat mahsubu da aynı sırayı izler.
	if err := LockContact(ctx, tx, businessID, contactID); err != nil {
		return err
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1::uuid FOR UPDATE`, invoiceID).Scan(&status); err != nil {
		return err
	}
	if status == "cancelled" {
		return ErrAlreadyCancelled
	}
	var paid bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM payment_allocations WHERE invoice_id = $1::uuid AND reversed_at IS NULL)`, invoiceID).Scan(&paid); err != nil {
		return err
	}
	if paid {
		return ErrHasPayments
	}

	pids, err := stock.ProductsOfInvoice(ctx, tx, businessID, invoiceID)
	if err != nil {
		return err
	}
	if _, err := stock.LockProducts(ctx, tx, businessID, pids); err != nil {
		return err
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
	if err := stock.DeactivateInvoice(ctx, tx, businessID, invoiceID); err != nil {
		return err
	}
	for _, id := range pids {
		touched.Add(id)
	}
	_, err = tx.Exec(ctx, `UPDATE invoices SET status = 'cancelled', cancelled_at = now() WHERE id = $1::uuid`, invoiceID)
	return err
}

const invoiceCols = `id::text, kind, series, year, number, contact_id::text, issue_date, due_date, status,
	prices_include_vat, general_discount, net_total, vat_total, gross_total, created_at,
	coalesce((SELECT c.name FROM contacts c WHERE c.id = invoices.contact_id), ''),
	coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.invoice_id = invoices.id AND a.reversed_at IS NULL), 0)::bigint`

type scanner interface{ Scan(dest ...any) error }

func scanInvoice(row scanner) (*Invoice, error) {
	var i Invoice
	var issue, due time.Time
	if err := row.Scan(&i.ID, &i.Kind, &i.Series, &i.Year, &i.Seq, &i.ContactID, &issue, &due, &i.Status,
		&i.PricesIncludeVAT, &i.GeneralDiscount, &i.NetTotal, &i.VATTotal, &i.GrossTotal, &i.CreatedAt, &i.ContactName, &i.Paid); err != nil {
		return nil, err
	}
	i.Number = FormatNumber(i.Series, i.Year, i.Seq)
	i.IssueDate, i.DueDate = issue.Format(dateLayout), due.Format(dateLayout)
	if i.Status == "issued" {
		i.Open = i.GrossTotal - i.Paid
	}
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
		`SELECT line_no, coalesce(product_id::text,''), description, quantity_milli, unit_price, vat_rate, line_discount,
		        allocated_discount, net_amount, vat_amount, gross_amount
		   FROM invoice_lines WHERE invoice_id = $1::uuid ORDER BY line_no`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l InvoiceLine
		if err := rows.Scan(&l.LineNo, &l.ProductID, &l.Description, &l.QuantityMilli, &l.UnitPrice, &l.VATRate, &l.Discount,
			&l.AllocatedDiscount, &l.Net, &l.VAT, &l.Gross); err != nil {
			return nil, err
		}
		inv.Lines = append(inv.Lines, l)
	}
	return inv, rows.Err()
}

// ListFilter fatura listesi süzgeçleridir; boş alanlar uygulanmaz.
type ListFilter struct{ Series, Kind, Status, ContactID string }

// List faturaları yeniden eskiye (oluşturulma zamanı, id) doğru, keyset sayfalamayla listeler.
// afterTS/afterID önceki sayfanın son kaydının oluşturulma zamanı (RFC3339) ve id'sidir.
func List(ctx context.Context, q ledger.Querier, businessID string, f ListFilter, afterTS, afterID string, limit int) ([]Invoice, error) {
	// ORDER BY'da sütunlar tablo adıyla nitelenir: niteleyici olmazsa `id`, SELECT'teki id::text çıktı sütununa bağlanır
	// ve sıralama keyset koşuluyla (uuid) tutarsız kalır.
	rows, err := q.Query(ctx,
		`SELECT `+invoiceCols+` FROM invoices
		  WHERE business_id = $1::uuid
		    AND (NULLIF($2,'') IS NULL OR (created_at, id) < (NULLIF($2,'')::timestamptz, NULLIF($3,'')::uuid))
		    AND ($4 = '' OR series = $4) AND ($5 = '' OR kind = $5) AND ($6 = '' OR status = $6)
		    AND (NULLIF($7,'') IS NULL OR contact_id = NULLIF($7,'')::uuid)
		  ORDER BY invoices.created_at DESC, invoices.id DESC LIMIT $8`,
		businessID, afterTS, afterID, f.Series, f.Kind, f.Status, f.ContactID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invoice{}
	for rows.Next() {
		i, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
