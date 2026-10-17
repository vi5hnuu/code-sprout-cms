package problem

import (
	"errors"
	"strings"
	"testing"
)

func TestReadTestSet(t *testing.T) {
	cases, n, err := ReadTestSet(strings.NewReader(`{"input":"1 2","output":"3"}`+"\n"+`{"input":"","output":""}`+"\n"), true)
	if err != nil || n != 2 || len(cases) != 2 || cases[0].Stdin != "1 2" || cases[0].Expected != "3" {
		t.Fatalf("cases=%+v n=%d err=%v", cases, n, err)
	}
	for name, body := range map[string]string{
		"empty":         "",
		"missing field": `{"input":"1"}`,
		"unknown field": `{"input":"1","output":"2","extra":3}`,
		"not json":      `input,output`,
	} {
		if _, _, err := ReadTestSet(strings.NewReader(body), false); !errors.Is(err, ErrBadTestSet) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
