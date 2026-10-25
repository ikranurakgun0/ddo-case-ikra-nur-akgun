package invoicing

import (
	"errors"
	"math/rand"
	"testing"
)

func line(qtyMilli, price int64, rate int, disc int64) LineInput {
	return LineInput{Description: "x", QuantityMilli: qtyMilli, UnitPrice: price, VATRate: rate, Discount: disc}
}

func TestVATExclusive(t *testing.T) {
	// 2 adet x 100,00 TL, %20 KDV hariç -> net 200,00; KDV 40,00; brüt 240,00
	r, err := Calculate([]LineInput{line(2000, 10000, 20, 0)}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Net != 20000 || r.VAT != 4000 || r.Gross != 24000 {
		t.Fatalf("beklenmeyen: %+v", r)
	}
}

func TestVATInclusive(t *testing.T) {
	// KDV dahil 120,00 TL, %20 -> net 100,00; KDV 20,00
	r, err := Calculate([]LineInput{line(1000, 12000, 20, 0)}, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Net != 10000 || r.VAT != 2000 || r.Gross != 12000 {
		t.Fatalf("beklenmeyen: %+v", r)
	}
}

func TestAllThreeRates(t *testing.T) {
	r, err := Calculate([]LineInput{line(1000, 10000, 1, 0), line(1000, 10000, 10, 0), line(1000, 10000, 20, 0)}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Lines[0].VAT != 100 || r.Lines[1].VAT != 1000 || r.Lines[2].VAT != 2000 || r.VAT != 3100 {
		t.Fatalf("KDV oranları yanlış: %+v", r)
	}
}

func TestHalfUpRounding(t *testing.T) {
	// 0,05 TL net, %10 -> 0,5 kuruş -> yarım yukarı: 1 kuruş
	r, _ := Calculate([]LineInput{line(1000, 5, 10, 0)}, 0, false)
	if r.Lines[0].VAT != 1 {
		t.Fatalf("0,5 kuruş yukarı yuvarlanmalı: %d", r.Lines[0].VAT)
	}
	// 0,04 TL net, %10 -> 0,4 kuruş -> 0
	r, _ = Calculate([]LineInput{line(1000, 4, 10, 0)}, 0, false)
	if r.Lines[0].VAT != 0 {
		t.Fatalf("0,4 kuruş aşağı yuvarlanmalı: %d", r.Lines[0].VAT)
	}
}

func TestFractionalQuantity(t *testing.T) {
	// 2,5 kg x 33,33 TL = 83,325 -> 83,33 (yarım yukarı)
	r, _ := Calculate([]LineInput{line(2500, 3333, 10, 0)}, 0, false)
	if r.Lines[0].Net != 8333 {
		t.Fatalf("net 8333 bekleniyordu: %d", r.Lines[0].Net)
	}
}

func TestGeneralDiscountDistributionIsExact(t *testing.T) {
	// 3 eşit satır, 1,00 TL genel iskonto -> 33+33+34 değil; toplam tam 100 olmalı
	r, err := Calculate([]LineInput{line(1000, 1000, 20, 0), line(1000, 1000, 20, 0), line(1000, 1000, 20, 0)}, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	for _, l := range r.Lines {
		sum += l.AllocatedDiscount
	}
	if sum != 100 {
		t.Fatalf("paylar toplamı 100 olmalı: %d", sum)
	}
	// deterministik: eşit kalanlarda ilk satırlar fazlayı alır
	if r.Lines[0].AllocatedDiscount != 34 || r.Lines[1].AllocatedDiscount != 33 || r.Lines[2].AllocatedDiscount != 33 {
		t.Fatalf("beklenmeyen dağılım: %d %d %d", r.Lines[0].AllocatedDiscount, r.Lines[1].AllocatedDiscount, r.Lines[2].AllocatedDiscount)
	}
	if r.Net != 3000-100 {
		t.Fatalf("net = satırlar − genel iskonto olmalı: %d", r.Net)
	}
}

func TestLineAndGeneralDiscountTogether(t *testing.T) {
	r, err := Calculate([]LineInput{line(1000, 10000, 10, 1000), line(1000, 5000, 20, 0)}, 500, false)
	if err != nil {
		t.Fatal(err)
	}
	// satır tutarları 9000 ve 5000 (toplam 14000), 500 iskonto: 321,43 -> 321 ve 178,57 -> 179
	if r.Lines[0].AllocatedDiscount+r.Lines[1].AllocatedDiscount != 500 {
		t.Fatal("iskonto payları toplamı 500 olmalı")
	}
	if r.Net != 14000-500 {
		t.Fatalf("net %d", r.Net)
	}
}

func TestRejects(t *testing.T) {
	cases := map[string]struct {
		lines []LineInput
		disc  int64
		code  string
	}{
		"boş":               {nil, 0, "validation_error"},
		"geçersiz KDV":      {[]LineInput{line(1000, 100, 18, 0)}, 0, "invalid_vat_rate"},
		"satır iskontosu>":  {[]LineInput{line(1000, 100, 20, 101)}, 0, "discount_exceeds_total"},
		"genel iskonto>":    {[]LineInput{line(1000, 100, 20, 0)}, 101, "discount_exceeds_total"},
		"sıfır toplam":      {[]LineInput{line(1000, 0, 20, 0)}, 0, "zero_total"},
		"tam iskonto sıfır": {[]LineInput{line(1000, 100, 20, 0)}, 100, "zero_total"},
		"negatif miktar":    {[]LineInput{line(-1, 100, 20, 0)}, 0, "validation_error"},
		"açıklamasız":       {[]LineInput{{QuantityMilli: 1000, UnitPrice: 1, VATRate: 20}}, 0, "validation_error"},
	}
	for name, c := range cases {
		_, err := Calculate(c.lines, c.disc, false)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Code != c.code {
			t.Errorf("%s: %q hatası bekleniyordu, geldi: %v", name, c.code, err)
		}
	}
}

func TestOverflowSafe(t *testing.T) {
	// En büyük kabul edilen miktar x fiyat: ara çarpım int64'ü aşar ama big ile güvenli; sonuç reddedilmeli.
	_, err := Calculate([]LineInput{line(maxQuantityMilli, maxUnitPrice, 20, 0)}, 0, false)
	if err == nil {
		t.Fatal("aşırı büyük tutar reddedilmeliydi")
	}
}

func TestParseQuantityMilli(t *testing.T) {
	ok := map[string]int64{"1": 1000, "2.5": 2500, "0.125": 125, "10": 10000, "0.001": 1}
	for in, want := range ok {
		got, err := ParseQuantityMilli(in)
		if err != nil || got != want {
			t.Errorf("%q: %d bekleniyordu, %d/%v", in, want, got, err)
		}
	}
	for _, bad := range []string{"", "-1", "1e3", "1.2345", "abc", ".5", "5.", "0", "0.000", "1,5"} {
		if _, err := ParseQuantityMilli(bad); err == nil {
			t.Errorf("%q reddedilmeliydi", bad)
		}
	}
}

// Rastgele binlerce fatura üzerinde değişmezler (invariants) kontrol edilir:
// kuruş yuvarlaması hiçbir durumda toplamı bozmamalı.
func TestInvariantsRandomized(t *testing.T) {
	rng := rand.New(rand.NewSource(42)) // sabit tohum: tekrarlanabilir
	rates := []int{1, 10, 20}
	for iter := 0; iter < 20000; iter++ {
		n := 1 + rng.Intn(8)
		lines := make([]LineInput, n)
		var sumBase int64
		for i := range lines {
			qty := int64(1 + rng.Intn(20000)) // 0,001 .. 20,000
			price := int64(rng.Intn(500000))  // 0 .. 5.000,00 TL
			l := line(qty, price, rates[rng.Intn(3)], 0)
			gross := mulDivRound(qty, price, 1000)
			if gross > 0 && rng.Intn(3) == 0 {
				l.Discount = int64(rng.Int63n(gross + 1))
			}
			lines[i] = l
			sumBase += gross - l.Discount
		}
		var gd int64
		if sumBase > 0 && rng.Intn(2) == 0 {
			gd = rng.Int63n(sumBase) // tamamını götürmesin
		}
		incl := rng.Intn(2) == 0
		r, err := Calculate(lines, gd, incl)
		if err != nil {
			var ve *ValidationError
			if errors.As(err, &ve) && ve.Code == "zero_total" {
				continue
			}
			t.Fatalf("iter %d: beklenmeyen hata: %v", iter, err)
		}
		var sn, sv, sg, sd int64
		for i, l := range r.Lines {
			if l.Net < 0 || l.VAT < 0 || l.AllocatedDiscount < 0 {
				t.Fatalf("iter %d: negatif değer: %+v", iter, l)
			}
			if l.Net+l.VAT != l.Gross {
				t.Fatalf("iter %d satır %d: net+kdv != brüt: %+v", iter, i, l)
			}
			sn += l.Net
			sv += l.VAT
			sg += l.Gross
			sd += l.AllocatedDiscount
		}
		if sn != r.Net || sv != r.VAT || sg != r.Gross {
			t.Fatalf("iter %d: toplamlar satır toplamlarına eşit değil", iter)
		}
		if r.Net+r.VAT != r.Gross {
			t.Fatalf("iter %d: fatura net+kdv != brüt", iter)
		}
		if sd != gd {
			t.Fatalf("iter %d: iskonto payları toplamı %d != genel iskonto %d", iter, sd, gd)
		}
	}
}
