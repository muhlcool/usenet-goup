package par2

import "testing"

func TestParparSliceSize(t *testing.T) {
	cases := map[int64]string{
		0:         "768K",
		10 << 30:  "768K",
		24 << 30:  "1536K",
		100 << 30: "3840K",
	}
	for in, want := range cases {
		if got := parparSliceSize(in); got != want {
			t.Errorf("%d: got %s want %s", in, got, want)
		}
	}
}
