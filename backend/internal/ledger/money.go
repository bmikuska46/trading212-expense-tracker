package ledger

import (
	"errors"
	"math/big"
	"slices"
	"strings"
)

const RoundingRule = "half_away_from_zero"
const maxDecimalLength = 128

// Money stores signed currency minor units. The zero value is invalid.
type Money struct {
	minor    int64
	currency string
}

func MinorUnits(currency string) (int, error) {
	units, ok := currencyMinorUnits[currency]
	if !ok {
		return 0, errors.New("unsupported currency; use an ISO 4217 code with defined minor units")
	}
	return units, nil
}

func NewMoney(minor int64, currency string) (Money, error) {
	if _, err := MinorUnits(currency); err != nil {
		return Money{}, err
	}
	return Money{minor: minor, currency: currency}, nil
}

// ParseMoney accepts signed plain decimals, without exponents or separators.
// Excess decimal places are accepted only when they are zero, never rounded.
func ParseMoney(value, currency string) (Money, error) {
	units, err := MinorUnits(currency)
	if err != nil {
		return Money{}, err
	}
	numerator, scale, err := parseDecimal(value, true)
	if err != nil {
		return Money{}, err
	}
	numerator.Mul(numerator, powerOfTen(units))
	minor, remainder := new(big.Int), new(big.Int)
	minor.QuoRem(numerator, powerOfTen(scale), remainder)
	if remainder.Sign() != 0 {
		return Money{}, errors.New("amount has precision below the currency minor unit")
	}
	if !minor.IsInt64() {
		return Money{}, errors.New("amount exceeds signed 64-bit minor units")
	}
	return NewMoney(minor.Int64(), currency)
}

// RoundMoney accepts any decimal a JSON number can hold, including exponents,
// and rounds it once to minor units. Ties round away from zero. Use it only
// for values a provider reports with extra precision, never for user input.
func RoundMoney(value, currency string) (Money, error) {
	units, err := MinorUnits(currency)
	if err != nil {
		return Money{}, err
	}
	if len(value) == 0 || len(value) > maxDecimalLength {
		return Money{}, errors.New("expected a decimal of at most 128 characters")
	}
	// big.Rat also reads fractions and base prefixes, which JSON never sends,
	// and a huge exponent would be slow to expand.
	mantissa, exponent, scientific := strings.Cut(strings.ToLower(value), "e")
	if strings.Trim(mantissa, "+-.0123456789") != "" || strings.Trim(exponent, "+-0123456789") != "" ||
		(scientific && len(strings.TrimLeft(exponent, "+-0")) > 2) {
		return Money{}, errors.New("expected a decimal number")
	}
	exact, ok := new(big.Rat).SetString(value)
	if !ok {
		return Money{}, errors.New("expected a decimal number")
	}
	exact.Mul(exact, new(big.Rat).SetInt(powerOfTen(units)))
	minor, remainder := new(big.Int), new(big.Int)
	minor.QuoRem(exact.Num(), exact.Denom(), remainder)
	if new(big.Int).Lsh(remainder.Abs(remainder), 1).Cmp(exact.Denom()) >= 0 {
		minor.Add(minor, big.NewInt(int64(exact.Sign())))
	}
	if !minor.IsInt64() {
		return Money{}, errors.New("amount exceeds signed 64-bit minor units")
	}
	return NewMoney(minor.Int64(), currency)
}

func (m Money) Minor() int64     { return m.minor }
func (m Money) Currency() string { return m.currency }

func (m Money) Decimal() (string, error) {
	units, err := MinorUnits(m.currency)
	if err != nil {
		return "", err
	}
	value := big.NewInt(m.minor)
	sign := ""
	if value.Sign() < 0 {
		sign = "-"
		value.Abs(value)
	}
	digits := value.String()
	if units == 0 {
		return sign + digits, nil
	}
	if len(digits) <= units {
		digits = strings.Repeat("0", units+1-len(digits)) + digits
	}
	return sign + digits[:len(digits)-units] + "." + digits[len(digits)-units:], nil
}

// Rate retains the exact source decimal, including trailing zeroes.
// It expresses quote currency units per EUR. The zero value is invalid.
type Rate struct{ decimal string }

func ParseRate(value string) (Rate, error) {
	numerator, _, err := parseDecimal(value, false)
	if err != nil || numerator.Sign() <= 0 {
		return Rate{}, errors.New("rate must be a positive plain decimal")
	}
	return Rate{decimal: value}, nil
}

func (r Rate) Decimal() string { return r.decimal }

// EURCents divides by the EUR-base quote rate, then rounds once to signed cents.
// Ties round away from zero. Intermediate arithmetic has arbitrary precision.
func EURCents(amount Money, rate Rate) (int64, error) {
	units, err := MinorUnits(amount.currency)
	if err != nil {
		return 0, err
	}
	rateNumerator, rateScale, err := parseDecimal(rate.decimal, false)
	if err != nil || rateNumerator.Sign() <= 0 {
		return 0, errors.New("invalid conversion rate")
	}
	if amount.currency == "EUR" && rateNumerator.Cmp(powerOfTen(rateScale)) != 0 {
		return 0, errors.New("EUR requires rate 1")
	}
	numerator := big.NewInt(amount.minor)
	numerator.Mul(numerator, powerOfTen(rateScale+2))
	denominator := new(big.Int).Mul(powerOfTen(units), rateNumerator)
	cents, remainder := new(big.Int), new(big.Int)
	cents.QuoRem(numerator, denominator, remainder)
	if new(big.Int).Lsh(remainder.Abs(remainder), 1).Cmp(denominator) >= 0 {
		cents.Add(cents, big.NewInt(int64(numerator.Sign())))
	}
	if !cents.IsInt64() {
		return 0, errors.New("EUR amount exceeds signed 64-bit cents")
	}
	return cents.Int64(), nil
}

func parseDecimal(value string, signed bool) (*big.Int, int, error) {
	invalid := errors.New("expected a plain decimal of at most 128 characters")
	if len(value) == 0 || len(value) > maxDecimalLength {
		return nil, 0, invalid
	}
	negative := false
	if signed && (value[0] == '-' || value[0] == '+') {
		negative = value[0] == '-'
		value = value[1:]
	}
	whole, fraction, hasPoint := strings.Cut(value, ".")
	if whole == "" || (hasPoint && fraction == "") {
		return nil, 0, invalid
	}
	for _, digit := range whole + fraction {
		if digit < '0' || digit > '9' {
			return nil, 0, invalid
		}
	}
	numerator, ok := new(big.Int).SetString(whole+fraction, 10)
	if !ok {
		return nil, 0, invalid
	}
	if negative {
		numerator.Neg(numerator)
	}
	return numerator, len(fraction), nil
}

func powerOfTen(exponent int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil)
}

// Currencies lists the supported ISO 4217 codes in alphabetical order.
func Currencies() []string {
	codes := make([]string, 0, len(currencyMinorUnits))
	for code := range currencyMinorUnits {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	return codes
}
