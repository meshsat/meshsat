package oob

import "testing"

func TestWindow(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"first_counter_1", func(t *testing.T) {
			var w Window
			if !w.Check(1) {
				t.Fatal("counter 1 must be accepted on a fresh window")
			}
			w.Mark(1)
			if w.High != 1 || w.Bitmap != 1 {
				t.Fatalf("state after mark: %+v", w)
			}
		}},
		{"zero_rejected", func(t *testing.T) {
			var w Window
			if w.Check(0) {
				t.Fatal("counter 0 accepted")
			}
		}},
		{"in_order", func(t *testing.T) {
			var w Window
			for c := uint32(1); c <= 200; c++ {
				if !w.Check(c) {
					t.Fatalf("counter %d rejected", c)
				}
				w.Mark(c)
			}
			if w.High != 200 {
				t.Fatalf("high %d", w.High)
			}
		}},
		{"duplicate_rejected", func(t *testing.T) {
			var w Window
			w.Mark(5)
			if w.Check(5) {
				t.Fatal("duplicate accepted")
			}
		}},
		{"out_of_order_inside_window", func(t *testing.T) {
			var w Window
			w.Mark(100)
			for _, c := range []uint32{99, 50, 37} {
				if !w.Check(c) {
					t.Fatalf("counter %d rejected", c)
				}
				w.Mark(c)
				if w.Check(c) {
					t.Fatalf("counter %d accepted twice", c)
				}
			}
			if w.High != 100 {
				t.Fatalf("high moved to %d", w.High)
			}
		}},
		{"edge_high_minus_63_accepted", func(t *testing.T) {
			var w Window
			w.Mark(100)
			if !w.Check(37) {
				t.Fatal("high-63 rejected")
			}
		}},
		{"edge_high_minus_64_rejected", func(t *testing.T) {
			var w Window
			w.Mark(100)
			if w.Check(36) {
				t.Fatal("high-64 accepted")
			}
		}},
		{"jump_over_64_clears_bitmap", func(t *testing.T) {
			var w Window
			w.Mark(10)
			w.Mark(9)
			w.Mark(500)
			if w.Bitmap != 1 {
				t.Fatalf("bitmap after big jump: %b", w.Bitmap)
			}
			if w.Check(499) != true {
				t.Fatal("499 should be acceptable after the jump")
			}
		}},
		{"shift_keeps_recent_bits", func(t *testing.T) {
			var w Window
			w.Mark(10)
			w.Mark(12)
			if w.Check(10) {
				t.Fatal("10 accepted twice after shift")
			}
			if !w.Check(11) {
				t.Fatal("11 rejected")
			}
		}},
		{"max_counter_then_lower_rejected", func(t *testing.T) {
			var w Window
			w.Mark(MaxCounter)
			if w.Check(MaxCounter) {
				t.Fatal("max accepted twice")
			}
			if w.Check(MaxCounter - 64) {
				t.Fatal("outside window accepted")
			}
			if !w.Check(MaxCounter - 1) {
				t.Fatal("inside window rejected")
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, c.run)
	}
}
