package nbs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// Rows from the January 2026 file, trimmed to three currencies.
const january = "\xEF\xBB\xBFDátum;USD;JPY;IDR\r\n" +
	"\"2. 1. 2026\";1,1721;183,94;\"19 593,06\"\r\n" +
	"\"5. 1. 2026\";1,1664;182,93;\"19 524,95\"\r\n" +
	"Priemer*;1,1738;183,9386;\"19 757,0189\"\r\n"

const year2025 = "\xEF\xBB\xBFObdobie;USD;BGN;IDR\r\n" +
	"12/2025;1,1709;1,9558;\"19 548,0501\"\r\n" +
	"\"4. štvrťrok\";1,1634;1,9558;\"19 386,4810\"\r\n" +
	"Priemer/2025;1,1300;1,9558;\"18 623,0584\"\r\n"

func TestParseMonth(t *testing.T) {
	daily, average, err := ParseMonth([]byte(january), 2026, time.January)
	if err != nil {
		t.Fatal(err)
	}
	want := []Rate{
		{"USD", "2026-01-02", "1.1721"}, {"JPY", "2026-01-02", "183.94"}, {"IDR", "2026-01-02", "19593.06"},
		{"USD", "2026-01-05", "1.1664"}, {"JPY", "2026-01-05", "182.93"}, {"IDR", "2026-01-05", "19524.95"},
	}
	if !slices.Equal(daily, want) {
		t.Fatalf("daily: %+v", daily)
	}
	if !slices.Equal(average, []Rate{{"USD", "2026-01", "1.1738"}, {"JPY", "2026-01", "183.9386"}, {"IDR", "2026-01", "19757.0189"}}) {
		t.Fatalf("average: %+v", average)
	}
}

func TestParseMonthRejectsChangedFormats(t *testing.T) {
	for name, data := range map[string]string{
		"other month":   "Dátum;USD\n\"2. 2. 2026\";1,17\n",
		"no date":       "Dátum;USD\n\"31. 1\";1,17\n",
		"impossible":    "Dátum;USD\n\"32. 1. 2026\";1,17\n",
		"bad header":    "Date;USD\n\"2. 1. 2026\";1,17\n",
		"bad currency":  "Dátum;US Dollar\n\"2. 1. 2026\";1,17\n",
		"negative rate": "Dátum;USD\n\"2. 1. 2026\";-1,17\n",
		"zero rate":     "Dátum;USD\n\"2. 1. 2026\";0\n",
		"text rate":     "Dátum;USD\n\"2. 1. 2026\";n/a\n",
		"html page":     "<!doctype html><html></html>",
	} {
		if _, _, err := ParseMonth([]byte(data), 2026, time.January); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseMonthWithoutRates(t *testing.T) {
	// A future month is a file with only the byte order mark.
	daily, average, err := ParseMonth([]byte("\xEF\xBB\xBF"), 2026, time.November)
	if err != nil || len(daily) != 0 || len(average) != 0 {
		t.Fatalf("%v %v %v", daily, average, err)
	}
	// Empty cells are days a currency was not published.
	daily, _, err = ParseMonth([]byte("Dátum;USD;JPY\n\"2. 1. 2026\";1,17;\n"), 2026, time.January)
	if err != nil || !slices.Equal(daily, []Rate{{"USD", "2026-01-02", "1.17"}}) {
		t.Fatalf("%v %v", daily, err)
	}
}

func TestParseYear(t *testing.T) {
	annual, err := ParseYear([]byte(year2025), 2025)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(annual, []Rate{{"USD", "2025", "1.1300"}, {"BGN", "2025", "1.9558"}, {"IDR", "2025", "18623.0584"}}) {
		t.Fatalf("annual: %+v", annual)
	}
	// The running year has no annual average yet.
	annual, err = ParseYear([]byte("Obdobie;USD\n1/2026;1,1738\n"), 2026)
	if err != nil || len(annual) != 0 {
		t.Fatalf("%v %v", annual, err)
	}
	if _, err := ParseYear([]byte(january), 2026); err == nil {
		t.Fatal("a monthly file is not a year file")
	}
}

func TestDecimal(t *testing.T) {
	for text, want := range map[string]string{
		"1,1721": "1.1721", "\"x\"": "", "19 593,06": "19593.06", "1 693,53": "1693.53",
		"1 693,53": "1693.53", "147,4": "147.4", "20 085": "20085", "1,2,3": "", "": "",
	} {
		got, err := Decimal(text)
		if (err != nil) != (want == "") || got != want {
			t.Errorf("%q: %q %v", text, got, err)
		}
	}
}

func TestAverageRoundsHalfUpToFourDecimals(t *testing.T) {
	for _, test := range []struct {
		rates []string
		want  string
	}{
		{[]string{"1.1721", "1.1664"}, "1.1693"},
		{[]string{"1.00005"}, "1.0001"},
		{[]string{"1.00004"}, "1.0000"},
		{[]string{"1", "2", "2"}, "1.6667"},
		{[]string{"19593.06", "19524.95"}, "19559.0050"},
	} {
		got, err := Average(test.rates)
		if err != nil || got != test.want {
			t.Errorf("%v: %s %v", test.rates, got, err)
		}
	}
	if _, err := Average(nil); err == nil {
		t.Fatal("no rates must fail")
	}
	if _, err := Average([]string{"-1"}); err == nil {
		t.Fatal("negative rates must fail")
	}
}

func TestClientRequestsMonthAndYearFiles(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/exchange-rate-month/2026-01/csv":
			w.Write([]byte(january))
		case "/exchange-rate-cumulative/2025/csv":
			w.Write([]byte(year2025))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewClient()
	client.BaseURL = server.URL + "/"
	daily, average, err := client.Month(context.Background(), 2026, time.January)
	if err != nil || len(daily) != 6 || len(average) != 3 {
		t.Fatalf("month: %d %d %v", len(daily), len(average), err)
	}
	if annual, err := client.Year(context.Background(), 2025); err != nil || len(annual) != 3 {
		t.Fatalf("year: %v %v", annual, err)
	}
	var failed *FetchError
	if _, _, err := client.Month(context.Background(), 2026, time.February); !errors.As(err, &failed) {
		t.Fatalf("missing file: %v", err)
	}
	if !slices.Equal(paths, []string{"/exchange-rate-month/2026-01/csv", "/exchange-rate-cumulative/2025/csv", "/exchange-rate-month/2026-02/csv"}) {
		t.Fatalf("paths: %v", paths)
	}
}
