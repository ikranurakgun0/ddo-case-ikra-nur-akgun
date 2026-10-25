// Package invoicing fatura hesabı (KDV, iskonto, kuruş yuvarlaması) ve fatura servisi.
//
// Tüm tutarlar kuruş cinsinden tam sayıdır; float hiçbir yerde kullanılmaz.
// Miktar "binde bir" birimle (quantity_milli) tutulur: 2,5 adet = 2500.
package invoicing

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

// ValidationError istemci hatasıdır (HTTP 400).
type ValidationError struct{ Code, Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func verr(code, format string, a ...any) error {
	return &ValidationError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

const (
	MaxLines         = 500
	maxQuantityMilli = 1_000_000_000      // 1 milyon birim
	maxUnitPrice     = 100_000_000_000    // 1 milyar TL (kuruş)
	maxLineTotal     = 10_000_000_000_000 // satır başına 100 milyar TL
)

// ValidVATRate yalnızca %1, %10, %20 kabul edilir.
func ValidVATRate(r int) bool { return r == 1 || r == 10 || r == 20 }

type LineInput struct {
	ProductID     string // isteğe bağlı; stok takipli ürünse stok hareketi üretir
	Description   string
	QuantityMilli int64
	UnitPrice     int64 // kuruş; prices_include_vat ise KDV dahil
	VATRate       int
	Discount      int64 // satır iskontosu, kuruş
}

type LineResult struct {
	LineInput
	AllocatedDiscount int64 // genel iskontonun bu satıra düşen payı
	Net               int64
	VAT               int64
	Gross             int64
}

type Result struct {
	Lines []LineResult
	Net   int64
	VAT   int64
	Gross int64
}

// Calculate fatura satırlarını hesaplar.
//
// Algoritma:
//  1. Satır tutarı = yuvarla(miktar × birim fiyat) − satır iskontosu
//  2. Genel iskonto, satır tutarlarına orantılı dağıtılır (en büyük kalan yöntemi):
//     paylar tam sayıdır ve toplamları genel iskontoya TAM eşittir.
//  3. KDV hariç fiyatta: net = satır tutarı, KDV = yuvarla(net × oran), brüt = net + KDV.
//     KDV dahil fiyatta: brüt = satır tutarı, net = yuvarla(brüt × 100 / (100+oran)), KDV = brüt − net.
//  4. Fatura toplamları satır değerlerinin toplamıdır; bu yüzden yuvarlama toplamı bozamaz.
//
// Yuvarlama: yarım yukarı (0,5 kuruş ve üstü yukarı).
func Calculate(in []LineInput, generalDiscount int64, includesVAT bool) (*Result, error) {
	if len(in) == 0 {
		return nil, verr("validation_error", "en az bir satır gerekli")
	}
	if len(in) > MaxLines {
		return nil, verr("validation_error", "en fazla %d satır olabilir", MaxLines)
	}
	if generalDiscount < 0 {
		return nil, verr("validation_error", "genel iskonto negatif olamaz")
	}

	base := make([]int64, len(in)) // iskonto sonrası satır tutarı (giriş bazında)
	var sumBase int64
	for i, l := range in {
		n := i + 1
		if strings.TrimSpace(l.Description) == "" {
			return nil, verr("validation_error", "satır %d: açıklama zorunlu", n)
		}
		if l.QuantityMilli <= 0 || l.QuantityMilli > maxQuantityMilli {
			return nil, verr("validation_error", "satır %d: miktar geçersiz", n)
		}
		if l.UnitPrice < 0 || l.UnitPrice > maxUnitPrice {
			return nil, verr("validation_error", "satır %d: birim fiyat geçersiz", n)
		}
		if !ValidVATRate(l.VATRate) {
			return nil, verr("invalid_vat_rate", "satır %d: KDV oranı %%1, %%10 veya %%20 olmalı", n)
		}
		if l.Discount < 0 {
			return nil, verr("validation_error", "satır %d: iskonto negatif olamaz", n)
		}
		gross := mulDivRound(l.QuantityMilli, l.UnitPrice, 1000)
		if gross > maxLineTotal {
			return nil, verr("validation_error", "satır %d: tutar çok büyük", n)
		}
		if l.Discount > gross {
			return nil, verr("discount_exceeds_total", "satır %d: iskonto satır tutarından büyük", n)
		}
		base[i] = gross - l.Discount
		sumBase += base[i]
	}
	if generalDiscount > sumBase {
		return nil, verr("discount_exceeds_total", "genel iskonto fatura tutarından büyük")
	}

	shares, err := distribute(generalDiscount, base)
	if err != nil {
		return nil, err
	}

	res := &Result{Lines: make([]LineResult, len(in))}
	for i, l := range in {
		after := base[i] - shares[i]
		var net, vat, gross int64
		if includesVAT {
			gross = after
			net = mulDivRound(gross, 100, int64(100+l.VATRate))
			vat = gross - net
		} else {
			net = after
			vat = mulDivRound(net, int64(l.VATRate), 100)
			gross = net + vat
		}
		res.Lines[i] = LineResult{LineInput: l, AllocatedDiscount: shares[i], Net: net, VAT: vat, Gross: gross}
		res.Net += net
		res.VAT += vat
		res.Gross += gross
	}
	if res.Gross <= 0 {
		return nil, verr("zero_total", "fatura toplamı sıfırdan büyük olmalı")
	}
	return res, nil
}

// mulDivRound (a*b)/c işlemini yarım-yukarı yuvarlayarak yapar. Ara çarpım taşmasın diye big kullanır.
// a, b >= 0 ve c > 0 olmalı.
func mulDivRound(a, b, c int64) int64 {
	n := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	n.Add(n, big.NewInt(c/2))
	n.Quo(n, big.NewInt(c))
	return n.Int64()
}

// distribute total tutarını weights ağırlıklarına göre tam sayı paylara böler (en büyük kalan yöntemi).
// Garanti: paylar toplamı = total ve 0 <= pay[i] <= weights[i].
func distribute(total int64, weights []int64) ([]int64, error) {
	out := make([]int64, len(weights))
	if total == 0 {
		return out, nil
	}
	var sumW int64
	for _, w := range weights {
		sumW += w
	}
	if sumW <= 0 || total > sumW {
		return nil, errors.New("dağıtılacak tutar ağırlıkları aşıyor")
	}
	type rem struct {
		idx int
		r   *big.Int
	}
	var rems []rem
	var allocated int64
	W := big.NewInt(sumW)
	for i, w := range weights {
		num := new(big.Int).Mul(big.NewInt(total), big.NewInt(w))
		q, r := new(big.Int).QuoRem(num, W, new(big.Int))
		out[i] = q.Int64()
		allocated += out[i]
		if r.Sign() > 0 {
			rems = append(rems, rem{i, r})
		}
	}
	left := total - allocated
	// Kalanı en büyük olandan başlayarak ver; eşitlikte düşük sıra numarası önce gelir (deterministik).
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].r.Cmp(rems[b].r) > 0 })
	for k := int64(0); k < left; k++ {
		out[rems[k].idx]++
	}
	return out, nil
}

// ParseQuantityMilli "2", "2.5", "0.125" gibi ondalık metni binde-bir birime çevirir.
// En fazla 3 ondalık basamak; negatif, üstel gösterim ve boş değer reddedilir.
func ParseQuantityMilli(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, verr("validation_error", "miktar zorunlu")
	}
	intPart, frac, hasDot := strings.Cut(s, ".")
	if intPart == "" || !allDigits(intPart) || (hasDot && (frac == "" || !allDigits(frac))) {
		return 0, verr("validation_error", "miktar geçersiz: %q", s)
	}
	if len(frac) > 3 {
		return 0, verr("validation_error", "miktar en fazla 3 ondalık basamak içerebilir")
	}
	if len(intPart) > 10 {
		return 0, verr("validation_error", "miktar çok büyük")
	}
	for len(frac) < 3 {
		frac += "0"
	}
	var v int64
	for _, c := range intPart + frac {
		v = v*10 + int64(c-'0')
	}
	if v <= 0 || v > maxQuantityMilli {
		return 0, verr("validation_error", "miktar geçersiz: %q", s)
	}
	return v, nil
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// NewValidationError diğer paketlerin de aynı istemci hatası türünü üretebilmesi içindir.
func NewValidationError(code, msg string) error { return &ValidationError{Code: code, Msg: msg} }
