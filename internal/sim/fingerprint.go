package sim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// fingerprint içerik sorgularını sabit sırayla çalıştırıp SHA-256 özeti üretir.
// Kimlik (uuid), oluşturulma zamanı ve parola özeti dahil değildir: bunlar her çalıştırmada değişir.
func fingerprint(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}) (string, error) {
	h := sha256.New()
	queries := []string{
		`SELECT name, tax_no FROM businesses ORDER BY tax_no`,
		`SELECT b.tax_no, c.kind, c.name, coalesce(c.tax_no,'') FROM contacts c JOIN businesses b ON b.id=c.business_id ORDER BY 1,3,2`,
		`SELECT b.tax_no, p.sku, p.name, p.kind, p.vat_rate, p.sale_price FROM products p JOIN businesses b ON b.id=p.business_id ORDER BY 1,2`,
		`SELECT b.tax_no, i.kind, i.series, i.year, i.number, c.name, i.issue_date, i.due_date, i.status,
		        i.prices_include_vat, i.general_discount, i.net_total, i.vat_total, i.gross_total
		   FROM invoices i JOIN businesses b ON b.id=i.business_id JOIN contacts c ON c.id=i.contact_id ORDER BY 1,3,4,5`,
		`SELECT b.tax_no, i.series, i.year, i.number, l.line_no, coalesce(p.sku,''), l.quantity_milli, l.unit_price, l.vat_rate,
		        l.line_discount, l.allocated_discount, l.net_amount, l.vat_amount, l.gross_amount
		   FROM invoice_lines l JOIN invoices i ON i.id=l.invoice_id JOIN businesses b ON b.id=i.business_id
		   LEFT JOIN products p ON p.id=l.product_id ORDER BY 1,2,3,4,5`,
		`SELECT b.tax_no, p.kind, c.name, p.method, p.pay_date, p.amount, p.status
		   FROM payments p JOIN businesses b ON b.id=p.business_id JOIN contacts c ON c.id=p.contact_id ORDER BY 1,5,2,3,6,4,7`,
		`SELECT b.tax_no, a.code, coalesce(sum(l.debit),0)::bigint, coalesce(sum(l.credit),0)::bigint
		   FROM accounts a JOIN businesses b ON b.id=a.business_id LEFT JOIN journal_lines l ON l.account_id=a.id
		  GROUP BY b.tax_no, a.code ORDER BY 1,2`,
		`SELECT b.tax_no, p.sku, coalesce(sum(CASE WHEN m.active THEN CASE m.direction WHEN 'in' THEN m.quantity_milli ELSE -m.quantity_milli END END),0)::bigint,
		        coalesce(sum(CASE WHEN m.active THEN CASE m.direction WHEN 'in' THEN m.value ELSE -m.value END END),0)::bigint
		   FROM products p JOIN businesses b ON b.id=p.business_id LEFT JOIN stock_movements m ON m.product_id=p.id GROUP BY b.tax_no, p.sku ORDER BY 1,2`,
	}
	for _, sql := range queries {
		rows, err := q.Query(ctx, sql)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				rows.Close()
				return "", err
			}
			fmt.Fprintln(h, vals)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return "", err
		}
		fmt.Fprintln(h, "---")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
