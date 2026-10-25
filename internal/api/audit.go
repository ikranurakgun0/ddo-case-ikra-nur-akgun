package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"esnafdefter/internal/httpx"
)

type auditItem struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	ActorID    string          `json:"actor_id,omitempty"`
	ActorEmail string          `json:"actor_email,omitempty"`
	Action     string          `json:"action"`
	Entity     string          `json:"entity"`
	EntityID   string          `json:"entity_id"`
	OldValue   json.RawMessage `json:"old_value,omitempty"`
	NewValue   json.RawMessage `json:"new_value,omitempty"`
}

// handleAuditLog denetim kaydını yeniden eskiye listeler (kim, ne zaman, neyi, eski/yeni değer).
// Süzgeçler: entity, entity_id, action. Sayfalama: after = son görülen kayıt id'si.
func (s *Server) handleAuditLog(w http.ResponseWriter, r *http.Request) {
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
	var after int64 = 1 << 62
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			httpx.WriteError(w, http.StatusBadRequest, "validation_error", "after geçersiz")
			return
		}
		after = n
	}
	q := r.URL.Query()
	rows, err := s.pool.Query(r.Context(),
		`SELECT a.id, a.at, coalesce(a.actor_id::text,''), coalesce(u.email,''), a.action, a.entity, a.entity_id,
		        a.old_value::text, a.new_value::text
		   FROM audit_log a LEFT JOIN users u ON u.id = a.actor_id
		  WHERE a.business_id = $1::uuid AND a.id < $2
		    AND ($3 = '' OR a.entity = $3) AND ($4 = '' OR a.entity_id = $4) AND ($5 = '' OR a.action = $5)
		  ORDER BY a.id DESC LIMIT $6`,
		c.BusinessID, after, q.Get("entity"), q.Get("entity_id"), q.Get("action"), limit+1)
	if err != nil {
		s.internalError(w, "denetim kaydı", err)
		return
	}
	defer rows.Close()
	items := []auditItem{}
	for rows.Next() {
		var it auditItem
		var oldV, newV *string
		if err := rows.Scan(&it.ID, &it.At, &it.ActorID, &it.ActorEmail, &it.Action, &it.Entity, &it.EntityID, &oldV, &newV); err != nil {
			s.internalError(w, "denetim kaydı", err)
			return
		}
		if oldV != nil {
			it.OldValue = json.RawMessage(*oldV)
		}
		if newV != nil {
			it.NewValue = json.RawMessage(*newV)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		s.internalError(w, "denetim kaydı", err)
		return
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		next = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_after": next})
}
