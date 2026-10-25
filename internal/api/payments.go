package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"esnafdefter/internal/httpx"
	"esnafdefter/internal/payments"
)

type paymentReq struct {
	Kind      string `json:"kind"` // receipt | disbursement
	ContactID string `json:"contact_id"`
	Method    string `json:"method"` // cash | bank
	Date      string `json:"date"`
	Amount    int64  `json:"amount"` // kuruş
}

func (s *Server) handleCreatePayment(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	body, ok := httpx.ReadBody(w, r)
	if !ok {
		return
	}
	var req paymentReq
	if !httpx.DecodeBytes(w, body, &req) {
		return
	}
	if !isUUID(req.ContactID) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "contact_id geçersiz")
		return
	}
	date, ok := parseDate(req.Date)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "date YYYY-AA-GG biçiminde olmalı")
		return
	}
	s.runIdempotent(w, r, body, c.BusinessID, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return payments.CreateTx(ctx, tx, payments.CreateParams{
			BusinessID: c.BusinessID, UserID: c.UserID, Kind: req.Kind, ContactID: req.ContactID,
			Method: req.Method, Date: date, Amount: req.Amount,
		})
	})
}

func (s *Server) handleGetPayment(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "ödeme bulunamadı")
		return
	}
	p, err := payments.Get(r.Context(), s.pool, c.BusinessID, id)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

// handleListPayments ödemeleri yeniden eskiye listeler. Süzgeçler: kind, contact_id.
func (s *Server) handleListPayments(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	limit, after, ok := parsePage(w, r)
	if !ok {
		return
	}
	afterTS, afterID, ok := parseCursor(w, after)
	if !ok {
		return
	}
	f := payments.ListFilter{Kind: r.URL.Query().Get("kind"), ContactID: r.URL.Query().Get("contact_id")}
	if !oneOf(f.Kind, "receipt", "disbursement") || (f.ContactID != "" && !isUUID(f.ContactID)) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "süzgeç değeri geçersiz")
		return
	}
	items, err := payments.List(r.Context(), s.pool, c.BusinessID, f, afterTS, afterID, limit+1)
	if err != nil {
		s.internalError(w, "ödeme listesi", err)
		return
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next = timeCursor(last.CreatedAt, last.ID)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_after": next})
}

// reversalDate ters kayıt tarihini ?date= ile ya da gövdeden alır; yoksa bugün.
func reversalDate(w http.ResponseWriter, v string) (time.Time, bool) {
	if v == "" {
		return time.Now().UTC().Truncate(24 * time.Hour), true
	}
	d, ok := parseDate(v)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "date YYYY-AA-GG biçiminde olmalı")
	}
	return d, ok
}

func (s *Server) handleDeletePayment(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "ödeme bulunamadı")
		return
	}
	date, ok := reversalDate(w, r.URL.Query().Get("date"))
	if !ok {
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.internalError(w, "transaction", err)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if err := payments.DeleteTx(r.Context(), tx, c.BusinessID, c.UserID, id, date); err != nil {
		s.writeDomainError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeDomainError(w, err)
		return
	}
	p, err := payments.Get(r.Context(), s.pool, c.BusinessID, id)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (s *Server) handleApplyAdvance(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "ödeme bulunamadı")
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
	date, ok := reversalDate(w, req.Date)
	if !ok {
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.internalError(w, "transaction", err)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	p, err := payments.ApplyAdvanceTx(r.Context(), tx, c.BusinessID, c.UserID, id, date)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeDomainError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (s *Server) handleAging(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "receivable"
	}
	if kind != "receivable" && kind != "payable" {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "kind 'receivable' veya 'payable' olmalı")
		return
	}
	asOf := time.Now().UTC().Truncate(24 * time.Hour)
	if v := r.URL.Query().Get("as_of"); v != "" {
		d, ok := parseDate(v)
		if !ok {
			httpx.WriteError(w, http.StatusBadRequest, "validation_error", "as_of YYYY-AA-GG biçiminde olmalı")
			return
		}
		asOf = d
	}
	rep, err := payments.Aging(r.Context(), s.pool, c.BusinessID, kind, asOf, r.URL.Query().Get("detail") == "1")
	if err != nil {
		s.internalError(w, "yaşlandırma", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rep)
}

func (s *Server) statement(w http.ResponseWriter, r *http.Request, businessID, contactID, kind string) {
	st, err := payments.ContactStatement(r.Context(), s.pool, businessID, contactID, kind)
	if errors.Is(err, payments.ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "cari bulunamadı")
		return
	}
	if err != nil {
		s.internalError(w, "ekstre", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Server) handleContactStatement(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "cari bulunamadı")
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "receivable"
	}
	if kind != "receivable" && kind != "payable" {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "kind 'receivable' veya 'payable' olmalı")
		return
	}
	s.statement(w, r, c.BusinessID, id, kind)
}

// handleMyStatement müşteri rolü yalnızca kendi carisinin ekstresini (salt okunur) görür.
func (s *Server) handleMyStatement(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	var contactID *string
	if err := s.pool.QueryRow(r.Context(), `SELECT contact_id::text FROM users WHERE id = $1::uuid`, c.UserID).Scan(&contactID); err != nil {
		s.internalError(w, "ekstre", err)
		return
	}
	if contactID == nil {
		httpx.WriteError(w, http.StatusNotFound, "no_contact", "hesabınız bir cariye bağlı değil")
		return
	}
	s.statement(w, r, c.BusinessID, *contactID, "receivable")
}

type vatLine struct {
	Rate int   `json:"rate"`
	Net  int64 `json:"net"`
	VAT  int64 `json:"vat"`
}

func (s *Server) handleVATSummary(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	from, ok1 := parseDate(r.URL.Query().Get("from"))
	to, ok2 := parseDate(r.URL.Query().Get("to"))
	if !ok1 || !ok2 || to.Before(from) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "from ve to YYYY-AA-GG biçiminde olmalı (to >= from)")
		return
	}
	rows, err := s.pool.Query(r.Context(),
		`SELECT i.kind, l.vat_rate, sum(l.net_amount)::bigint, sum(l.vat_amount)::bigint
		   FROM invoice_lines l JOIN invoices i ON i.id = l.invoice_id
		  WHERE i.business_id = $1::uuid AND i.status = 'issued' AND i.issue_date BETWEEN $2 AND $3
		  GROUP BY i.kind, l.vat_rate ORDER BY i.kind, l.vat_rate`, c.BusinessID, from, to)
	if err != nil {
		s.internalError(w, "kdv özeti", err)
		return
	}
	defer rows.Close()
	sales, purchases := []vatLine{}, []vatLine{}
	var out, in int64
	for rows.Next() {
		var kind string
		var l vatLine
		if err := rows.Scan(&kind, &l.Rate, &l.Net, &l.VAT); err != nil {
			s.internalError(w, "kdv özeti", err)
			return
		}
		if kind == "sale" {
			sales = append(sales, l)
			out += l.VAT
		} else {
			purchases = append(purchases, l)
			in += l.VAT
		}
	}
	if err := rows.Err(); err != nil {
		s.internalError(w, "kdv özeti", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"from": r.URL.Query().Get("from"), "to": r.URL.Query().Get("to"),
		"sales": sales, "purchases": purchases,
		"output_vat": out, "input_vat": in, "payable": out - in, // hesaplanan - indirilecek
	})
}
