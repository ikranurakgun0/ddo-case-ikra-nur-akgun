package stock

import (
	"math/rand"
	"testing"
)

func in(id, qty, value int64) Move { return Move{ID: id, Qty: qty, InValue: value, Active: true} }
func out(id, qty int64) Move       { return Move{ID: id, Out: true, Qty: qty, Active: true} }

func TestWeightedAverageBasic(t *testing.T) {
	// 10 adet x 100 TL + 10 adet x 200 TL -> ortalama 150 TL; 5 adet satış = 750 TL
	r := Replay([]Move{in(1, 10000, 100000), in(2, 10000, 200000), out(3, 5000)})
	if r.Cost[3] != 75000 {
		t.Fatalf("maliyet 75000 olmalı: %d", r.Cost[3])
	}
	if r.Qty != 15000 || r.Value != 300000-75000 {
		t.Fatalf("kapanış: %d / %d", r.Qty, r.Value)
	}
}

func TestSellingAllLeavesNoResidue(t *testing.T) {
	// 3 adet toplam 100,00 TL; birer birer sat: 33,33 + 33,34 + 33,33 = 100,00
	r := Replay([]Move{in(1, 3000, 10000), out(2, 1000), out(3, 1000), out(4, 1000)})
	if r.Cost[2] != 3333 || r.Cost[3] != 3334 || r.Cost[4] != 3333 {
		t.Fatalf("maliyetler: %d %d %d", r.Cost[2], r.Cost[3], r.Cost[4])
	}
	if r.Qty != 0 || r.Value != 0 {
		t.Fatalf("stok bitince değer 0 olmalı: %d/%d", r.Qty, r.Value)
	}
}

func TestRetroactivePurchaseChangesLaterCosts(t *testing.T) {
	base := []Move{in(1, 10000, 100000), out(3, 5000)} // 10 x 100 TL; 5 sat -> 500 TL
	if c := Replay(base).Cost[3]; c != 50000 {
		t.Fatalf("başlangıç maliyeti: %d", c)
	}
	// Satıştan ÖNCE bir alış daha: 10 adet x 300 TL -> ortalama 200 TL, satış 1000 TL olmalı
	withEarlier := []Move{in(1, 10000, 100000), in(2, 10000, 300000), out(3, 5000)}
	if c := Replay(withEarlier).Cost[3]; c != 100000 {
		t.Fatalf("geçmişe dönük alış sonrası maliyet 100000 olmalı: %d", c)
	}
	// Satıştan SONRA gelen alış maliyeti etkilemez
	later := []Move{in(1, 10000, 100000), out(3, 5000), in(2, 10000, 300000)}
	if c := Replay(later).Cost[3]; c != 50000 {
		t.Fatalf("sonraki alış etkilememeli: %d", c)
	}
}

func TestCancelledMovementsAreIgnored(t *testing.T) {
	m := in(2, 10000, 300000)
	m.Active = false
	r := Replay([]Move{in(1, 10000, 100000), m, out(3, 5000)})
	if r.Cost[3] != 50000 {
		t.Fatalf("iptal edilen alış yok sayılmalı: %d", r.Cost[3])
	}
	o := out(4, 1000)
	o.Active = false
	if c, ok := Replay([]Move{in(1, 1000, 100), o}).Cost[4]; !ok || c != 0 {
		t.Fatalf("iptal edilen çıkışın maliyeti 0 olmalı: %d %v", c, ok)
	}
}

func TestNegativeStockUsesLastUnitCost(t *testing.T) {
	// 2 adet x 10 TL; 5 adet sat: 2 adet ortalamayla (20 TL) + 3 adet son birim maliyetiyle (30 TL) = 50 TL
	r := Replay([]Move{in(1, 2000, 2000), out(2, 5000)})
	if r.Cost[2] != 5000 || r.MinQty != -3000 {
		t.Fatalf("maliyet %d, min %d", r.Cost[2], r.MinQty)
	}
	if r.Qty != -3000 || r.Value != -3000 {
		t.Fatalf("kapanış: %d/%d", r.Qty, r.Value)
	}
	// Hiç stok yokken satış: son giriş yoksa maliyet 0
	if c := Replay([]Move{out(1, 1000)}).Cost[1]; c != 0 {
		t.Fatalf("girişsiz satış maliyeti 0: %d", c)
	}
}

func TestMinQtyDetectsBackdatedSale(t *testing.T) {
	// Satış alıştan önceye düşerse stok eksiye iner
	r := Replay([]Move{out(2, 1000), in(1, 5000, 500)})
	if r.MinQty >= 0 {
		t.Fatal("negatif stok tespit edilmeliydi")
	}
}

// Değişmez: stok tamamen satıldığında toplam maliyet = toplam alış değeri (kuruş kaybı yok).
func TestConservationRandomized(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 5000; iter++ {
		var moves []Move
		var id, q, totalIn int64
		for i := 0; i < 1+rng.Intn(10); i++ {
			id++
			if q == 0 || rng.Intn(3) != 0 {
				qty := int64(1 + rng.Intn(20000))
				val := int64(rng.Intn(5000000))
				moves = append(moves, in(id, qty, val))
				q += qty
				totalIn += val
			} else {
				qty := int64(1 + rng.Int63n(q))
				moves = append(moves, out(id, qty))
				q -= qty
			}
		}
		if q > 0 {
			id++
			moves = append(moves, out(id, q)) // kalanı sat
		}
		r := Replay(moves)
		var totalOut int64
		for _, c := range r.Cost {
			if c < 0 {
				t.Fatalf("iter %d: negatif maliyet", iter)
			}
			totalOut += c
		}
		if r.Qty != 0 || r.Value != 0 || totalOut != totalIn {
			t.Fatalf("iter %d: giriş %d çıkış %d kapanış %d/%d", iter, totalIn, totalOut, r.Qty, r.Value)
		}
		// Tekrar oynatma aynı sonucu vermeli (deterministik)
		r2 := Replay(moves)
		for k, v := range r.Cost {
			if r2.Cost[k] != v {
				t.Fatalf("iter %d: deterministik değil", iter)
			}
		}
	}
}
