package ledger_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"esnafdefter/internal/ledger"
	"esnafdefter/internal/testutil"
)

var d = func(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }

func post(t *testing.T, pool *pgxpool.Pool, e ledger.Entry) (string, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	id, err := ledger.Post(ctx, tx, e)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", ledger.MapError(err)
	}
	return id, nil
}

func sale(biz string, date string, amount int64) ledger.Entry {
	return ledger.Entry{BusinessID: biz, Date: d(date), Description: "test", SourceType: "manual",
		Lines: []ledger.Line{{Account: "120", Debit: amount}, {Account: "600", Credit: amount}}}
}

func TestPostBalancedEntryAndTrialBalance(t *testing.T) {
	pool := testutil.NewPool(t)
	biz := testutil.CreateBusiness(t, pool, "A", "1000000001")
	if _, err := post(t, pool, sale(biz, "2026-10-01", 12345)); err != nil {
		t.Fatal(err)
	}
	tb, err := ledger.Trial(context.Background(), pool, biz, d("2026-12-31"))
	if err != nil {
		t.Fatal(err)
	}
	if !tb.Balanced || tb.TotalDebit != 12345 || tb.TotalCredit != 12345 {
		t.Fatalf("mizan: %+v", tb)
	}
}

func TestGoValidationRejectsBadEntries(t *testing.T) {
	pool := testutil.NewPool(t)
	biz := testutil.CreateBusiness(t, pool, "A", "1000000002")
	cases := map[string][]ledger.Line{
		"dengesiz":      {{Account: "120", Debit: 100}, {Account: "600", Credit: 99}},
		"tek satır":     {{Account: "120", Debit: 100}},
		"iki taraflı":   {{Account: "120", Debit: 100, Credit: 100}, {Account: "600", Credit: 0, Debit: 0}},
		"negatif":       {{Account: "120", Debit: -5}, {Account: "600", Credit: -5}},
		"sıfır tutarlı": {{Account: "120"}, {Account: "600"}},
	}
	for name, lines := range cases {
		e := sale(biz, "2026-10-01", 1)
		e.Lines = lines
		if _, err := post(t, pool, e); err == nil {
			t.Errorf("%s: reddedilmeliydi", name)
		}
	}
	e := sale(biz, "2026-10-01", 5)
	e.Lines[0].Account = "999"
	if _, err := post(t, pool, e); !errors.Is(err, ledger.ErrUnknownAccount) {
		t.Errorf("bilinmeyen hesap: %v", err)
	}
}

// Uygulama kodunu atlayıp doğrudan SQL ile dengesiz kayıt yazmak da başarısız olmalı:
// kural veritabanında zorlanır.
func TestDatabaseRejectsUnbalancedEntryEvenViaRawSQL(t *testing.T) {
	pool := testutil.NewPool(t)
	biz := testutil.CreateBusiness(t, pool, "A", "1000000003")
	ctx := context.Background()
	tx, _ := pool.Begin(ctx)
	var eid string
	if err := tx.QueryRow(ctx, `INSERT INTO journal_entries(business_id,entry_date,description,source_type)
		VALUES ($1::uuid,'2026-10-01','kurnaz','manual') RETURNING id::text`, biz).Scan(&eid); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO journal_lines(entry_id,business_id,account_id,debit,credit)
		 SELECT $1::uuid,$2::uuid,id,100,0 FROM accounts WHERE business_id=$2::uuid AND code='120'`,
		`INSERT INTO journal_lines(entry_id,business_id,account_id,debit,credit)
		 SELECT $1::uuid,$2::uuid,id,0,99 FROM accounts WHERE business_id=$2::uuid AND code='600'`,
	} {
		if _, err := tx.Exec(ctx, sql, eid, biz); err != nil {
			t.Fatal(err)
		}
	}
	err := tx.Commit(ctx)
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "DD002" {
		t.Fatalf("commit DD002 ile reddedilmeliydi: %v", err)
	}
}

func TestDatabaseRejectsEntryWithoutLines(t *testing.T) {
	pool := testutil.NewPool(t)
	biz := testutil.CreateBusiness(t, pool, "A", "1000000004")
	ctx := context.Background()
	tx, _ := pool.Begin(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO journal_entries(business_id,entry_date,description,source_type)
		VALUES ($1::uuid,'2026-10-01','boş','manual')`, biz); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("satırsız kayıt reddedilmeliydi")
	}
}

func TestJournalIsImmutable(t *testing.T) {
	pool := testutil.NewPool(t)
	biz := testutil.CreateBusiness(t, pool, "A", "1000000005")
	if _, err := post(t, pool, sale(biz, "2026-10-01", 500)); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE journal_lines SET debit = debit + 1 WHERE debit > 0`,
		`DELETE FROM journal_lines`,
		`UPDATE journal_entries SET description = 'x'`,
		`DELETE FROM journal_entries`,
		`TRUNCATE journal_lines`,
		`TRUNCATE journal_entries CASCADE`,
	} {
		if _, err := pool.Exec(context.Background(), q); err == nil {
			t.Errorf("%q reddedilmeliydi", q)
		}
	}
}

func TestReverseZeroesOutAndCannotBeRepeated(t *testing.T) {
	pool := testutil.NewPool(t)
	biz := testutil.CreateBusiness(t, pool, "A", "1000000006")
	ctx := context.Background()
	id, err := post(t, pool, sale(biz, "2026-10-01", 777))
	if err != nil {
		t.Fatal(err)
	}
	rev := func() error {
		tx, _ := pool.Begin(ctx)
		defer tx.Rollback(ctx) //nolint:errcheck
		if _, err := ledger.Reverse(ctx, tx, biz, id, d("2026-10-02"), ""); err != nil {
			return err
		}
		return ledger.MapError(tx.Commit(ctx))
	}
	if err := rev(); err != nil {
		t.Fatal(err)
	}
	tb, _ := ledger.Trial(ctx, pool, biz, d("2026-12-31"))
	for _, a := range tb.Accounts {
		if a.Balance != 0 {
			t.Errorf("hesap %s ters kayıttan sonra sıfırlanmalıydı: %d", a.Code, a.Balance)
		}
	}
	if err := rev(); !errors.Is(err, ledger.ErrNotFound) && !errors.Is(err, ledger.ErrAlreadyReversed) {
		t.Fatalf("ikinci ters kayıt reddedilmeliydi: %v", err)
	}
}

func TestClosedPeriodBlocksPostingAndCannotReopen(t *testing.T) {
	pool := testutil.NewPool(t)
	biz := testutil.CreateBusiness(t, pool, "A", "1000000007")
	ctx := context.Background()
	if err := ledger.ClosePeriod(ctx, pool, biz, d("2026-09-30")); err != nil {
		t.Fatal(err)
	}
	if _, err := post(t, pool, sale(biz, "2026-09-30", 100)); !errors.Is(err, ledger.ErrPeriodClosed) {
		t.Fatalf("kapalı döneme kayıt reddedilmeliydi: %v", err)
	}
	if _, err := post(t, pool, sale(biz, "2026-10-01", 100)); err != nil {
		t.Fatalf("açık döneme kayıt girilebilmeli: %v", err)
	}
	for _, through := range []string{"2026-09-30", "2026-08-31"} {
		if err := ledger.ClosePeriod(ctx, pool, biz, d(through)); !errors.Is(err, ledger.ErrNotAdvancing) {
			t.Errorf("%s: dönem geri açılamamalı: %v", through, err)
		}
	}
	// Doğrudan SQL ile de geri açılamaz (tetikleyici)
	if _, err := pool.Exec(ctx, `UPDATE period_locks SET closed_through = '2026-01-01' WHERE business_id=$1::uuid`, biz); err == nil {
		t.Error("doğrudan SQL ile dönem açılabildi")
	}
}

func TestCannotLinkAnotherBusinessAccount(t *testing.T) {
	pool := testutil.NewPool(t)
	a := testutil.CreateBusiness(t, pool, "A", "1000000008")
	b := testutil.CreateBusiness(t, pool, "B", "1000000009")
	ctx := context.Background()
	tx, _ := pool.Begin(ctx)
	defer tx.Rollback(ctx) //nolint:errcheck
	var eid, accB string
	_ = tx.QueryRow(ctx, `INSERT INTO journal_entries(business_id,entry_date,description,source_type)
		VALUES ($1::uuid,'2026-10-01','x','manual') RETURNING id::text`, a).Scan(&eid)
	_ = tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE business_id=$1::uuid AND code='120'`, b).Scan(&accB)
	_, err := tx.Exec(ctx, `INSERT INTO journal_lines(entry_id,business_id,account_id,debit,credit) VALUES ($1::uuid,$2::uuid,$3::uuid,10,0)`, eid, a, accB)
	if err == nil {
		t.Fatal("başka işletmenin hesabına bağlanmaya izin verilmemeli (bileşik yabancı anahtar)")
	}
}

func TestConcurrentPostingsKeepBooksBalanced(t *testing.T) {
	pool := testutil.NewPool(t)
	biz := testutil.CreateBusiness(t, pool, "A", "1000000010")
	var wg sync.WaitGroup
	for i := 1; i <= 40; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, err := post(t, pool, sale(biz, "2026-10-01", int64(n*100+n))); err != nil {
				t.Errorf("kayıt %d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()
	tb, err := ledger.Trial(context.Background(), pool, biz, d("2026-12-31"))
	if err != nil || !tb.Balanced {
		t.Fatalf("mizan dengesiz: %+v %v", tb, err)
	}
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM journal_entries WHERE business_id=$1::uuid`, biz).Scan(&n)
	if n != 40 {
		t.Fatalf("40 kayıt bekleniyordu: %d", n)
	}
}

var _ pgx.Tx
