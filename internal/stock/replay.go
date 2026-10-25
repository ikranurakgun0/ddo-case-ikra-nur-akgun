// Package stock ağırlıklı ortalama maliyet (AOM) hesabı ve stok hareketleri.
//
// Maliyet, bir ürünün hareketleri kronolojik sırayla baştan oynatılarak (replay) hesaplanır.
// Geçmişe dönük bir alış eklendiğinde/düzeltildiğinde aynı oynatma yeniden çalışır ve sonraki
// tüm çıkışların maliyeti kendiliğinden güncellenir. Bu, "artımlı güncelleme" yönteminin
// yaratabileceği tutarsızlıkları tasarım gereği ortadan kaldırır.
package stock

import (
	"math/big"
)

// Move oynatılacak tek bir stok hareketidir. Sıra, çağıran tarafından belirlenir:
// (tarih, önce girişler, sonra çıkışlar, id).
type Move struct {
	ID      int64
	Out     bool
	Qty     int64 // binde bir birim (1000 = 1,000)
	InValue int64 // yalnızca girişlerde: alış değeri (kuruş)
	Active  bool  // iptal edilmiş hareketler yok sayılır
}

type Result struct {
	Cost   map[int64]int64 // çıkış id -> hesaplanan maliyet (kuruş); iptal edilenler 0
	Qty    int64           // kapanış miktarı
	Value  int64           // kapanış stok değeri (kuruş)
	MinQty int64           // oynatma boyunca görülen en düşük miktar (negatif stok denetimi için)
}

// Replay hareketleri oynatır.
//
// Kurallar:
//   - Giriş: miktar ve değer toplamlara eklenir.
//   - Çıkış, stok yeterliyse: maliyet = yuvarla(toplam değer × çıkış miktarı / toplam miktar).
//     Çıkış stokun tamamını bitiriyorsa maliyet = kalan değerin TAMAMI. Böylece kuruş artığı kalmaz.
//   - Çıkış stoktan fazlaysa (negatif stok politikası açıksa): eldeki stok ortalamayla,
//     açık kalan kısım son giriş birim maliyetiyle değerlenir.
//   - Stok sıfır/negatifken çıkış: son giriş birim maliyeti kullanılır.
//
// Yuvarlama yarım-yukarıdır (sıfırdan uzağa).
func Replay(moves []Move) Result {
	res := Result{Cost: map[int64]int64{}}
	var q, v int64
	lastNum, lastDen := int64(0), int64(1) // son girişin birim maliyeti = lastNum/lastDen
	for _, m := range moves {
		if !m.Active {
			if m.Out {
				res.Cost[m.ID] = 0
			}
			continue
		}
		if !m.Out {
			q += m.Qty
			v += m.InValue
			lastNum, lastDen = m.InValue, m.Qty
		} else {
			var cost int64
			switch {
			case q > 0 && m.Qty <= q:
				if m.Qty == q {
					cost = v
				} else {
					cost = mulDivRound(v, m.Qty, q)
				}
			case q > 0: // eldeki stoktan fazla çıkış
				cost = v + mulDivRound(lastNum, m.Qty-q, lastDen)
			default:
				cost = mulDivRound(lastNum, m.Qty, lastDen)
			}
			res.Cost[m.ID] = cost
			q -= m.Qty
			v -= cost
		}
		if q < res.MinQty {
			res.MinQty = q
		}
	}
	res.Qty, res.Value = q, v
	return res
}

// mulDivRound (a*b)/c işlemini yarım-yukarı (sıfırdan uzağa) yuvarlar; a negatif olabilir. b>=0, c>0.
func mulDivRound(a, b, c int64) int64 {
	neg := a < 0
	if neg {
		a = -a
	}
	n := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	n.Add(n, big.NewInt(c/2))
	n.Quo(n, big.NewInt(c))
	r := n.Int64()
	if neg {
		return -r
	}
	return r
}

// AvgUnitCost kuruş cinsinden birim maliyet (1 tam birim için), raporlama amaçlıdır.
func AvgUnitCost(qtyMilli, value int64) int64 {
	if qtyMilli <= 0 {
		return 0
	}
	return mulDivRound(value, 1000, qtyMilli)
}
