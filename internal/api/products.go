package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"esnafdefter/internal/audit"
	"esnafdefter/internal/httpx"
	"esnafdefter/internal/invoicing"
	"esnafdefter/internal/stock"
)

type product struct {
	ID        string `json:"id"`
	SKU       string `json:"sku"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // product | service
	Unit      string `json:"unit"`
	VATRate   int    `json:"vat_rate"`
	SalePrice int64  `json:"sale_price"`
	Stock     *struct {
		QuantityMilli int64 `json:"quantity_milli"`
		Value         int64 `json:"value"`
		AvgUnitCost   int64 `json:"avg_unit_cost"`
	} `json:"stock,omitempty"`
}

type productReq struct {
	SKU       string `json:"sku"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Unit      string `json:"unit"`
	VATRate   int    `json:"vat_rate"`
	SalePrice int64  `json:"sale_price"`
}

func (s *Server) handleCreateProduct(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	var req productReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	req.SKU, req.Name = strings.TrimSpace(req.SKU), strings.TrimSpace(req.Name)
	if req.Unit == "" {
		req.Unit = "adet"
	}
	switch {
	case req.SKU == "" || len(req.SKU) > 64:
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "sku zorunlu (en fazla 64 karakter)")
		return
	case req.Name == "" || len(req.Name) > 200:
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "ad zorunlu (en fazla 200 karakter)")
		return
	case req.Kind != "product" && req.Kind != "service":
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "kind 'product' veya 'service' olmalı")
		return
	case !invoicing.ValidVATRate(req.VATRate):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_vat_rate", "KDV oranı %1, %10 veya %20 olmalı")
		return
	case req.SalePrice < 0 || req.SalePrice > 100_000_000_000:
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "satış fiyatı geçersiz")
		return
	}
	out := product{SKU: req.SKU, Name: req.Name, Kind: req.Kind, Unit: req.Unit, VATRate: req.VATRate, SalePrice: req.SalePrice}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.internalError(w, "ürün ekleme", err)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	err = tx.QueryRow(r.Context(),
		`INSERT INTO products(business_id, sku, name, kind, unit, vat_rate, sale_price)
		 VALUES ($1::uuid,$2,$3,$4,$5,$6,$7) RETURNING id::text`,
		c.BusinessID, req.SKU, req.Name, req.Kind, req.Unit, req.VATRate, req.SalePrice).Scan(&out.ID)
	if err == nil {
		err = audit.Write(r.Context(), tx, audit.Entry{BusinessID: c.BusinessID, ActorID: c.UserID, Action: "product_create",
			Entity: "product", EntityID: out.ID, New: map[string]any{"sku": req.SKU, "name": req.Name, "kind": req.Kind, "vat_rate": req.VATRate, "sale_price": req.SalePrice}})
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		httpx.WriteError(w, http.StatusConflict, "duplicate_sku", "bu sku zaten kullanılıyor")
		return
	}
	if err != nil {
		s.internalError(w, "ürün ekleme", err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
}

func (s *Server) handleGetProduct(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "ürün bulunamadı")
		return
	}
	var p product
	err := s.pool.QueryRow(r.Context(),
		`SELECT id::text, sku, name, kind, unit, vat_rate, sale_price FROM products WHERE id = $1::uuid AND business_id = $2::uuid`,
		id, c.BusinessID).Scan(&p.ID, &p.SKU, &p.Name, &p.Kind, &p.Unit, &p.VATRate, &p.SalePrice)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "ürün bulunamadı")
		return
	}
	if err != nil {
		s.internalError(w, "ürün okuma", err)
		return
	}
	if p.Kind == "product" {
		var q, v int64
		if err := s.pool.QueryRow(r.Context(),
			`SELECT coalesce(sum(CASE WHEN active THEN CASE direction WHEN 'in' THEN quantity_milli ELSE -quantity_milli END END),0)::bigint,
			        coalesce(sum(CASE WHEN active THEN CASE direction WHEN 'in' THEN value ELSE -value END END),0)::bigint
			   FROM stock_movements WHERE product_id = $1::uuid`, id).Scan(&q, &v); err != nil {
			s.internalError(w, "stok", err)
			return
		}
		p.Stock = &struct {
			QuantityMilli int64 `json:"quantity_milli"`
			Value         int64 `json:"value"`
			AvgUnitCost   int64 `json:"avg_unit_cost"`
		}{q, v, stock.AvgUnitCost(q, v)}
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

// handleListProducts ürünleri SKU'ya göre sıralı listeler. Süzgeçler: q (SKU/ad), kind (product|service).
func (s *Server) handleListProducts(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	limit, after, ok := parsePage(w, r)
	if !ok {
		return
	}
	afterSKU, afterID, ok := parseCursor(w, after)
	if !ok {
		return
	}
	kind := r.URL.Query().Get("kind")
	if !oneOf(kind, "product", "service") {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "kind geçersiz")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := s.pool.Query(r.Context(),
		`SELECT id::text, sku, name, kind, unit, vat_rate, sale_price FROM products
		  WHERE business_id = $1::uuid
		    AND ($2 = '' OR (sku, id) > ($2, NULLIF($3,'')::uuid))
		    AND ($4 = '' OR position(lower($4) in lower(sku)) > 0 OR position(lower($4) in lower(name)) > 0)
		    AND ($5 = '' OR kind = $5)
		  ORDER BY products.sku, products.id LIMIT $6`, c.BusinessID, afterSKU, afterID, q, kind, limit+1)
	if err != nil {
		s.internalError(w, "ürün listesi", err)
		return
	}
	defer rows.Close()
	items := []product{}
	for rows.Next() {
		var p product
		if err := rows.Scan(&p.ID, &p.SKU, &p.Name, &p.Kind, &p.Unit, &p.VATRate, &p.SalePrice); err != nil {
			s.internalError(w, "ürün listesi", err)
			return
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		s.internalError(w, "ürün listesi", err)
		return
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next = encodeCursor(last.SKU, last.ID)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_after": next})
}

func (s *Server) handleStockReport(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	rep, err := stock.Report(r.Context(), s.pool, c.BusinessID)
	if err != nil {
		s.internalError(w, "stok raporu", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rep)
}

func (s *Server) handleStockMovements(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			httpx.WriteError(w, http.StatusBadRequest, "validation_error", "limit 1-200 arasında olmalı")
			return
		}
		limit = n
	}
	var after int64
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			httpx.WriteError(w, http.StatusBadRequest, "validation_error", "after geçersiz")
			return
		}
		after = n
	}
	pid := r.URL.Query().Get("product_id")
	if pid != "" && !isUUID(pid) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "product_id geçersiz")
		return
	}
	items, err := stock.Movements(r.Context(), s.pool, c.BusinessID, pid, after, limit+1, r.URL.Query().Get("order") == "desc")
	if err != nil {
		s.internalError(w, "stok hareketleri", err)
		return
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_after": next})
}

type settingsReq struct {
	AllowNegativeStock bool `json:"allow_negative_stock"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	var st settingsReq
	if err := s.pool.QueryRow(r.Context(), `SELECT allow_negative_stock FROM business_settings WHERE business_id = $1::uuid`, c.BusinessID).Scan(&st.AllowNegativeStock); err != nil {
		s.internalError(w, "ayarlar", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	var req settingsReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.internalError(w, "ayarlar", err)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	var old bool
	if err := tx.QueryRow(r.Context(), `SELECT allow_negative_stock FROM business_settings WHERE business_id = $1::uuid FOR UPDATE`, c.BusinessID).Scan(&old); err != nil {
		s.internalError(w, "ayarlar", err)
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE business_settings SET allow_negative_stock = $2 WHERE business_id = $1::uuid`, c.BusinessID, req.AllowNegativeStock); err != nil {
		s.internalError(w, "ayarlar", err)
		return
	}
	if _, err := tx.Exec(r.Context(),
		`INSERT INTO audit_log(business_id, actor_id, action, entity, entity_id, old_value, new_value)
		 VALUES ($1::uuid, $2::uuid, 'settings_update', 'business_settings', $1,
		         jsonb_build_object('allow_negative_stock', $3::boolean), jsonb_build_object('allow_negative_stock', $4::boolean))`,
		c.BusinessID, c.UserID, old, req.AllowNegativeStock); err != nil {
		s.internalError(w, "ayarlar", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.internalError(w, "ayarlar", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, req)
}
