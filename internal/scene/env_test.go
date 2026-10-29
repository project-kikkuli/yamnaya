package scene

import "testing"

func TestReducedMotionOnlyWhenAsked(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want bool
	}{{"", false}, {"0", false}, {"1", true}, {"true", true}} {
		if got := reducedMotion(tc.env); got != tc.want {
			t.Errorf("YM_REDUCED_MOTION=%q: reduced %v, want %v", tc.env, got, tc.want)
		}
	}
}
