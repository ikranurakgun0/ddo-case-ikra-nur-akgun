// Package audit denetim kaydını (audit_log) yazar.
//
// Kritik işlemler (kim, ne zaman, neyi, eski/yeni değer) EKLEMELİ-YALNIZ audit_log tablosuna yazılır
// (tabloda UPDATE/DELETE/TRUNCATE tetikleyiciyle engellidir). Kayıt, işin kendisiyle AYNI transaction'da
// yazılır: işlem geri alınırsa kayıt da kalmaz; işlem başarılıysa kayıt mutlaka vardır.
package audit

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgconn"
)

// Execer hem havuz hem transaction tarafından sağlanır.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type Entry struct {
	BusinessID string
	ActorID    string // işlemi yapan kullanıcı; sistem işlemlerinde boş olabilir
	Action     string // örn. invoice_create, invoice_cancel, payment_delete, period_close
	Entity     string // örn. invoice, payment, period
	EntityID   string
	Old, New   any // JSON'a çevrilebilir; nil ise NULL yazılır
}

func marshal(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

// Write kaydı verilen transaction (ya da havuz) üzerinden yazar.
func Write(ctx context.Context, q Execer, e Entry) error {
	oldJSON, err := marshal(e.Old)
	if err != nil {
		return err
	}
	newJSON, err := marshal(e.New)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx,
		`INSERT INTO audit_log(business_id, actor_id, action, entity, entity_id, old_value, new_value)
		 VALUES ($1::uuid, NULLIF($2,'')::uuid, $3, $4, $5, $6::jsonb, $7::jsonb)`,
		e.BusinessID, e.ActorID, e.Action, e.Entity, e.EntityID, oldJSON, newJSON)
	return err
}
