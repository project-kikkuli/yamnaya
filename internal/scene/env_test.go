package scene

import "testing"

func TestAnimationIsOffUnlessAsked(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want bool
	}{{"", true}, {"1", true}, {"true", true}, {"0", false}} {
		if got := reducedMotion(tc.env); got != tc.want {
			t.Errorf("YM_REDUCED_MOTION=%q: reduced %v, want %v", tc.env, got, tc.want)
		}
	}
	if !Pace0() {
		t.Fatal("default scene ticks")
	}
}

func Pace0() bool { return Pace(Scene{}) == 0 || !ReducedMotion }
