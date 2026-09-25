package httpx

import (
	"net/url"
	"testing"
)

func TestPayloadReadBudgetUnitContract(t *testing.T) {
	for _, query := range []string{"limit=32768&limit_chars=1000", "limit_chars=", "limit_chars=0", "limit_chars=100001", "limit_chars=1.5", "offset=-1", "offset=0&offset=1", "limit_chars=1000&limit_chars=5000", "limit=3"} {
		values, _ := url.ParseQuery(query)
		if _, _, _, err := payloadReadBudget(values); err == nil {
			t.Fatal("ambiguous or invalid budget accepted", query)
		}
	}
	for _, test := range []struct {
		query        string
		offset       int64
		bytes, chars int
	}{
		{"", 0, 32768, 0}, {"offset=123&limit_chars=100000", 123, 0, 100000}, {"limit=262144", 0, 262144, 0},
	} {
		values, _ := url.ParseQuery(test.query)
		offset, bytes, chars, err := payloadReadBudget(values)
		if err != nil || offset != test.offset || bytes != test.bytes || chars != test.chars {
			t.Fatal(test, offset, bytes, chars, err)
		}
	}
}
