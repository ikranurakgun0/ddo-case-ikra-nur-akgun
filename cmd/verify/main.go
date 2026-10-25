// EsnafDefter uçtan uca doğrulama komutu.
//
// Ayağa kalkmış ve seed edilmiş sistem üzerinde her kapsam maddesini (F-1..F-7), her zorlu görevi
// (T-1..T-4) ve kimlik/yetki senaryolarını HTTP API üzerinden dener; sonunda PASS/FAIL tablosu basar
// ve hata varsa sıfırdan farklı kodla çıkar. Doğrulama, canlı akışa dahil olmayan son işletmenin
// (isletme5) demo hesaplarıyla çalışır ve her çalıştırmada kendi benzersiz verisini üretir.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"esnafdefter/internal/auth"
	"esnafdefter/internal/sim"
)

const verifyBusiness = 5 // canlı akıştan muaf işletme

type env struct {
	c     *client
	run   string // bu çalıştırmaya özgü kısa ek (sku/seri çakışmasını önler)
	owner string
	acct  string
	emp   string
	cust  string
	seq   int
}

func (e *env) next() int { e.seq++; return e.seq }

func (e *env) sku(p string) string { return fmt.Sprintf("V%s-%s%d", e.run, p, e.next()) }

type check struct {
	id, desc string
	fn       func(*env)
}

func main() {
	api := os.Getenv("API_URL")
	if api == "" {
		api = "http://localhost:8080"
	}
	e := &env{c: &client{base: api, http: &http.Client{Timeout: 120 * time.Second}},
		run: strings.ToUpper(fmt.Sprintf("%x", time.Now().Unix()%0xFFFFF))}

	if err := waitReady(e); err != nil {
		fmt.Fprintln(os.Stderr, "sistem hazır değil:", err)
		os.Exit(2)
	}
	login := func(role auth.Role) string {
		r := e.c.do("POST", "/auth/login", "", map[string]string{"email": sim.DemoEmail(role, verifyBusiness), "password": sim.DemoPassword}, nil)
		if r.code != 200 {
			fmt.Fprintf(os.Stderr, "demo hesabı giriş yapamadı (%s): %d %v\n", role, r.code, r.body)
			os.Exit(2)
		}
		return r.str("access_token")
	}
	e.owner, e.acct, e.emp, e.cust = login(auth.RoleOwner), login(auth.RoleAccountant), login(auth.RoleEmployee), login(auth.RoleCustomer)

	checks := []check{
		{"HEALTH", "GET /health: veritabanı dahil sağlık; JSON hata biçimi", checkHealth},
		{"AUTH", "yanlış parola/bilinmeyen kullanıcı 401, belirteçsiz 401, refresh rotasyonu ve yeniden kullanım tespiti", checkAuth},
		{"RBAC", "yetkisiz rol 403, başka işletmenin verisi 404 (sunucu tarafı)", checkRBAC},
		{"F-1", "Cari hesaplar (müşteri/tedarikçi): oluştur, getir, sayfala, doğrula", checkF1},
		{"F-2", "Ürün ve hizmet kartları: oluştur, tekrarlı SKU, KDV doğrulama", checkF2},
		{"F-3", "Satış ve alış faturaları: toplamlar, KDV dahil/hariç, iptal", checkF3},
		{"F-4", "Tahsilat ve ödeme, kasa/banka hesapları, silme", checkF4},
		{"F-5", "Stok hareketleri ve stok raporu", checkF5},
		{"F-6", "KDV özeti ve mizan", checkF6},
		{"F-7", "Raporlar: yaşlandırma, ekstre (müşteri rolü salt okunur), stok", checkF7},
		{"T-1", "Çift kayıt: mizan dengesi, ters kayıtla iptal, kapalı dönem kilidi", checkT1},
		{"T-2", "Boşluksuz fatura numarası (30 eşzamanlı) ve KDV/iskonto hesabı", checkT2},
		{"T-3", "Tahsilat mahsubu (FIFO, avans, silme), vade yaşlandırma, eşzamanlı ödemeler", checkT3},
		{"T-4", "Ağırlıklı ortalama maliyet, geçmişe dönük yeniden hesap, negatif stok, eşzamanlı satış", checkT4},
		{"IDEMPOTENCY", "Idempotency-Key: tekrar istek aynı sonucu döndürür (fatura + ödeme, eşzamanlı)", checkIdempotency},
		{"AUDIT", "Denetim kaydı: kritik işlemler (kim, ne zaman, neyi, eski/yeni değer) yazılır; başarısız işlem iz bırakmaz; yalnızca owner/accountant okur", checkAudit},
	}

	start := time.Now()
	type result struct {
		id, desc string
		err      string
	}
	var results []result
	fmt.Printf("EsnafDefter doğrulama (çalıştırma %s, işletme %d demo hesapları)\n\n", e.run, verifyBusiness)
	for _, c := range checks {
		t0 := time.Now()
		msg := runCheck(e, c)
		status := "PASS"
		if msg != "" {
			status = "FAIL"
		}
		fmt.Printf("  %-12s %s  (%.1fs)\n", c.id, status, time.Since(t0).Seconds())
		results = append(results, result{c.id, c.desc, msg})
	}

	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KİMLİK\tSONUÇ\tAÇIKLAMA")
	fmt.Fprintln(tw, "------\t-----\t--------")
	failed := 0
	for _, r := range results {
		s := "PASS"
		if r.err != "" {
			s = "FAIL"
			failed++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.id, s, r.desc)
	}
	tw.Flush()
	for _, r := range results {
		if r.err != "" {
			fmt.Printf("\n[%s] BAŞARISIZ: %s\n", r.id, r.err)
		}
	}
	fmt.Printf("\n%d/%d geçti, süre %s\n", len(results)-failed, len(results), time.Since(start).Round(time.Second))
	if failed > 0 {
		os.Exit(1)
	}
}

func runCheck(e *env, c check) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			if f, ok := r.(failure); ok {
				msg = f.msg
			} else {
				msg = fmt.Sprintf("beklenmeyen hata: %v", r)
			}
		}
	}()
	c.fn(e)
	return ""
}

// waitReady API sağlığını ve seed'in tamamlanmasını bekler.
func waitReady(e *env) error {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		r := e.c.do("GET", "/health", "", nil, nil)
		if r.code == 200 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("API sağlıklı değil (%v)", r.body)
		}
		time.Sleep(2 * time.Second)
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return nil
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	deadline = time.Now().Add(15 * time.Minute)
	last := time.Time{}
	for {
		var status string
		err := pool.QueryRow(ctx, `SELECT status FROM seed_state WHERE id = 1`).Scan(&status)
		if err == nil && status == "done" {
			return nil
		}
		if time.Since(last) > 15*time.Second {
			fmt.Println("seed bekleniyor...")
			last = time.Now()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("seed zamanında tamamlanmadı")
		}
		time.Sleep(3 * time.Second)
	}
}
