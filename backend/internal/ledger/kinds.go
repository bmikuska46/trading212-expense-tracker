package ledger

import (
	"errors"
	"math/big"
)

const (
	KindExpense  = "expense"
	KindIncome   = "income"
	KindTransfer = "transfer"
	KindRefund   = "refund"
)

func ValidKind(kind string) bool {
	switch kind {
	case KindExpense, KindIncome, KindTransfer, KindRefund:
		return true
	}
	return false
}

// SignMatchesKind enforces the stored sign convention: expenses are
// negative, income and refunds are positive, and transfers are nonzero with
// the sign showing the direction of the leg.
func SignMatchesKind(kind string, minor int64) bool {
	switch kind {
	case KindExpense:
		return minor < 0
	case KindIncome, KindRefund:
		return minor > 0
	case KindTransfer:
		return minor != 0
	}
	return false
}

// SignedAmount applies the kind's sign to a positive magnitude. Transfers
// keep the direction given by outgoing.
func SignedAmount(kind string, magnitude int64, outgoing bool) int64 {
	if kind == KindExpense || (kind == KindTransfer && outgoing) {
		return -magnitude
	}
	return magnitude
}

// AllocateEURCents divides a parent EUR amount across split amounts in
// proportion to their original-currency amounts. Each share is rounded down
// in magnitude and the remaining cents go to the largest remainders, with
// ties resolved by position, so the shares always add up to the parent.
func AllocateEURCents(parentMinor, parentEURCents int64, parts []int64) ([]int64, error) {
	if len(parts) == 0 || parentMinor == 0 {
		return nil, errors.New("split parts and a nonzero parent amount are required")
	}
	var sum int64
	for _, part := range parts {
		if (part < 0) != (parentMinor < 0) || part == 0 {
			return nil, errors.New("split amounts must be nonzero and share the parent sign")
		}
		sum += part
	}
	if sum != parentMinor {
		return nil, errors.New("split amounts must add up to the parent amount")
	}
	total := big.NewInt(abs(parentEURCents))
	denominator := big.NewInt(abs(parentMinor))
	shares := make([]int64, len(parts))
	remainders := make([]*big.Int, len(parts))
	var allocated int64
	for index, part := range parts {
		quotient, remainder := new(big.Int).QuoRem(new(big.Int).Mul(total, big.NewInt(abs(part))), denominator, new(big.Int))
		shares[index] = quotient.Int64()
		remainders[index] = remainder
		allocated += shares[index]
	}
	for left := abs(parentEURCents) - allocated; left > 0; left-- {
		best := -1
		for index := range parts {
			if best < 0 || remainders[index].Cmp(remainders[best]) > 0 {
				best = index
			}
		}
		shares[best]++
		remainders[best] = big.NewInt(-1)
	}
	if parentEURCents < 0 {
		for index := range shares {
			shares[index] = -shares[index]
		}
	}
	return shares, nil
}

func abs(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
