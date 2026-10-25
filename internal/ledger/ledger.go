// Package ledger çift kayıt yevmiye motorudur.
//
// Kuralların asıl güvencesi veritabanındadır (migrations/0002_accounting.sql):
// borç=alacak (deferred constraint trigger), değiştirilemezlik, dönem kilidi.
// Bu paket onları kullanır ve veritabanı hatalarını anlamlı hatalara çevirir.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrUnbalanced      = errors.New("yevmiye kaydı dengesiz")
	ErrInvalidEntry    = errors.New("geçersiz yevmiye kaydı")
	ErrPeriodClosed    = errors.New("dönem kapatılmış")
	ErrUnknownAccount  = errors.New("hesap bulunamadı")
	ErrAlreadyReversed = errors.New("kayıt zaten ters çevrilmiş")
	ErrNotFound        = errors.New("kayıt bulunamadı")
	ErrNotAdvancing    = errors.New("kapanış tarihi mevcut kapanıştan ileri olmalı")
)

// Querier hem havuz hem transaction tarafından sağlanır.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Line tek bir borç ya da alacak satırıdır (kuruş cinsinden, tam sayı).
type Line struct {
	Account   string // hesap kodu, ör. "120"
	Debit     int64
	Credit    int64
	ContactID string // isteğe bağlı cari
}

type Entry struct {
	BusinessID  string
	Date        time.Time
	Description string
	SourceType  string // invoice | payment | reversal | manual | stock
	SourceID    string
	CreatedBy   string
	Lines       []Line
}

// MapError veritabanının özel hata kodlarını (DD001..) paket hatalarına çevirir.
func MapError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "DD001":
			return fmt.Errorf("%w: %s", ErrPeriodClosed, pe.Message)
		case "DD002":
			return fmt.Errorf("%w: %s", ErrUnbalanced, pe.Message)
		case "DD003":
			return fmt.Errorf("%w: %s", ErrNotAdvancing, pe.Message)
		case "23505":
			if pe.ConstraintName == "journal_entries_reverses_id_key" {
				return ErrAlreadyReversed
			}
		}
	}
	return err
}

func validate(lines []Line) error {
	if len(lines) < 2 {
		return fmt.Errorf("%w: en az iki satır gerekir", ErrInvalidEntry)
	}
	var d, c int64
	for i, l := range lines {
		if l.Account == "" {
			return fmt.Errorf("%w: satır %d hesapsız", ErrInvalidEntry, i+1)
		}
		if l.Debit < 0 || l.Credit < 0 || (l.Debit > 0) == (l.Credit > 0) {
			return fmt.Errorf("%w: satır %d tam olarak bir tarafta pozitif tutar içermeli", ErrInvalidEntry, i+1)
		}
		d += l.Debit
		c += l.Credit
	}
	if d != c {
		return fmt.Errorf("%w: borç=%d alacak=%d", ErrUnbalanced, d, c)
	}
	return nil
}

// Post kaydı verilen transaction içinde yazar. Çağıran commit eder; böylece fatura ve
// yevmiye kaydı ya birlikte yazılır ya da hiç yazılmaz.
func Post(ctx context.Context, tx pgx.Tx, e Entry) (string, error) {
	if err := validate(e.Lines); err != nil {
		return "", err
	}
	codes := make([]string, 0, len(e.Lines))
	seen := map[string]bool{}
	for _, l := range e.Lines {
		if !seen[l.Account] {
			seen[l.Account] = true
			codes = append(codes, l.Account)
		}
	}
	rows, err := tx.Query(ctx, `SELECT code, id::text FROM accounts WHERE business_id = $1::uuid AND code = ANY($2)`, e.BusinessID, codes)
	if err != nil {
		return "", err
	}
	ids := map[string]string{}
	for rows.Next() {
		var code, id string
		if err := rows.Scan(&code, &id); err != nil {
			rows.Close()
			return "", err
		}
		ids[code] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	for _, c := range codes {
		if _, ok := ids[c]; !ok {
			return "", fmt.Errorf("%w: %s", ErrUnknownAccount, c)
		}
	}

	var entryID string
	err = tx.QueryRow(ctx,
		`INSERT INTO journal_entries(business_id, entry_date, description, source_type, source_id, created_by)
		 VALUES ($1::uuid, $2, $3, $4, NULLIF($5,'')::uuid, NULLIF($6,'')::uuid) RETURNING id::text`,
		e.BusinessID, e.Date, e.Description, e.SourceType, e.SourceID, e.CreatedBy).Scan(&entryID)
	if err != nil {
		return "", MapError(err)
	}
	for _, l := range e.Lines {
		if _, err := tx.Exec(ctx,
			`INSERT INTO journal_lines(entry_id, business_id, account_id, contact_id, debit, credit)
			 VALUES ($1::uuid, $2::uuid, $3::uuid, NULLIF($4,'')::uuid, $5, $6)`,
			entryID, e.BusinessID, ids[l.Account], l.ContactID, l.Debit, l.Credit); err != nil {
			return "", MapError(err)
		}
	}
	return entryID, nil
}

// Reverse bir kaydı silmeden, borç/alacak yönlerini çevirerek ters kayıt atar.
// date kapatılmış bir döneme düşüyorsa ErrPeriodClosed döner.
func Reverse(ctx context.Context, tx pgx.Tx, businessID, entryID string, date time.Time, createdBy string) (string, error) {
	var desc, sourceID string
	err := tx.QueryRow(ctx,
		`SELECT description, coalesce(source_id::text,'') FROM journal_entries
		  WHERE id = $1::uuid AND business_id = $2::uuid AND reverses_id IS NULL`, entryID, businessID).Scan(&desc, &sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx,
		`SELECT a.code, l.debit, l.credit, coalesce(l.contact_id::text,'')
		   FROM journal_lines l JOIN accounts a ON a.id = l.account_id
		  WHERE l.entry_id = $1::uuid ORDER BY l.id`, entryID)
	if err != nil {
		return "", err
	}
	var lines []Line
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.Account, &l.Debit, &l.Credit, &l.ContactID); err != nil {
			rows.Close()
			return "", err
		}
		l.Debit, l.Credit = l.Credit, l.Debit // yönleri çevir
		lines = append(lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}

	var revID string
	err = tx.QueryRow(ctx,
		`INSERT INTO journal_entries(business_id, entry_date, description, source_type, source_id, reverses_id, created_by)
		 VALUES ($1::uuid, $2, $3, 'reversal', NULLIF($4,'')::uuid, $5::uuid, NULLIF($6,'')::uuid) RETURNING id::text`,
		businessID, date, "İPTAL: "+desc, sourceID, entryID, createdBy).Scan(&revID)
	if err != nil {
		return "", MapError(err)
	}
	for _, l := range lines {
		accID := ""
		if err := tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE business_id=$1::uuid AND code=$2`, businessID, l.Account).Scan(&accID); err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO journal_lines(entry_id, business_id, account_id, contact_id, debit, credit)
			 VALUES ($1::uuid, $2::uuid, $3::uuid, NULLIF($4,'')::uuid, $5, $6)`,
			revID, businessID, accID, l.ContactID, l.Debit, l.Credit); err != nil {
			return "", MapError(err)
		}
	}
	return revID, nil
}

// ClosePeriod dönemi `through` tarihine (dahil) kadar kilitler. Yalnızca ileri gidebilir.
func ClosePeriod(ctx context.Context, q interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, businessID string, through time.Time) error {
	tag, err := q.Exec(ctx,
		`UPDATE period_locks SET closed_through = $2, updated_at = now()
		  WHERE business_id = $1::uuid AND closed_through < $2`, businessID, through)
	if err != nil {
		return MapError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotAdvancing
	}
	return nil
}

// ClosedThrough kapatılmış son günü döner; hiç kapatılmamışsa nil.
func ClosedThrough(ctx context.Context, q Querier, businessID string) (*time.Time, error) {
	var d time.Time
	if err := q.QueryRow(ctx, `SELECT closed_through FROM period_locks WHERE business_id = $1::uuid`, businessID).Scan(&d); err != nil {
		return nil, err
	}
	if d.Year() <= 1900 {
		return nil, nil
	}
	return &d, nil
}

type AccountBalance struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Debit   int64  `json:"debit"`
	Credit  int64  `json:"credit"`
	Balance int64  `json:"balance"` // borç - alacak
}

type TrialBalance struct {
	AsOf        string           `json:"as_of"`
	Accounts    []AccountBalance `json:"accounts"`
	TotalDebit  int64            `json:"total_debit"`
	TotalCredit int64            `json:"total_credit"`
	Balanced    bool             `json:"balanced"`
}

// Trial mizanı üretir (asOf dahil o güne kadar). Hareketsiz hesaplar da listelenir.
func Trial(ctx context.Context, q Querier, businessID string, asOf time.Time) (*TrialBalance, error) {
	rows, err := q.Query(ctx,
		`SELECT a.code, a.name, a.type, coalesce(sum(l.debit),0)::bigint, coalesce(sum(l.credit),0)::bigint
		   FROM accounts a
		   LEFT JOIN (journal_lines l JOIN journal_entries e ON e.id = l.entry_id AND e.entry_date <= $2)
		          ON l.account_id = a.id
		  WHERE a.business_id = $1::uuid
		  GROUP BY a.id, a.code, a.name, a.type
		  ORDER BY a.code`, businessID, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tb := &TrialBalance{AsOf: asOf.Format("2006-01-02"), Accounts: []AccountBalance{}}
	for rows.Next() {
		var a AccountBalance
		if err := rows.Scan(&a.Code, &a.Name, &a.Type, &a.Debit, &a.Credit); err != nil {
			return nil, err
		}
		a.Balance = a.Debit - a.Credit
		tb.TotalDebit += a.Debit
		tb.TotalCredit += a.Credit
		tb.Accounts = append(tb.Accounts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tb.Balanced = tb.TotalDebit == tb.TotalCredit
	return tb, nil
}
