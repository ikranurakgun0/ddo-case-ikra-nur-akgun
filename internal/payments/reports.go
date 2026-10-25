package payments

import (
	"context"
	"sort"
	"time"

	"esnafdefter/internal/invoicing"
	"esnafdefter/internal/ledger"
)

var BucketNames = []string{"0-30", "31-60", "61-90", "90+"}

// Bucket vadesi geçmiş gün sayısına göre kovayı belirler.
// Gün = rapor tarihi − vade tarihi. Vadesi henüz gelmemiş (gün <= 0) ve 30 güne kadar gecikmiş
// alacaklar "0-30" kovasındadır; 31-60, 61-90 ve 90 günden fazla (91+) sırasıyla diğer kovalardır.
func Bucket(daysPastDue int) string {
	switch {
	case daysPastDue <= 30:
		return "0-30"
	case daysPastDue <= 60:
		return "31-60"
	case daysPastDue <= 90:
		return "61-90"
	default:
		return "90+"
	}
}

type AgingContact struct {
	ContactID string           `json:"contact_id"`
	Name      string           `json:"name"`
	Buckets   map[string]int64 `json:"buckets"`
	Total     int64            `json:"total"`
}

type AgingInvoice struct {
	InvoiceID   string `json:"invoice_id"`
	Number      string `json:"number"`
	ContactID   string `json:"contact_id"`
	DueDate     string `json:"due_date"`
	DaysPastDue int    `json:"days_past_due"`
	Bucket      string `json:"bucket"`
	Open        int64  `json:"open"`
}

type AgingReport struct {
	AsOf     string           `json:"as_of"`
	Kind     string           `json:"kind"` // receivable | payable
	Buckets  map[string]int64 `json:"buckets"`
	Total    int64            `json:"total"`
	Contacts []AgingContact   `json:"contacts"`
	Invoices []AgingInvoice   `json:"invoices,omitempty"`
}

// Aging vade yaşlandırma raporu üretir. Açık tutar = fatura brütü − rapor tarihine kadar (dahil)
// yapılmış ve geri alınmamış mahsuplar. İptal edilmiş faturalar dahil değildir.
func Aging(ctx context.Context, q ledger.Querier, businessID, kind string, asOf time.Time, detail bool) (*AgingReport, error) {
	invKind := "sale"
	if kind == "payable" {
		invKind = "purchase"
	}
	rows, err := q.Query(ctx,
		`SELECT i.id::text, i.series, i.year, i.number, i.contact_id::text, c.name, i.due_date,
		        i.gross_total - coalesce((SELECT sum(a.amount) FROM payment_allocations a JOIN payments p ON p.id = a.payment_id
		                                   WHERE a.invoice_id = i.id AND a.reversed_at IS NULL AND p.pay_date <= $3), 0)::bigint
		   FROM invoices i JOIN contacts c ON c.id = i.contact_id
		  WHERE i.business_id = $1::uuid AND i.kind = $2 AND i.status = 'issued' AND i.issue_date <= $3
		  ORDER BY c.name, i.due_date, i.id`, businessID, invKind, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rep := &AgingReport{AsOf: asOf.Format(dateLayout), Kind: kind, Buckets: zeroBuckets(), Contacts: []AgingContact{}}
	byContact := map[string]*AgingContact{}
	for rows.Next() {
		var id, cid, name, series string
		var year int
		var seq, open int64
		var due time.Time
		if err := rows.Scan(&id, &series, &year, &seq, &cid, &name, &due, &open); err != nil {
			return nil, err
		}
		if open <= 0 {
			continue
		}
		days := int(asOf.Sub(due).Hours() / 24)
		b := Bucket(days)
		rep.Buckets[b] += open
		rep.Total += open
		c := byContact[cid]
		if c == nil {
			c = &AgingContact{ContactID: cid, Name: name, Buckets: zeroBuckets()}
			byContact[cid] = c
			rep.Contacts = append(rep.Contacts, AgingContact{})
		}
		c.Buckets[b] += open
		c.Total += open
		if detail {
			rep.Invoices = append(rep.Invoices, AgingInvoice{InvoiceID: id, Number: invoicing.FormatNumber(series, year, seq),
				ContactID: cid, DueDate: due.Format(dateLayout), DaysPastDue: days, Bucket: b, Open: open})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rep.Contacts = rep.Contacts[:0]
	for _, c := range byContact {
		rep.Contacts = append(rep.Contacts, *c)
	}
	sort.Slice(rep.Contacts, func(i, j int) bool {
		if rep.Contacts[i].Name != rep.Contacts[j].Name {
			return rep.Contacts[i].Name < rep.Contacts[j].Name
		}
		return rep.Contacts[i].ContactID < rep.Contacts[j].ContactID
	})
	return rep, nil
}

func zeroBuckets() map[string]int64 {
	m := make(map[string]int64, len(BucketNames))
	for _, n := range BucketNames {
		m[n] = 0
	}
	return m
}

type StatementRow struct {
	Date    string `json:"date"`
	Type    string `json:"type"` // invoice | payment
	Ref     string `json:"ref"`
	Status  string `json:"status"`
	Charge  int64  `json:"charge"`  // borçlandırma (fatura)
	Payment int64  `json:"payment"` // tahsilat/ödeme
	Balance int64  `json:"balance"` // yürüyen bakiye (iptal/silinenler hariç)
}

type Statement struct {
	ContactID string         `json:"contact_id"`
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Balance   int64          `json:"balance"`
	Rows      []StatementRow `json:"rows"`
}

// ContactStatement cari ekstresi: faturalar ve ödemeler tarih sırasıyla, yürüyen bakiyeyle.
func ContactStatement(ctx context.Context, q ledger.Querier, businessID, contactID, kind string) (*Statement, error) {
	invKind, payKind := "sale", "receipt"
	if kind == "payable" {
		invKind, payKind = "purchase", "disbursement"
	}
	st := &Statement{ContactID: contactID, Kind: kind, Rows: []StatementRow{}}
	if err := q.QueryRow(ctx, `SELECT name FROM contacts WHERE id = $1::uuid AND business_id = $2::uuid`, contactID, businessID).Scan(&st.Name); err != nil {
		return nil, ErrNotFound
	}
	rows, err := q.Query(ctx,
		`SELECT d, t, ref, st, amt FROM (
		   SELECT issue_date AS d, created_at AS c, 'invoice' AS t,
		          series || '-' || year || '-' || lpad(number::text, 6, '0') AS ref, status AS st, gross_total AS amt
		     FROM invoices WHERE business_id = $1::uuid AND contact_id = $2::uuid AND kind = $3
		   UNION ALL
		   SELECT pay_date, created_at, 'payment', id::text, status, amount
		     FROM payments WHERE business_id = $1::uuid AND contact_id = $2::uuid AND kind = $4
		 ) x ORDER BY d, c, ref`, businessID, contactID, invKind, payKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bal int64
	for rows.Next() {
		var r StatementRow
		var d time.Time
		var amt int64
		if err := rows.Scan(&d, &r.Type, &r.Ref, &r.Status, &amt); err != nil {
			return nil, err
		}
		r.Date = d.Format(dateLayout)
		counted := r.Status == "issued" || r.Status == "active"
		if r.Type == "invoice" {
			r.Charge = amt
			if counted {
				bal += amt
			}
		} else {
			r.Payment = amt
			if counted {
				bal -= amt
			}
		}
		r.Balance = bal
		st.Rows = append(st.Rows, r)
	}
	st.Balance = bal
	return st, rows.Err()
}
