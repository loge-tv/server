package media

// Text line detection on small gray keyframes. Credits are rows of letters:
// in a horizontal profile of strong luma steps they show up as bands a few
// pixels high with empty rows between, which natural pictures rarely have.
// The frame is cut into vertical strips so text beside a picture (credits
// next to a video) is found too.

const (
	LineW, LineH = 320, 180 // frame size the scan decodes to

	lineGrad    = 40 // |p[x+1]-p[x]| of a letter stroke (8 bit gray)
	lineStrips  = 4
	lineRowOn   = 4 // steps in a strip row of a text line
	lineMinBand = 2
	lineMaxBand = 12
)

// Lines returns the strip with the most text lines in a LineW×LineH gray
// frame, and the top rows of those lines.
func Lines(f []byte) (strip int, rows []int) {
	if len(f) < LineW*LineH {
		return 0, nil
	}
	sw := LineW / lineStrips
	for i := 0; i < lineStrips; i++ {
		if r := stripLines(f, i*sw, (i+1)*sw); len(r) > len(rows) {
			strip, rows = i, r
		}
	}
	return strip, rows
}

func stripLines(f []byte, x0, x1 int) []int {
	var steps [LineH]int
	for y := 0; y < LineH; y++ {
		row := f[y*LineW : (y+1)*LineW]
		n := 0
		for x := x0; x < x1-1; x++ {
			if d := int(row[x+1]) - int(row[x]); d > lineGrad || -d > lineGrad {
				n++
			}
		}
		steps[y] = n
	}
	var out []int
	for y := 0; y < LineH; {
		if steps[y] == 0 {
			y++
			continue
		}
		s, on := y, 0
		for y < LineH && steps[y] > 0 {
			if steps[y] >= lineRowOn {
				on++
			}
			y++
		}
		// A band touching the frame edge may be cut off: not a line.
		if h := y - s; h >= lineMinBand && h <= lineMaxBand && on*2 >= h && s > 0 && y < LineH {
			out = append(out, s)
		}
	}
	return out
}

// SameLayout reports whether two keyframes show the same text lines in the
// same place (a logo, a sign), not scrolled or replaced credits.
func SameLayout(a, b Frame) bool {
	if a.Strip != b.Strip || len(a.Rows) != len(b.Rows) {
		return false
	}
	for i := range a.Rows {
		if d := a.Rows[i] - b.Rows[i]; d > 1 || d < -1 {
			return false
		}
	}
	return true
}
