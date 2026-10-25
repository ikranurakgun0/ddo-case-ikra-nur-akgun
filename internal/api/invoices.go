package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"esnafdefter/internal/httpx"
	"esnafdefter/internal/invoicing"
	"esnafdefter/internal/ledger"
	"esnafdefter/internal/payments"
	"esnafdefter/internal/stock"
)

type invoiceLineReq struct {
	ProductID   string      `json:"product_id"` // isteğe bağlı; stok takipli ürünse stok hareketi üretir
	Description string      `json:"description"`
	Quantity    json.Number `json:"quantity"`   // ondalık, en fazla 3 basamak (ör. 2.5)
	UnitPrice   int64       `json:"unit_price"` // kuruş
	VATRate     int         `json:"vat_rate"`   // 1, 10 veya 20
	Discount    int64       `json:"discount"`   // satır iskontosu, kuruş
}

type invoiceReq struct {
	Kind             string           `json:"kind"`
	ContactID        string           `json:"contact_id"`
	Series           string           `json:"series"`
	IssueDate        string           `json:"issue_date"`
	DueDate          string           `json:"due_date"`
	PricesIncludeVAT bool             `json:"prices_include_vat"`
	GeneralDiscount  int64            `json:"general_discount"` // kuruş
	Lines            []invoiceLineReq `json:"lines"`
	CancelDate       string           `json:"cancel_date"` // yalnızca /correct için: eski faturanın ters kayıt tarihi
}

func parseDate(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", s)
	return t, err == nil
}

// handleCreateInvoice faturayı oluşturur. Idempotency-Key başlığı gönderilirse aynı anahtarla
// gelen tekrar istek yeni fatura üretmez, ilk yanıtı aynen döndürür.
func (s *Server) handleCreateInvoice(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	body, ok := httpx.ReadBody(w, r)
	if !ok {
		return
	}
	var req invoiceReq
	if !httpx.DecodeBytes(w, body, &req) {
		return
	}
	params, ok := s.invoiceParams(w, c.BusinessID, c.UserID, req)
	if !ok {
		return
	}
	s.runIdempotent(w, r, body, c.BusinessID, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return invoicing.CreateAndFinalize(ctx, tx, params)
	})
}

// invoiceParams istek gövdesini doğrulayıp servis parametrelerine çevirir.
func (s *Server) invoiceParams(w http.ResponseWriter, businessID, userID string, req invoiceReq) (invoicing.CreateParams, bool) {
	if !isUUID(req.ContactID) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "contact_id geçersiz")
		return invoicing.CreateParams{}, false
	}
	issue, ok1 := parseDate(req.IssueDate)
	due, ok2 := parseDate(req.DueDate)
	if !ok1 || !ok2 {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "tarihler YYYY-AA-GG biçiminde olmalı")
		return invoicing.CreateParams{}, false
	}
	lines := make([]invoicing.LineInput, 0, len(req.Lines))
	for _, l := range req.Lines {
		q, err := invoicing.ParseQuantityMilli(l.Quantity.String())
		if err != nil {
			s.writeDomainError(w, err)
			return invoicing.CreateParams{}, false
		}
		lines = append(lines, invoicing.LineInput{ProductID: l.ProductID, Description: l.Description, QuantityMilli: q,
			UnitPrice: l.UnitPrice, VATRate: l.VATRate, Discount: l.Discount})
	}
	return invoicing.CreateParams{
		BusinessID: businessID, UserID: userID, Kind: req.Kind, ContactID: req.ContactID, Series: req.Series,
		IssueDate: issue, DueDate: due, IncludesVAT: req.PricesIncludeVAT, GeneralDiscount: req.GeneralDiscount, Lines: lines,
	}, true
}

// handleCorrectInvoice bir ALIŞ faturasını düzeltir (eski iptal + yenisi), stok maliyetlerini yeniden hesaplar.
func (s *Server) handleCorrectInvoice(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "fatura bulunamadı")
		return
	}
	body, ok := httpx.ReadBody(w, r)
	if !ok {
		return
	}
	var req invoiceReq
	if !httpx.DecodeBytes(w, body, &req) {
		return
	}
	params, ok := s.invoiceParams(w, c.BusinessID, c.UserID, req)
	if !ok {
		return
	}
	cancelDate := time.Now().UTC().Truncate(24 * time.Hour)
	if req.CancelDate != "" {
		d, ok := parseDate(req.CancelDate)
		if !ok {
			httpx.WriteError(w, http.StatusBadRequest, "validation_error", "cancel_date YYYY-AA-GG biçiminde olmalı")
			return
		}
		cancelDate = d
	}
	s.runIdempotent(w, r, body, c.BusinessID, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return invoicing.CorrectAndFinalize(ctx, tx, id, params, cancelDate)
	})
}

// writeDomainError alan hatalarını uygun HTTP koduna çevirir.
func (s *Server) writeDomainError(w http.ResponseWriter, err error) {
	err = payments.MapError(err)
	var ve *invoicing.ValidationError
	var ie *stock.InsufficientError
	switch {
	case errors.As(err, &ve):
		status := http.StatusBadRequest
		if ve.Code == "contact_not_found" || ve.Code == "product_not_found" {
			status = http.StatusNotFound
		}
		httpx.WriteError(w, status, ve.Code, ve.Msg)
	case errors.As(err, &ie):
		httpx.WriteError(w, http.StatusConflict, "insufficient_stock", ie.Error())
	case errors.Is(err, ledger.ErrPeriodClosed):
		httpx.WriteError(w, http.StatusConflict, "period_closed", "bu tarih kapatılmış bir döneme denk geliyor")
	case errors.Is(err, invoicing.ErrNotFound), errors.Is(err, payments.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", "kayıt bulunamadı")
	case errors.Is(err, invoicing.ErrAlreadyCancelled), errors.Is(err, ledger.ErrAlreadyReversed):
		httpx.WriteError(w, http.StatusConflict, "already_cancelled", "fatura zaten iptal edilmiş")
	case errors.Is(err, invoicing.ErrHasPayments):
		httpx.WriteError(w, http.StatusConflict, "has_payments", "fatura mahsup edilmiş; önce ilgili tahsilat/ödemeyi silin")
	case errors.Is(err, payments.ErrAlreadyDeleted):
		httpx.WriteError(w, http.StatusConflict, "already_deleted", "ödeme zaten silinmiş")
	case errors.Is(err, payments.ErrNoAdvance):
		httpx.WriteError(w, http.StatusConflict, "no_advance", "mahsup edilecek avans yok")
	case errors.Is(err, payments.ErrAllocationLimit):
		httpx.WriteError(w, http.StatusConflict, "allocation_limit", "mahsup tutarı sınırı aşıldı")
	default:
		s.internalError(w, "istek", err)
	}
}

func (s *Server) handleGetInvoice(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "fatura bulunamadı")
		return
	}
	inv, err := invoicing.Get(r.Context(), s.pool, c.BusinessID, id)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, inv)
}

func (s *Server) handleListInvoices(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	limit, after, ok := parsePage(w, r)
	if !ok {
		return
	}
	items, err := invoicing.List(r.Context(), s.pool, c.BusinessID, after, r.URL.Query().Get("series"), limit+1)
	if err != nil {
		s.internalError(w, "fatura listesi", err)
		return
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_after": next})
}

type cancelReq struct {
	Date string `json:"date"` // ters kaydın tarihi; boşsa bugün
}

func (s *Server) handleCancelInvoice(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "fatura bulunamadı")
		return
	}
	body, ok := httpx.ReadBody(w, r)
	if !ok {
		return
	}
	var req cancelReq
	if len(bytes.TrimSpace(body)) > 0 && !httpx.DecodeBytes(w, body, &req) {
		return
	}
	date := time.Now().UTC().Truncate(24 * time.Hour)
	if req.Date != "" {
		d, ok := parseDate(req.Date)
		if !ok {
			httpx.WriteError(w, http.StatusBadRequest, "validation_error", "date YYYY-AA-GG biçiminde olmalı")
			return
		}
		date = d
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.internalError(w, "transaction", err)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if err := invoicing.CancelAndFinalize(r.Context(), tx, c.BusinessID, c.UserID, id, date); err != nil {
		s.writeDomainError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeDomainError(w, ledger.MapError(err))
		return
	}
	inv, err := invoicing.Get(r.Context(), s.pool, c.BusinessID, id)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, inv)
}

// handleInvoiceJournal faturaya ait yevmiye kayıtlarını (varsa ters kayıt dahil) satırlarıyla döner.
func (s *Server) handleInvoiceJournal(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "fatura bulunamadı")
		return
	}
	if _, err := invoicing.Get(r.Context(), s.pool, c.BusinessID, id); err != nil {
		s.writeDomainError(w, err)
		return
	}
	rows, err := s.pool.Query(r.Context(),
		`SELECT e.id::text, e.entry_date, e.description, e.source_type, a.code, l.debit, l.credit
		   FROM journal_entries e
		   JOIN journal_lines l ON l.entry_id = e.id
		   JOIN accounts a ON a.id = l.account_id
		  WHERE e.business_id = $1::uuid AND e.source_id = $2::uuid AND e.source_type IN ('invoice','reversal')
		  ORDER BY e.created_at, e.id, l.id`, c.BusinessID, id)
	if err != nil {
		s.internalError(w, "yevmiye", err)
		return
	}
	defer rows.Close()
	type jl struct {
		Account string `json:"account"`
		Debit   int64  `json:"debit"`
		Credit  int64  `json:"credit"`
	}
	type je struct {
		ID          string `json:"id"`
		Date        string `json:"date"`
		Description string `json:"description"`
		SourceType  string `json:"source_type"`
		Lines       []jl   `json:"lines"`
	}
	out := []*je{}
	idx := map[string]*je{}
	for rows.Next() {
		var eid, desc, st, code string
		var d time.Time
		var deb, cred int64
		if err := rows.Scan(&eid, &d, &desc, &st, &code, &deb, &cred); err != nil {
			s.internalError(w, "yevmiye", err)
			return
		}
		e, ok := idx[eid]
		if !ok {
			e = &je{ID: eid, Date: d.Format("2006-01-02"), Description: desc, SourceType: st}
			idx[eid] = e
			out = append(out, e)
		}
		e.Lines = append(e.Lines, jl{Account: code, Debit: deb, Credit: cred})
	}
	if err := rows.Err(); err != nil {
		s.internalError(w, "yevmiye", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"entries": out})
}
