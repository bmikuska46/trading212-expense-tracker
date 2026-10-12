package ledger

import (
	"slices"
	"testing"
)

func TestNormalizeNameTreatsEquivalentSpellingsAsOne(t *testing.T) {
	for _, group := range [][]string{
		{"RETAIL_STORES", "Retail stores", "  retail\tSTORES ", "Ｒｅｔａｉｌ　Ｓｔｏｒｅｓ", "retail__stores"},
		{"Café Nero", "CAFÉ NERO", "Café Nero"},
		{"Straße", "STRASSE"},
	} {
		want := NormalizeName(group[0])
		for _, name := range group[1:] {
			if got := NormalizeName(name); got != want {
				t.Fatalf("%q normalized to %q, want %q", name, got, want)
			}
		}
	}
	if NormalizeName("A&B") == NormalizeName("AB") {
		t.Fatal("punctuation must stay significant")
	}
	if NormalizeName(" \t_ ") != "" || ValidName("___") {
		t.Fatal("blank names must normalize to empty")
	}
}

func TestLabelNameHumanizesSourceLabels(t *testing.T) {
	for input, want := range map[string]string{
		"RETAIL_STORES":  "Retail stores",
		"MISCELLANEOUS":  "Miscellaneous",
		"Food & Drink":   "Food & Drink",
		"  groceries  ":  "groceries",
		"TRAVEL\nAGENCY": "Travel agency",
	} {
		if got := LabelName(input); got != want {
			t.Fatalf("LabelName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSignRules(t *testing.T) {
	for _, test := range []struct {
		kind  string
		minor int64
		ok    bool
	}{
		{KindExpense, -1, true}, {KindExpense, 1, false}, {KindIncome, 1, true}, {KindIncome, -1, false},
		{KindRefund, 1, true}, {KindRefund, -1, false}, {KindTransfer, -1, true}, {KindTransfer, 1, true},
		{KindTransfer, 0, false}, {"cashback", 1, false},
	} {
		if SignMatchesKind(test.kind, test.minor) != test.ok {
			t.Fatalf("%s %d", test.kind, test.minor)
		}
	}
}

func TestAllocateEURCentsAddsUpExactly(t *testing.T) {
	for _, test := range []struct {
		parent, eur int64
		parts, want []int64
	}{
		{-1000, -1000, []int64{-300, -700}, []int64{-300, -700}},
		{-100, -33, []int64{-50, -50}, []int64{-17, -16}},
		{300, 100, []int64{100, 100, 100}, []int64{34, 33, 33}},
		{1000, 1234, []int64{1, 999}, []int64{1, 1233}},
	} {
		got, err := AllocateEURCents(test.parent, test.eur, test.parts)
		if err != nil || !slices.Equal(got, test.want) {
			t.Fatalf("allocate %v: %v %v, want %v", test, got, err, test.want)
		}
	}
	for _, parts := range [][]int64{{-300, -600}, {-1100, 100}, {}, {-1000, 0}} {
		if _, err := AllocateEURCents(-1000, -1000, parts); err == nil {
			t.Fatalf("inconsistent split %v accepted", parts)
		}
	}
}
