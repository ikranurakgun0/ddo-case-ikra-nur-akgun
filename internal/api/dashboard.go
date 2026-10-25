package api

import (
	"net/http"
	"time"

	"esnafdefter/internal/httpx"
	"esnafdefter/internal/invoicing"
	"esnafdefter/internal/payments"
)

// handleDashboard pano için özet göstergeleri ve son hareketleri tek istekte döner.
//
// Göstergeler (kuruş): açık alacak/borç, kasa+banka, stok değeri (153 hesabı = stok değeri değişmezi),
// bu ayın net satışı ve ödenecek KDV'si; ayrıca fatura/ödeme/cari sayıları ve son 8 hareket.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	ctx := r.Context()
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, -1)

	var out struct {
		Receivable  int64 `json:"receivable"`
		Payable     int64 `json:"payable"`
		Cash        int64 `json:"cash"`
		StockValue  int64 `json:"stock_value"`
		MonthSales  int64 `json:"month_sales"`
		MonthVATDue int64 `json:"month_vat_payable"`
		Counts      struct {
			Invoices int64 `json:"invoices"`
			Payments int64 `json:"payments"`
			Contacts int64 `json:"contacts"`
			Products int64 `json:"products"`
		} `json:"counts"`
		RecentInvoices []invoicing.Invoice `json:"recent_invoices"`
		RecentPayments []payments.Payment  `json:"recent_payments"`
		GeneratedAt    time.Time           `json:"generated_at"`
	}
	open := func(kind string) (int64, error) {
		var v int64
		err := s.pool.QueryRow(ctx,
			`SELECT coalesce(sum(i.gross_total),0)::bigint
			        - coalesce((SELECT sum(a.amount) FROM payment_allocations a JOIN invoices i2 ON i2.id = a.invoice_id
			                     WHERE a.business_id = $1::uuid AND a.reversed_at IS NULL AND i2.kind = $2 AND i2.status = 'issued'),0)::bigint
			   FROM invoices i WHERE i.business_id = $1::uuid AND i.kind = $2 AND i.status = 'issued'`, c.BusinessID, kind).Scan(&v)
		return v, err
	}
	var err error
	if out.Receivable, err = open("sale"); err != nil {
		s.internalError(w, "pano", err)
		return
	}
	if out.Payable, err = open("purchase"); err != nil {
		s.internalError(w, "pano", err)
		return
	}
	if err = s.pool.QueryRow(ctx,
		`SELECT coalesce(sum(CASE WHEN a.code IN ('100','102') THEN l.debit - l.credit END),0)::bigint,
		        coalesce(sum(CASE WHEN a.code = '153' THEN l.debit - l.credit END),0)::bigint
		   FROM journal_lines l JOIN accounts a ON a.id = l.account_id
		  WHERE l.business_id = $1::uuid AND a.code IN ('100','102','153')`, c.BusinessID).Scan(&out.Cash, &out.StockValue); err != nil {
		s.internalError(w, "pano", err)
		return
	}
	if err = s.pool.QueryRow(ctx,
		`SELECT coalesce(sum(CASE WHEN i.kind = 'sale' THEN l.net_amount END),0)::bigint,
		        (coalesce(sum(CASE WHEN i.kind = 'sale' THEN l.vat_amount END),0)
		         - coalesce(sum(CASE WHEN i.kind = 'purchase' THEN l.vat_amount END),0))::bigint
		   FROM invoice_lines l JOIN invoices i ON i.id = l.invoice_id
		  WHERE i.business_id = $1::uuid AND i.status = 'issued' AND i.issue_date BETWEEN $2 AND $3`,
		c.BusinessID, monthStart, monthEnd).Scan(&out.MonthSales, &out.MonthVATDue); err != nil {
		s.internalError(w, "pano", err)
		return
	}
	if err = s.pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM invoices WHERE business_id = $1::uuid),
		        (SELECT count(*) FROM payments WHERE business_id = $1::uuid),
		        (SELECT count(*) FROM contacts WHERE business_id = $1::uuid),
		        (SELECT count(*) FROM products WHERE business_id = $1::uuid)`, c.BusinessID).
		Scan(&out.Counts.Invoices, &out.Counts.Payments, &out.Counts.Contacts, &out.Counts.Products); err != nil {
		s.internalError(w, "pano", err)
		return
	}
	if out.RecentInvoices, err = invoicing.List(ctx, s.pool, c.BusinessID, invoicing.ListFilter{}, "", "", 8); err != nil {
		s.internalError(w, "pano", err)
		return
	}
	if out.RecentPayments, err = payments.List(ctx, s.pool, c.BusinessID, payments.ListFilter{}, "", "", 8); err != nil {
		s.internalError(w, "pano", err)
		return
	}
	out.GeneratedAt = now
	httpx.WriteJSON(w, http.StatusOK, out)
}
