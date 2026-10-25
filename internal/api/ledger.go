package api

import (
	"errors"
	"net/http"
	"time"

	"esnafdefter/internal/httpx"
	"esnafdefter/internal/ledger"
)

type closeReq struct {
	Through string `json:"through"` // bu tarihe kadar (dahil) kapat
}

func (s *Server) handleClosePeriod(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	var req closeReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	through, ok := parseDate(req.Through)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "through YYYY-AA-GG biçiminde olmalı")
		return
	}
	if through.After(time.Now().UTC()) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "gelecekteki bir tarihe kadar dönem kapatılamaz")
		return
	}
	if err := ledger.ClosePeriod(r.Context(), s.pool, c.BusinessID, through); err != nil {
		if errors.Is(err, ledger.ErrNotAdvancing) {
			httpx.WriteError(w, http.StatusConflict, "period_not_advancing", "kapanış tarihi mevcut kapanıştan ileri olmalı; kapatılmış dönem yeniden açılamaz")
			return
		}
		s.internalError(w, "dönem kapatma", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"closed_through": req.Through})
}

func (s *Server) handleGetPeriods(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	d, err := ledger.ClosedThrough(r.Context(), s.pool, c.BusinessID)
	if err != nil {
		s.internalError(w, "dönem", err)
		return
	}
	var v any
	if d != nil {
		v = d.Format("2006-01-02")
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"closed_through": v})
}

func (s *Server) handleTrialBalance(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	asOf := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	if v := r.URL.Query().Get("as_of"); v != "" {
		d, ok := parseDate(v)
		if !ok {
			httpx.WriteError(w, http.StatusBadRequest, "validation_error", "as_of YYYY-AA-GG biçiminde olmalı")
			return
		}
		asOf = d
	}
	tb, err := ledger.Trial(r.Context(), s.pool, c.BusinessID, asOf)
	if err != nil {
		s.internalError(w, "mizan", err)
		return
	}
	if r.URL.Query().Get("as_of") == "" {
		tb.AsOf = ""
	}
	httpx.WriteJSON(w, http.StatusOK, tb)
}

func (s *Server) internalError(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	httpx.WriteError(w, http.StatusInternalServerError, "internal", "beklenmeyen bir hata oluştu")
}
