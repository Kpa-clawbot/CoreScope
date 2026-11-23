package main

import "testing"

func TestSnrQuarterBin(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{-7.25, -7.25},
		{13.1, 13.0},
		{99, 31.75},
		{-99, -32},
	}
	for _, c := range cases {
		if got := binSNR(snrQuarterBin(c.in)); got != c.want {
			t.Errorf("binSNR(snrQuarterBin(%v)) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSnrHistQuantile(t *testing.T) {
	var h snrHist
	if _, ok := h.quantile(0.5); ok {
		t.Fatal("empty histogram must report ok == false")
	}
	x := &heardHour{}
	for _, v := range []float64{-10, -5, 0, 5, 10} {
		x.N++
		x.addSNR(v)
	}
	h.addHour(x)
	for _, c := range []struct{ q, want float64 }{{0.5, 0}, {0.1, -10}, {0.9, 10}} {
		got, ok := h.quantile(c.q)
		if !ok || got != c.want {
			t.Errorf("quantile(%v) = %v, %v; want %v", c.q, got, ok, c.want)
		}
	}
}
