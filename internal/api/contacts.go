package api

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"esnafdefter/internal/audit"
	"esnafdefter/internal/httpx"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidRe.MatchString(s) }

type contact struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	TaxNo string `json:"tax_no,omitempty"`
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

type contactReq struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	TaxNo string `json:"tax_no"`
	Email string `json:"email"`
	Phone string `json:"phone"`
}

func (s *Server) handleCreateContact(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	var req contactReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 200 {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "ad zorunlu (en fazla 200 karakter)")
		return
	}
	if req.Kind != "customer" && req.Kind != "supplier" && req.Kind != "both" {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "kind 'customer', 'supplier' veya 'both' olmalı")
		return
	}
	out := contact{Kind: req.Kind, Name: req.Name, TaxNo: req.TaxNo, Email: req.Email, Phone: req.Phone}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.internalError(w, "cari ekleme", err)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	err = tx.QueryRow(r.Context(),
		`INSERT INTO contacts(business_id, kind, name, tax_no, email, phone)
		 VALUES ($1::uuid,$2,$3,NULLIF($4,''),NULLIF($5,''),NULLIF($6,'')) RETURNING id::text`,
		c.BusinessID, req.Kind, req.Name, req.TaxNo, req.Email, req.Phone).Scan(&out.ID)
	if err == nil {
		err = audit.Write(r.Context(), tx, audit.Entry{BusinessID: c.BusinessID, ActorID: c.UserID, Action: "contact_create",
			Entity: "contact", EntityID: out.ID, New: map[string]any{"kind": req.Kind, "name": req.Name, "tax_no": req.TaxNo}})
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.internalError(w, "cari ekleme", err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
}

func (s *Server) handleGetContact(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "cari bulunamadı")
		return
	}
	var out contact
	err := s.pool.QueryRow(r.Context(),
		`SELECT id::text, kind, name, coalesce(tax_no,''), coalesce(email,''), coalesce(phone,'')
		   FROM contacts WHERE id = $1::uuid AND business_id = $2::uuid`, id, c.BusinessID).
		Scan(&out.ID, &out.Kind, &out.Name, &out.TaxNo, &out.Email, &out.Phone)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "cari bulunamadı")
		return
	}
	if err != nil {
		s.internalError(w, "cari okuma", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// parsePage limit (varsayılan 50, en fazla 200) ve after imlecini okur.
func parsePage(w http.ResponseWriter, r *http.Request) (limit int, after string, ok bool) {
	limit = 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			httpx.WriteError(w, http.StatusBadRequest, "validation_error", "limit 1-200 arasında olmalı")
			return 0, "", false
		}
		limit = n
	}
	after = r.URL.Query().Get("after")
	return limit, after, true
}

// handleListContacts carileri ada göre sıralı, keyset sayfalamayla listeler.
// Süzgeçler: q (ad içinde arama), kind (customer: müşteri+ikisi, supplier: tedarikçi+ikisi).
func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	limit, after, ok := parsePage(w, r)
	if !ok {
		return
	}
	afterName, afterID, ok := parseCursor(w, after)
	if !ok {
		return
	}
	kind := r.URL.Query().Get("kind")
	if !oneOf(kind, "customer", "supplier", "both") {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "kind geçersiz")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := s.pool.Query(r.Context(),
		`SELECT id::text, kind, name, coalesce(tax_no,''), coalesce(email,''), coalesce(phone,'')
		   FROM contacts
		  WHERE business_id = $1::uuid
		    AND ($2 = '' OR (name, id) > ($2, NULLIF($3,'')::uuid))
		    AND ($4 = '' OR position(lower($4) in lower(name)) > 0)
		    AND ($5 = '' OR kind = $5 OR (kind = 'both' AND $5 IN ('customer','supplier')))
		  ORDER BY contacts.name, contacts.id LIMIT $6`, c.BusinessID, afterName, afterID, q, kind, limit+1)
	if err != nil {
		s.internalError(w, "cari listesi", err)
		return
	}
	defer rows.Close()
	items := []contact{}
	for rows.Next() {
		var x contact
		if err := rows.Scan(&x.ID, &x.Kind, &x.Name, &x.TaxNo, &x.Email, &x.Phone); err != nil {
			s.internalError(w, "cari listesi", err)
			return
		}
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		s.internalError(w, "cari listesi", err)
		return
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next = encodeCursor(last.Name, last.ID)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_after": next})
}
