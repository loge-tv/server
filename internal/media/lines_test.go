package media

import (
	"math/rand"
	"testing"
)

func frame(fill byte) []byte {
	f := make([]byte, LineW*LineH)
	for i := range f {
		f[i] = fill
	}
	return f
}

// text draws a line of "letters" (bright strokes 2 px wide, 2 px apart)
// h rows high from row y, columns x0..x1.
func text(f []byte, y, h, x0, x1 int) {
	for r := y; r < y+h; r++ {
		for x := x0; x < x1; x++ {
			if x%4 < 2 {
				f[r*LineW+x] = 230
			}
		}
	}
}

func TestLines(t *testing.T) {
	// Credits on black: 6 lines centred.
	f := frame(0)
	for i := 0; i < 6; i++ {
		text(f, 20+i*25, 7, 100, 220)
	}
	if _, rows, _ := Lines(f); len(rows) != 6 || rows[0] != 20 {
		t.Fatalf("credits on black: %v", rows)
	}
	// Credits in a panel beside a textured picture (left half).
	rng := rand.New(rand.NewSource(1))
	f = frame(40)
	for y := 0; y < LineH; y++ {
		for x := 0; x < 160; x++ {
			f[y*LineW+x] = byte(rng.Intn(256))
		}
	}
	for i := 0; i < 5; i++ {
		text(f, 30+i*20, 6, 250, 310)
	}
	if strip, rows, _ := Lines(f); len(rows) != 5 || strip != 3 {
		t.Fatalf("credits beside a picture: strip %d rows %v", strip, rows)
	}
	// A textured picture alone: every row has steps, no gaps, no lines.
	for y := 0; y < LineH; y++ {
		for x := 160; x < LineW; x++ {
			f[y*LineW+x] = byte(rng.Intn(256))
		}
	}
	if _, rows, _ := Lines(f); len(rows) != 0 {
		t.Fatalf("picture taken for text: %v", rows)
	}
	// A flat dark frame with one bright lamp: a blob, not a line.
	f = frame(10)
	for y := 80; y < 86; y++ {
		for x := 150; x < 156; x++ {
			f[y*LineW+x] = 250
		}
	}
	if _, rows, _ := Lines(f); len(rows) != 0 {
		t.Fatalf("lamp taken for text: %v", rows)
	}
}

func TestLineWidth(t *testing.T) {
	// Two words of credits: 40 + 32 columns, a word space between.
	f := frame(0)
	text(f, 50, 7, 100, 140)
	text(f, 50, 7, 141, 173)
	// A garland: lamps 2 px wide every 9 px, rows that look like lines.
	for i := 0; i < 5; i++ {
		y := 100 + 15*i
		for x := 5 + 3*i; x < 75; x += 9 {
			for r := y; r < y+4; r++ {
				f[r*LineW+x], f[r*LineW+x+1] = 250, 250
			}
		}
	}
	// The strip with most lines is the garland's: its lines are narrow.
	strip, rows, widths := Lines(f)
	if strip != 0 || len(rows) != 5 || len(widths) != 5 {
		t.Fatalf("strip %d rows %v widths %v", strip, rows, widths)
	}
	if fr := (Frame{Rows: rows, Width: widths}); fr.TextLines(20) != 0 {
		t.Fatalf("garland lines counted as text: widths %v", widths)
	}
	// The words alone, in their own strip.
	g := frame(0)
	for i := 0; i < 4; i++ {
		text(g, 20+25*i, 7, 100, 140)
		text(g, 20+25*i, 7, 141, 173)
	}
	_, _, widths = Lines(g)
	if fr := (Frame{Width: widths}); fr.TextLines(20) != 4 || widths[0] < 70 {
		t.Fatalf("credits widths %v", widths)
	}
}

func TestSameLayout(t *testing.T) {
	a := Frame{Strip: 1, Rows: []int{10, 30, 50, 70}}
	if !SameLayout(a, Frame{Strip: 1, Rows: []int{11, 30, 49, 70}}) {
		t.Error("jitter of a row taken for movement")
	}
	if SameLayout(a, Frame{Strip: 1, Rows: []int{1, 21, 41, 61}}) {
		t.Error("scroll taken for a static layout")
	}
	if SameLayout(a, Frame{Strip: 2, Rows: a.Rows}) {
		t.Error("another strip taken for the same layout")
	}
}
