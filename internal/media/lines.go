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
	lineGap     = 4 // columns without strokes inside a line of text
)

// Lines returns the strip with the most text lines in a LineW×LineH gray
// frame, the top rows of those lines and their widths (see lineWidth).
func Lines(f []byte) (strip int, rows, widths []int) {
	if len(f) < LineW*LineH {
		return 0, nil, nil
	}
	sw := LineW / lineStrips
	var bands [][2]int
	for i := 0; i < lineStrips; i++ {
		if b := stripLines(f, i*sw, (i+1)*sw); len(b) > len(bands) {
			strip, bands = i, b
		}
	}
	for _, b := range bands {
		rows = append(rows, b[0])
		widths = append(widths, lineWidth(f, b[0], b[1], strip*sw, (strip+1)*sw))
	}
	return strip, rows, widths
}

// lineWidth measures the line in rows [y0, y1) that crosses the strip
// [x0, x1): the longest run of columns with letter strokes, gaps of up to
// lineGap columns (letter and word spacing) bridged, over the whole width.
// Text gives tens of columns; lamps, garlands and specks a few each.
func lineWidth(f []byte, y0, y1, x0, x1 int) int {
	var on [LineW]bool
	for y := y0; y < y1; y++ {
		row := f[y*LineW : (y+1)*LineW]
		for x := 0; x < LineW-1; x++ {
			if d := int(row[x+1]) - int(row[x]); d > lineGrad || -d > lineGrad {
				on[x] = true
			}
		}
	}
	best := 0
	for x := 0; x < LineW; {
		if !on[x] {
			x++
			continue
		}
		s, last := x, x
		for x < LineW && x-last <= lineGap+1 {
			if on[x] {
				last = x
			}
			x++
		}
		if last >= x0 && s < x1 {
			best = max(best, last-s+1)
		}
		x = last + 1
	}
	return best
}

func stripLines(f []byte, x0, x1 int) [][2]int {
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
	var out [][2]int
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
			out = append(out, [2]int{s, y})
		}
	}
	return out
}

// TextLines counts the frame's lines at least width columns wide: text,
// not a garland of lamps.
func (f Frame) TextLines(width int) int {
	n := 0
	for _, w := range f.Width {
		if w >= width {
			n++
		}
	}
	return n
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
