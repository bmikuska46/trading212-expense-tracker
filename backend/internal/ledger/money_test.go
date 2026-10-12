package ledger

import (
	"math"
	"strings"
	"testing"
)

func TestExactMoneyParsingAndFormatting(t *testing.T) {
	for _, test := range []struct {
		value, currency, formatted string
		minor                      int64
	}{
		{"0.10", "EUR", "0.10", 10},
		{"-12.34", "EUR", "-12.34", -1234},
		{"+001.2300", "USD", "1.23", 123},
		{"-0.00", "EUR", "0.00", 0},
		{"123", "JPY", "123", 123},
		{"123.000", "JPY", "123", 123},
		{"1.234", "KWD", "1.234", 1234},
		{"0.0001", "CLF", "0.0001", 1},
		{"92233720368547758.07", "EUR", "92233720368547758.07", math.MaxInt64},
		{"-92233720368547758.08", "EUR", "-92233720368547758.08", math.MinInt64},
	} {
		t.Run(test.currency+"/"+test.value, func(t *testing.T) {
			amount, err := ParseMoney(test.value, test.currency)
			if err != nil || amount.Minor() != test.minor || amount.Currency() != test.currency {
				t.Fatalf("got %v, %v", amount, err)
			}
			formatted, err := amount.Decimal()
			if err != nil || formatted != test.formatted {
				t.Fatalf("formatted %q, %v", formatted, err)
			}
		})
	}
	// Binary floating point cannot represent 0.1 + 0.2 exactly.
	a, _ := ParseMoney("0.1", "EUR")
	b, _ := ParseMoney("0.2", "EUR")
	if a.Minor()+b.Minor() != 30 {
		t.Fatal("decimal sum is not exact")
	}
}

func TestRejectAmbiguousPrecisionAndOverflow(t *testing.T) {
	for _, value := range []string{"", "1e2", "NaN", "Inf", ".5", "1.", " 1", "1 ", "1,23", "1,000.00", "--1", "+", "1.2.3", "0.001", "92233720368547758.08", "-92233720368547758.09", strings.Repeat("0", 129)} {
		if _, err := ParseMoney(value, "EUR"); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	for _, currency := range []string{"eur", "XXX", "XAU", "BTC", "ZZZ", ""} {
		if _, err := ParseMoney("1", currency); err == nil {
			t.Errorf("accepted unsupported currency %q", currency)
		}
	}
	if _, err := ParseMoney("1.1", "JPY"); err == nil {
		t.Fatal("fractional yen accepted")
	}
	if _, err := (Money{}).Decimal(); err == nil {
		t.Fatal("invalid zero value accepted")
	}
}

func TestExactEURConversionAndRounding(t *testing.T) {
	for _, test := range []struct {
		amount, currency, rate string
		cents                  int64
	}{
		{"110", "USD", "1.100000", 10000},
		{"-110", "USD", "1.1", -10000},
		{"0.01", "USD", "2", 1},
		{"-0.01", "USD", "2", -1},
		{"0.01", "USD", "2.000000000000000000000000000001", 0},
		{"-0.01", "USD", "1.999999999999999999999999999999", -1},
		{"0.01", "USD", "3", 0},
		{"1.234", "KWD", "1", 123},
		{"1.235", "KWD", "1", 124},
		{"-1.235", "KWD", "1", -124},
		{"150", "JPY", "150", 100},
		{"0.0001", "CLF", "0.02", 1},
		{"-12.34", "EUR", "1.000", -1234},
		{"92233720368547758.07", "EUR", "1", math.MaxInt64},
		{"-92233720368547758.08", "EUR", "1", math.MinInt64},
	} {
		t.Run(test.amount+"/"+test.rate, func(t *testing.T) {
			amount, err := ParseMoney(test.amount, test.currency)
			if err != nil {
				t.Fatal(err)
			}
			rate, err := ParseRate(test.rate)
			if err != nil || rate.Decimal() != test.rate {
				t.Fatalf("rate changed: %v, %v", rate, err)
			}
			cents, err := EURCents(amount, rate)
			if err != nil || cents != test.cents {
				t.Fatalf("got %d, %v; want %d", cents, err, test.cents)
			}
		})
	}
}

func TestRejectInvalidRatesAndConversionOverflow(t *testing.T) {
	for _, value := range []string{"", "0", "0.000", "-1", "+1", "1e-2", "NaN", ".1", "1.", "1/2", " 1", strings.Repeat("1", 129)} {
		if _, err := ParseRate(value); err == nil {
			t.Errorf("accepted invalid rate %q", value)
		}
	}
	amount, _ := NewMoney(math.MaxInt64, "USD")
	small, _ := ParseRate("0.1")
	if _, err := EURCents(amount, small); err == nil {
		t.Fatal("conversion overflow accepted")
	}
	euro, _ := ParseMoney("1", "EUR")
	two, _ := ParseRate("2")
	if _, err := EURCents(euro, two); err == nil {
		t.Fatal("non-unit EUR rate accepted")
	}
	one, _ := ParseRate("1")
	if _, err := EURCents(Money{}, one); err == nil {
		t.Fatal("invalid money accepted")
	}
	if _, err := EURCents(amount, Rate{}); err == nil {
		t.Fatal("invalid rate accepted")
	}
}

func FuzzMoneyDecimalRoundTrip(f *testing.F) {
	f.Add(int64(0), "EUR")
	f.Add(int64(math.MinInt64), "JPY")
	f.Add(int64(math.MaxInt64), "KWD")
	f.Add(int64(-1), "CLF")
	f.Fuzz(func(t *testing.T, minor int64, currency string) {
		amount, err := NewMoney(minor, currency)
		if err != nil {
			return
		}
		decimal, err := amount.Decimal()
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseMoney(decimal, currency)
		if err != nil || parsed != amount {
			t.Fatalf("round trip changed amount: %v, %v", parsed, err)
		}
	})
}

func TestRoundProviderMoney(t *testing.T) {
	for _, test := range []struct {
		value, currency string
		minor           int64
	}{
		{"1234.5678", "EUR", 123457},
		{"-0.005", "EUR", -1},
		{"0.004999", "EUR", 0},
		{"2.5", "JPY", 3},
		{"1.5E-2", "EUR", 2},
		{"1e3", "EUR", 100000},
		{"7", "EUR", 700},
	} {
		amount, err := RoundMoney(test.value, test.currency)
		if err != nil || amount.Minor() != test.minor {
			t.Errorf("%s %s: got %v, %v", test.value, test.currency, amount, err)
		}
	}
	for _, value := range []string{"", "1/3", "0x10", "NaN", "Inf", "1e999", "92233720368547758.08", " 1", "1,5"} {
		if _, err := RoundMoney(value, "EUR"); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	if _, err := RoundMoney("1", "BTC"); err == nil {
		t.Fatal("unsupported currency accepted")
	}
}
