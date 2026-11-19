package kicad

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strconv"
	"strings"
)

// View2D controls zoom and pan in screen pixels for 2D vector previews.
type View2D struct {
	Zoom float64
	PanX float64
	PanY float64
}

// DefaultView2D returns a centered fit-to-window view.
func DefaultView2D() View2D {
	return View2D{Zoom: 1.0}
}

type Pad2D struct {
	X, Y   float64
	W, H   float64
	RotDeg float64
	Shape  string
	Kind   string
	DrillW float64
	DrillH float64
	RRatio float64
	Number string
	Color  color.RGBA
}

type Line2D struct {
	X1, Y1, X2, Y2 float64
	Width          float64
	Color          color.RGBA
}

type Rect2D struct {
	X1, Y1, X2, Y2 float64
	Width          float64
	Stroke         color.RGBA
	Fill           color.RGBA
	Filled         bool
}

type Circle2D struct {
	CX, CY, R float64
	Width     float64
	Stroke    color.RGBA
	Fill      color.RGBA
	Filled    bool
}

type Poly2D struct {
	Pts    [][2]float64
	Width  float64
	Stroke color.RGBA
	Fill   color.RGBA
	Filled bool
}

type Text2D struct {
	X, Y  float64
	Text  string
	Size  float64
	Color color.RGBA
}

type Pin2D struct {
	X, Y     float64
	AngleDeg float64
	Length   float64
	Name     string
	Number   string
}

// Drawing2D represents a parsed KiCad footprint or symbol scene ready for rendering.
type Drawing2D struct {
	Kind       string // "footprint" or "symbol"
	Name       string
	Pads       []Pad2D
	Lines      []Line2D
	Rects      []Rect2D
	Circles    []Circle2D
	Polys      []Poly2D
	Texts      []Text2D
	Pins       []Pin2D
	MinX, MinY float64
	MaxX, MaxY float64
	hasBounds  bool
}

func (d *Drawing2D) expandBounds(x, y float64) {
	if !d.hasBounds {
		d.MinX, d.MaxX = x, x
		d.MinY, d.MaxY = y, y
		d.hasBounds = true
		return
	}
	if x < d.MinX {
		d.MinX = x
	}
	if x > d.MaxX {
		d.MaxX = x
	}
	if y < d.MinY {
		d.MinY = y
	}
	if y > d.MaxY {
		d.MaxY = y
	}
}

func (d *Drawing2D) finalizeBounds() {
	if !d.hasBounds {
		d.MinX, d.MinY, d.MaxX, d.MaxY = -5, -5, 5, 5
	}
	if d.MaxX-d.MinX < 0.5 {
		d.MinX -= 1
		d.MaxX += 1
	}
	if d.MaxY-d.MinY < 0.5 {
		d.MinY -= 1
		d.MaxY += 1
	}
}

// ParseFootprintDrawing parses a `.kicad_mod` S-expression into a 2D vector scene.
func ParseFootprintDrawing(rawSExpr string) (*Drawing2D, error) {
	node, err := ParseSExpr(rawSExpr)
	if err != nil {
		return nil, err
	}
	if node.Tag() != "footprint" && node.Tag() != "module" {
		return nil, fmt.Errorf("expected footprint/module node, got %q", node.Tag())
	}

	d := &Drawing2D{
		Kind: "footprint",
		Name: node.Arg(1),
	}

	for _, ch := range node.Children {
		switch ch.Tag() {
		case "pad":
			pad := parsePad2D(ch)
			d.Pads = append(d.Pads, pad)
			r := math.Hypot(pad.W, pad.H) * 0.5
			if r < 0.2 {
				r = 0.2
			}
			d.expandBounds(pad.X-r, pad.Y-r)
			d.expandBounds(pad.X+r, pad.Y+r)
		case "fp_line":
			x1, y1, ok1 := parseXYChild(ch, "start")
			x2, y2, ok2 := parseXYChild(ch, "end")
			if ok1 && ok2 {
				col := layerColor(childLayer(ch))
				w := parseStrokeWidth(ch, 0.12)
				d.Lines = append(d.Lines, Line2D{X1: x1, Y1: y1, X2: x2, Y2: y2, Width: w, Color: col})
				d.expandBounds(x1, y1)
				d.expandBounds(x2, y2)
			}
		case "fp_rect":
			x1, y1, ok1 := parseXYChild(ch, "start")
			x2, y2, ok2 := parseXYChild(ch, "end")
			if ok1 && ok2 {
				col := layerColor(childLayer(ch))
				w := parseStrokeWidth(ch, 0.12)
				filled := isFilledNode(ch)
				d.Rects = append(d.Rects, Rect2D{X1: x1, Y1: y1, X2: x2, Y2: y2, Width: w, Stroke: col, Fill: alphaColor(col, 45), Filled: filled})
				d.expandBounds(x1, y1)
				d.expandBounds(x2, y2)
			}
		case "fp_circle":
			cx, cy, ok1 := parseXYChild(ch, "center")
			ex, ey, ok2 := parseXYChild(ch, "end")
			if ok1 && ok2 {
				r := math.Hypot(ex-cx, ey-cy)
				col := layerColor(childLayer(ch))
				w := parseStrokeWidth(ch, 0.12)
				filled := isFilledNode(ch)
				d.Circles = append(d.Circles, Circle2D{CX: cx, CY: cy, R: r, Width: w, Stroke: col, Fill: alphaColor(col, 45), Filled: filled})
				d.expandBounds(cx-r, cy-r)
				d.expandBounds(cx+r, cy+r)
			}
		case "fp_arc":
			sx, sy, ok1 := parseXYChild(ch, "start")
			mx, my, okMid := parseXYChild(ch, "mid")
			ex, ey, ok2 := parseXYChild(ch, "end")
			col := layerColor(childLayer(ch))
			w := parseStrokeWidth(ch, 0.12)
			var pts [][2]float64
			if ok1 && okMid && ok2 {
				pts = sampleArc3Pt(sx, sy, mx, my, ex, ey)
			} else if ok1 && ok2 {
				ang := parseSingleFloatChild(ch, "angle")
				pts = sampleArcCenterAngle(sx, sy, ex, ey, ang)
			}
			if len(pts) >= 2 {
				d.Polys = append(d.Polys, Poly2D{Pts: pts, Width: w, Stroke: col})
				for _, pt := range pts {
					d.expandBounds(pt[0], pt[1])
				}
			}
		case "fp_poly":
			pts := parsePtsNode(ch.FindChild("pts"), false)
			if len(pts) >= 2 {
				col := layerColor(childLayer(ch))
				w := parseStrokeWidth(ch, 0.12)
				filled := isFilledNode(ch)
				if filled || len(pts) >= 3 {
					pts = append(pts, pts[0])
				}
				d.Polys = append(d.Polys, Poly2D{Pts: pts, Width: w, Stroke: col, Fill: alphaColor(col, 55), Filled: filled})
				for _, pt := range pts {
					d.expandBounds(pt[0], pt[1])
				}
			}
		case "fp_text", "property":
			if ch.FindChild("hide") != nil || isEffectsHidden(ch) {
				continue
			}
			txt := ch.Arg(2)
			if txt != "" {
				x, y, _, ok := parseAtNode(ch.FindChild("at"))
				if ok {
					col := layerColor(childLayer(ch))
					d.Texts = append(d.Texts, Text2D{X: x, Y: y, Text: txt, Size: 1.0, Color: col})
					d.expandBounds(x, y)
				}
			}
		}
	}

	d.finalizeBounds()
	return d, nil
}

// ParseSymbolDrawing parses a `.kicad_sym` or `(symbol ...)` S-expression into a 2D vector scene.
func ParseSymbolDrawing(rawSExpr string) (*Drawing2D, error) {
	node, err := ParseSExpr(rawSExpr)
	if err != nil {
		return nil, err
	}
	if node.Tag() == "kicad_symbol_lib" {
		syms := node.FindChildren("symbol")
		if len(syms) == 0 {
			return nil, fmt.Errorf("no symbols in library")
		}
		node = syms[0]
	}
	if node.Tag() != "symbol" {
		return nil, fmt.Errorf("expected symbol node, got %q", node.Tag())
	}

	d := &Drawing2D{
		Kind: "symbol",
		Name: node.Arg(1),
	}
	parseSymbolChildren2D(node, d)
	d.finalizeBounds()
	return d, nil
}

// ParseSymbolDrawingFromFile parses symbolName from a `.kicad_sym` file and resolves `(extends "...")`
// inheritance from parent symbols in the same library file.
func ParseSymbolDrawingFromFile(symLibPath, symbolName string) (*Drawing2D, error) {
	raw, err := ExtractSymbolSExprFromFile(symLibPath, symbolName)
	if err != nil {
		return nil, err
	}
	node, err := ParseSExpr(raw)
	if err != nil {
		return nil, err
	}
	d := &Drawing2D{
		Kind: "symbol",
		Name: node.Arg(1),
	}
	if ext := node.FindChild("extends"); ext != nil {
		if parentName := ext.Arg(1); parentName != "" {
			if pRaw, perr := ExtractSymbolSExprFromFile(symLibPath, parentName); perr == nil {
				if pNode, nerr := ParseSExpr(pRaw); nerr == nil {
					parseSymbolChildren2D(pNode, d)
				}
			}
		}
	}
	parseSymbolChildren2D(node, d)
	d.finalizeBounds()
	return d, nil
}

func parseSymbolChildren2D(node *Node, d *Drawing2D) {
	bodyStroke := color.RGBA{0xF0, 0x80, 0x70, 0xFF}
	bodyFill := color.RGBA{0x2B, 0x33, 0x40, 0xB0}
	textCol := color.RGBA{0x88, 0xD0, 0xB8, 0xFF}

	for _, ch := range node.Children {
		switch ch.Tag() {
		case "symbol":
			parseSymbolChildren2D(ch, d)
		case "rectangle":
			x1, y1, ok1 := parseXYChild(ch, "start")
			x2, y2, ok2 := parseXYChild(ch, "end")
			if ok1 && ok2 {
				y1, y2 = -y1, -y2
				w := parseStrokeWidth(ch, 0.254)
				filled := hasFillType(ch, "background") || hasFillType(ch, "outline")
				fillCol := bodyFill
				if hasFillType(ch, "outline") {
					fillCol = alphaColor(bodyStroke, 180)
				}
				d.Rects = append(d.Rects, Rect2D{X1: x1, Y1: y1, X2: x2, Y2: y2, Width: w, Stroke: bodyStroke, Fill: fillCol, Filled: filled})
				d.expandBounds(x1, y1)
				d.expandBounds(x2, y2)
			}
		case "circle":
			cx, cy, ok1 := parseXYChild(ch, "center")
			r := parseSingleFloatChild(ch, "radius")
			if ok1 && r > 0 {
				cy = -cy
				w := parseStrokeWidth(ch, 0.254)
				filled := hasFillType(ch, "background") || hasFillType(ch, "outline")
				d.Circles = append(d.Circles, Circle2D{CX: cx, CY: cy, R: r, Width: w, Stroke: bodyStroke, Fill: bodyFill, Filled: filled})
				d.expandBounds(cx-r, cy-r)
				d.expandBounds(cx+r, cy+r)
			}
		case "arc":
			sx, sy, ok1 := parseXYChild(ch, "start")
			mx, my, okMid := parseXYChild(ch, "mid")
			ex, ey, ok2 := parseXYChild(ch, "end")
			if ok1 && okMid && ok2 {
				pts := sampleArc3Pt(sx, -sy, mx, -my, ex, -ey)
				w := parseStrokeWidth(ch, 0.254)
				d.Polys = append(d.Polys, Poly2D{Pts: pts, Width: w, Stroke: bodyStroke})
				for _, pt := range pts {
					d.expandBounds(pt[0], pt[1])
				}
			}
		case "polyline":
			pts := parsePtsNode(ch.FindChild("pts"), true)
			if len(pts) >= 2 {
				w := parseStrokeWidth(ch, 0.254)
				filled := hasFillType(ch, "background") || hasFillType(ch, "outline")
				d.Polys = append(d.Polys, Poly2D{Pts: pts, Width: w, Stroke: bodyStroke, Fill: bodyFill, Filled: filled})
				for _, pt := range pts {
					d.expandBounds(pt[0], pt[1])
				}
			}
		case "pin":
			x, y, ang, ok := parseAtNode(ch.FindChild("at"))
			if !ok {
				continue
			}
			y = -y
			plen := parseSingleFloatChild(ch, "length")
			if plen <= 0 {
				plen = 2.54
			}
			var pName, pNum string
			if nn := ch.FindChild("name"); nn != nil {
				pName = nn.Arg(1)
			}
			if numN := ch.FindChild("number"); numN != nil {
				pNum = numN.Arg(1)
			}
			d.Pins = append(d.Pins, Pin2D{
				X: x, Y: y, AngleDeg: ang, Length: plen, Name: pName, Number: pNum,
			})
			rad := ang * math.Pi / 180.0
			ex := x + plen*math.Cos(rad)
			ey := y - plen*math.Sin(rad)
			d.expandBounds(x, y)
			d.expandBounds(ex, ey)
		case "property":
			k := ch.Arg(1)
			v := ch.Arg(2)
			if (k == "Reference" || k == "Value") && v != "" && !isEffectsHidden(ch) && ch.FindChild("hide") == nil {
				x, y, _, ok := parseAtNode(ch.FindChild("at"))
				if ok {
					d.Texts = append(d.Texts, Text2D{X: x, Y: -y, Text: v, Size: 1.27, Color: textCol})
					d.expandBounds(x, -y)
				}
			}
		}
	}
}

// RenderDrawing2D rasterizes a 2D footprint or symbol scene at width x height with the given View2D zoom and pan.
func RenderDrawing2D(d *Drawing2D, width, height int, view View2D) *image.RGBA {
	if width <= 0 {
		width = 1
	}
	if height <= 0 {
		height = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	bg := color.RGBA{0x11, 0x16, 0x1D, 0xFF}
	if d != nil && d.Kind == "symbol" {
		bg = color.RGBA{0x16, 0x1B, 0x22, 0xFF}
	}
	fillRectSolid(img, 0, 0, width, height, bg)

	if d == nil {
		return img
	}

	zoom := view.Zoom
	if zoom <= 0 {
		zoom = 1.0
	}

	bw := d.MaxX - d.MinX
	bh := d.MaxY - d.MinY
	margin := 0.80
	scaleX := float64(width) * margin / bw
	scaleY := float64(height) * margin / bh
	baseScale := math.Min(scaleX, scaleY)
	if baseScale <= 0 {
		baseScale = 20
	}
	scale := baseScale * zoom

	cxWorld := (d.MinX + d.MaxX) * 0.5
	cyWorld := (d.MinY + d.MaxY) * 0.5
	cxScreen := float64(width)*0.5 + view.PanX
	cyScreen := float64(height)*0.5 + view.PanY

	toScreen := func(wx, wy float64) (float64, float64) {
		return cxScreen + (wx-cxWorld)*scale, cyScreen + (wy-cyWorld)*scale
	}

	// Draw subtle adaptive grid
	drawGrid2D(img, width, height, cxWorld, cyWorld, cxScreen, cyScreen, scale)

	// Origin crosshair
	ox, oy := toScreen(0, 0)
	axisCol := color.RGBA{0x3B, 0x48, 0x5C, 0xFF}
	drawThickLine(img, ox-10, oy, ox+10, oy, 1, axisCol)
	drawThickLine(img, ox, oy-10, ox, oy+10, 1, axisCol)

	// 1. Filled rects and circles first
	for _, r := range d.Rects {
		x1, y1 := toScreen(r.X1, r.Y1)
		x2, y2 := toScreen(r.X2, r.Y2)
		if r.Filled {
			fillRectWorld(img, x1, y1, x2, y2, r.Fill)
		}
		lw := math.Max(1.0, r.Width*scale)
		if lw > 4 {
			lw = 4
		}
		drawThickLine(img, x1, y1, x2, y1, lw, r.Stroke)
		drawThickLine(img, x2, y1, x2, y2, lw, r.Stroke)
		drawThickLine(img, x2, y2, x1, y2, lw, r.Stroke)
		drawThickLine(img, x1, y2, x1, y1, lw, r.Stroke)
	}

	for _, c := range d.Circles {
		cx, cy := toScreen(c.CX, c.CY)
		rs := c.R * scale
		lw := math.Max(1.0, c.Width*scale)
		if lw > 4 {
			lw = 4
		}
		if c.Filled {
			fillCircle(img, cx, cy, rs, c.Fill)
		}
		drawCircleRing(img, cx, cy, rs, lw, c.Stroke)
	}

	// 2. Polylines & polygons
	for _, p := range d.Polys {
		if len(p.Pts) < 2 {
			continue
		}
		spts := make([][2]float64, len(p.Pts))
		for i, pt := range p.Pts {
			sx, sy := toScreen(pt[0], pt[1])
			spts[i] = [2]float64{sx, sy}
		}
		if p.Filled && len(spts) >= 3 {
			fillPolygon2D(img, spts, p.Fill)
		}
		lw := math.Max(1.0, p.Width*scale)
		if lw > 4 {
			lw = 4
		}
		for i := 0; i+1 < len(spts); i++ {
			drawThickLine(img, spts[i][0], spts[i][1], spts[i+1][0], spts[i+1][1], lw, p.Stroke)
		}
	}

	// 3. Lines
	for _, l := range d.Lines {
		x1, y1 := toScreen(l.X1, l.Y1)
		x2, y2 := toScreen(l.X2, l.Y2)
		lw := math.Max(1.0, l.Width*scale)
		if lw > 4 {
			lw = 4
		}
		drawThickLine(img, x1, y1, x2, y2, lw, l.Color)
	}

	// 4. Pads (PCB Footprints)
	for _, pad := range d.Pads {
		px, py := toScreen(pad.X, pad.Y)
		pw := pad.W * scale
		ph := pad.H * scale
		drawPad2D(img, px, py, pw, ph, pad.RotDeg, pad.Shape, pad.RRatio, pad.Color)
		if pad.DrillW > 0 {
			dw := pad.DrillW * scale
			dh := pad.DrillH * scale
			if dh <= 0 {
				dh = dw
			}
			drawPad2D(img, px, py, dw, dh, pad.RotDeg, "oval", 0, bg)
		}
		if pad.Number != "" && math.Min(pw, ph) >= 10 {
			drawTextCentered(img, int(px), int(py), pad.Number, 1, color.RGBA{0xF8, 0xFA, 0xFC, 0xFF})
		}
	}

	// 5. Pins (Schematic Symbols)
	pinCol := color.RGBA{0xE0, 0x6C, 0x75, 0xFF}
	pinNumCol := color.RGBA{0xE5, 0xC0, 0x7B, 0xFF}
	pinNameCol := color.RGBA{0x98, 0xC3, 0x79, 0xFF}
	for _, pin := range d.Pins {
		sx, sy := toScreen(pin.X, pin.Y)
		rad := pin.AngleDeg * math.Pi / 180.0
		dx := math.Cos(rad)
		dy := -math.Sin(rad)
		ex := sx + pin.Length*scale*dx
		ey := sy + pin.Length*scale*dy
		drawThickLine(img, sx, sy, ex, ey, 2.0, pinCol)
		drawCircleRing(img, sx, sy, 2.5, 1.5, pinCol)
		if pin.Length*scale >= 8 {
			if pin.Number != "" && pin.Number != "~" {
				drawTextCentered(img, int((sx+ex)*0.5), int((sy+ey)*0.5)-7, pin.Number, 1, pinNumCol)
			}
			if pin.Name != "" && pin.Name != "~" {
				drawTextCentered(img, int(ex+dx*14), int(ey+dy*10), pin.Name, 1, pinNameCol)
			}
		}
	}

	// 6. Labels
	for _, t := range d.Texts {
		tx, ty := toScreen(t.X, t.Y)
		drawTextCentered(img, int(tx), int(ty), t.Text, 1, t.Color)
	}

	// Subtle HUD label
	hud := fmt.Sprintf("%s  (Zoom %.0f%%)", d.Name, zoom*100)
	drawTextBitmap(img, 8, height-14, hud, 1, color.RGBA{0x94, 0xA3, 0xB8, 0xFF})

	return img
}

func parsePad2D(n *Node) Pad2D {
	p := Pad2D{
		Number: n.Arg(1),
		Kind:   n.Arg(2),
		Shape:  n.Arg(3),
		RRatio: 0.25,
	}
	if p.Shape == "" {
		p.Shape = "rect"
	}
	p.X, p.Y, p.RotDeg, _ = parseAtNode(n.FindChild("at"))
	if sz := n.FindChild("size"); sz != nil {
		p.W = parseFloatSafe(sz.Arg(1))
		p.H = parseFloatSafe(sz.Arg(2))
		if p.H == 0 {
			p.H = p.W
		}
	}
	if dr := n.FindChild("drill"); dr != nil {
		// (drill 0.8) or (drill oval 0.8 1.2)
		if dr.Arg(1) == "oval" {
			p.DrillW = parseFloatSafe(dr.Arg(2))
			p.DrillH = parseFloatSafe(dr.Arg(3))
		} else {
			p.DrillW = parseFloatSafe(dr.Arg(1))
			p.DrillH = p.DrillW
		}
	}
	if rr := n.FindChild("roundrect_rratio"); rr != nil {
		if v := parseFloatSafe(rr.Arg(1)); v > 0 {
			p.RRatio = v
		}
	}

	switch p.Kind {
	case "thru_hole":
		p.Color = color.RGBA{0xD4, 0xAA, 0x00, 0xFF}
	case "np_thru_hole":
		p.Color = color.RGBA{0x64, 0x74, 0x8B, 0xFF}
	default:
		p.Color = color.RGBA{0xC8, 0x34, 0x34, 0xFF}
		if layers := n.FindChild("layers"); layers != nil {
			for _, ch := range layers.Children[1:] {
				if strings.HasPrefix(ch.Value, "B.") {
					p.Color = color.RGBA{0x34, 0x65, 0xC8, 0xFF}
					break
				}
			}
		}
	}
	return p
}

func childLayer(n *Node) string {
	if l := n.FindChild("layer"); l != nil {
		return l.Arg(1)
	}
	return ""
}

func layerColor(layer string) color.RGBA {
	switch layer {
	case "F.Cu":
		return color.RGBA{0xC8, 0x34, 0x34, 0xFF}
	case "B.Cu":
		return color.RGBA{0x34, 0x65, 0xC8, 0xFF}
	case "F.SilkS", "F.Silkscreen":
		return color.RGBA{0x00, 0xC8, 0xC8, 0xFF}
	case "B.SilkS", "B.Silkscreen":
		return color.RGBA{0xC8, 0x00, 0xC8, 0xFF}
	case "F.CrtYd", "F.Courtyard":
		return color.RGBA{0xFF, 0x26, 0xE2, 0xFF}
	case "B.CrtYd", "B.Courtyard":
		return color.RGBA{0xA0, 0x40, 0xC0, 0xFF}
	case "F.Fab":
		return color.RGBA{0x94, 0xA3, 0xB8, 0xFF}
	case "B.Fab":
		return color.RGBA{0x64, 0x74, 0x8B, 0xFF}
	case "Edge.Cuts":
		return color.RGBA{0xE5, 0xC0, 0x20, 0xFF}
	default:
		return color.RGBA{0x94, 0xA3, 0xB8, 0xFF}
	}
}

func alphaColor(c color.RGBA, a uint8) color.RGBA {
	c.A = a
	return c
}

func parseXYChild(n *Node, tag string) (float64, float64, bool) {
	ch := n.FindChild(tag)
	if ch == nil || len(ch.Children) < 3 {
		return 0, 0, false
	}
	return parseFloatSafe(ch.Arg(1)), parseFloatSafe(ch.Arg(2)), true
}

func parseAtNode(at *Node) (float64, float64, float64, bool) {
	if at == nil || len(at.Children) < 3 {
		return 0, 0, 0, false
	}
	return parseFloatSafe(at.Arg(1)), parseFloatSafe(at.Arg(2)), parseFloatSafe(at.Arg(3)), true
}

func parseSingleFloatChild(n *Node, tag string) float64 {
	ch := n.FindChild(tag)
	if ch == nil || len(ch.Children) < 2 {
		return 0
	}
	return parseFloatSafe(ch.Arg(1))
}

func parseStrokeWidth(n *Node, def float64) float64 {
	if w := parseSingleFloatChild(n, "width"); w > 0 {
		return w
	}
	if st := n.FindChild("stroke"); st != nil {
		if w := parseSingleFloatChild(st, "width"); w > 0 {
			return w
		}
	}
	return def
}

func isFilledNode(n *Node) bool {
	if f := n.FindChild("fill"); f != nil {
		v := strings.ToLower(f.Arg(1))
		return v == "solid" || v == "yes"
	}
	return false
}

func hasFillType(n *Node, target string) bool {
	if f := n.FindChild("fill"); f != nil {
		if t := f.FindChild("type"); t != nil {
			return strings.EqualFold(t.Arg(1), target)
		}
	}
	return false
}

func isEffectsHidden(n *Node) bool {
	if eff := n.FindChild("effects"); eff != nil {
		if h := eff.FindChild("hide"); h != nil {
			v := strings.ToLower(h.Arg(1))
			return v == "" || v == "yes" || v == "true"
		}
	}
	return false
}

func parsePtsNode(pts *Node, flipY bool) [][2]float64 {
	if pts == nil {
		return nil
	}
	var out [][2]float64
	for _, ch := range pts.Children {
		if ch.Tag() == "xy" && len(ch.Children) >= 3 {
			x := parseFloatSafe(ch.Arg(1))
			y := parseFloatSafe(ch.Arg(2))
			if flipY {
				y = -y
			}
			out = append(out, [2]float64{x, y})
		}
	}
	return out
}

func parseFloatSafe(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func sampleArc3Pt(x1, y1, xm, ym, x2, y2 float64) [][2]float64 {
	d := 2 * (x1*(ym-y2) + xm*(y2-y1) + x2*(y1-ym))
	if math.Abs(d) < 1e-6 {
		return [][2]float64{{x1, y1}, {xm, ym}, {x2, y2}}
	}
	sq1 := x1*x1 + y1*y1
	sqm := xm*xm + ym*ym
	sq2 := x2*x2 + y2*y2
	ux := (sq1*(ym-y2) + sqm*(y2-y1) + sq2*(y1-ym)) / d
	uy := (sq1*(x2-xm) + sqm*(x1-x2) + sq2*(xm-x1)) / d
	r := math.Hypot(x1-ux, y1-uy)

	a1 := math.Atan2(y1-uy, x1-ux)
	am := math.Atan2(ym-uy, xm-ux)
	a2 := math.Atan2(y2-uy, x2-ux)

	// Normalize sweep direction so it passes through am
	norm := func(a float64) float64 {
		for a < 0 {
			a += 2 * math.Pi
		}
		for a >= 2*math.Pi {
			a -= 2 * math.Pi
		}
		return a
	}
	dm := norm(am - a1)
	d2 := norm(a2 - a1)
	sweep := d2
	if dm > d2 {
		sweep = d2 - 2*math.Pi
	}

	const steps = 24
	pts := make([][2]float64, 0, steps+1)
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		a := a1 + sweep*t
		pts = append(pts, [2]float64{ux + r*math.Cos(a), uy + r*math.Sin(a)})
	}
	return pts
}

func sampleArcCenterAngle(cx, cy, sx, sy, angleDeg float64) [][2]float64 {
	r := math.Hypot(sx-cx, sy-cy)
	if r < 1e-6 {
		return nil
	}
	a0 := math.Atan2(sy-cy, sx-cx)
	sweep := angleDeg * math.Pi / 180.0
	const steps = 24
	pts := make([][2]float64, 0, steps+1)
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		a := a0 + sweep*t
		pts = append(pts, [2]float64{cx + r*math.Cos(a), cy + r*math.Sin(a)})
	}
	return pts
}

// --- Software 2D Drawing Primitives ---

func blendPixel(img *image.RGBA, x, y int, c color.RGBA) {
	if x < 0 || y < 0 || x >= img.Bounds().Dx() || y >= img.Bounds().Dy() || c.A == 0 {
		return
	}
	if c.A == 255 {
		img.SetRGBA(x, y, c)
		return
	}
	dst := img.RGBAAt(x, y)
	a := uint32(c.A)
	ia := 255 - a
	img.SetRGBA(x, y, color.RGBA{
		R: uint8((uint32(c.R)*a + uint32(dst.R)*ia) / 255),
		G: uint8((uint32(c.G)*a + uint32(dst.G)*ia) / 255),
		B: uint8((uint32(c.B)*a + uint32(dst.B)*ia) / 255),
		A: 255,
	})
}

func fillRectSolid(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	b := img.Bounds()
	x0 = max(x0, b.Min.X)
	y0 = max(y0, b.Min.Y)
	x1 = min(x1, b.Max.X)
	y1 = min(y1, b.Max.Y)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func fillRectWorld(img *image.RGBA, x1, y1, x2, y2 float64, c color.RGBA) {
	if x1 > x2 {
		x1, x2 = x2, x1
	}
	if y1 > y2 {
		y1, y2 = y2, y1
	}
	for y := int(math.Floor(y1)); y <= int(math.Ceil(y2)); y++ {
		for x := int(math.Floor(x1)); x <= int(math.Ceil(x2)); x++ {
			blendPixel(img, x, y, c)
		}
	}
}

func drawGrid2D(img *image.RGBA, w, h int, cxWorld, cyWorld, cxScreen, cyScreen, scale float64) {
	// Pick nice step in mm: 0.1, 0.5, 1, 2.54, 5, 10, 25, 50
	steps := []float64{0.1, 0.5, 1.0, 2.54, 5.0, 10.0, 25.0, 50.0, 100.0}
	step := steps[len(steps)-1]
	for _, s := range steps {
		if s*scale >= 22 {
			step = s
			break
		}
	}
	gridCol := color.RGBA{0x1E, 0x26, 0x33, 0xFF}

	minWX := cxWorld - cxScreen/scale
	maxWX := cxWorld + (float64(w)-cxScreen)/scale
	minWY := cyWorld - cyScreen/scale
	maxWY := cyWorld + (float64(h)-cyScreen)/scale

	startX := math.Floor(minWX/step) * step
	for wx := startX; wx <= maxWX; wx += step {
		sx := int(math.Round(cxScreen + (wx-cxWorld)*scale))
		if sx >= 0 && sx < w {
			for y := 0; y < h; y++ {
				img.SetRGBA(sx, y, gridCol)
			}
		}
	}
	startY := math.Floor(minWY/step) * step
	for wy := startY; wy <= maxWY; wy += step {
		sy := int(math.Round(cyScreen + (wy-cyWorld)*scale))
		if sy >= 0 && sy < h {
			for x := 0; x < w; x++ {
				img.SetRGBA(x, sy, gridCol)
			}
		}
	}
}

func drawThickLine(img *image.RGBA, x1, y1, x2, y2, width float64, c color.RGBA) {
	r := math.Max(0.5, width*0.5)
	minX := max(0, int(math.Floor(math.Min(x1, x2)-r-1)))
	maxX := min(img.Bounds().Dx()-1, int(math.Ceil(math.Max(x1, x2)+r+1)))
	minY := max(0, int(math.Floor(math.Min(y1, y2)-r-1)))
	maxY := min(img.Bounds().Dy()-1, int(math.Ceil(math.Max(y1, y2)+r+1)))

	dx := x2 - x1
	dy := y2 - y1
	len2 := dx*dx + dy*dy

	for y := minY; y <= maxY; y++ {
		py := float64(y) + 0.5
		for x := minX; x <= maxX; x++ {
			px := float64(x) + 0.5
			var dist float64
			if len2 < 1e-9 {
				dist = math.Hypot(px-x1, py-y1)
			} else {
				t := ((px-x1)*dx + (py-y1)*dy) / len2
				if t < 0 {
					t = 0
				} else if t > 1 {
					t = 1
				}
				projX := x1 + t*dx
				projY := y1 + t*dy
				dist = math.Hypot(px-projX, py-projY)
			}
			cov := clamp01(r + 0.5 - dist)
			if cov > 0 {
				col := c
				col.A = uint8(float64(c.A) * cov)
				blendPixel(img, x, y, col)
			}
		}
	}
}

func fillCircle(img *image.RGBA, cx, cy, r float64, c color.RGBA) {
	if r <= 0 {
		return
	}
	minX := max(0, int(math.Floor(cx-r-1)))
	maxX := min(img.Bounds().Dx()-1, int(math.Ceil(cx+r+1)))
	minY := max(0, int(math.Floor(cy-r-1)))
	maxY := min(img.Bounds().Dy()-1, int(math.Ceil(cy+r+1)))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			dist := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			cov := clamp01(r + 0.5 - dist)
			if cov > 0 {
				col := c
				col.A = uint8(float64(c.A) * cov)
				blendPixel(img, x, y, col)
			}
		}
	}
}

func drawCircleRing(img *image.RGBA, cx, cy, r, width float64, c color.RGBA) {
	if r <= 0 {
		return
	}
	hw := math.Max(0.5, width*0.5)
	minX := max(0, int(math.Floor(cx-r-hw-1)))
	maxX := min(img.Bounds().Dx()-1, int(math.Ceil(cx+r+hw+1)))
	minY := max(0, int(math.Floor(cy-r-hw-1)))
	maxY := min(img.Bounds().Dy()-1, int(math.Ceil(cy+r+hw+1)))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			dist := math.Abs(math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy) - r)
			cov := clamp01(hw + 0.5 - dist)
			if cov > 0 {
				col := c
				col.A = uint8(float64(c.A) * cov)
				blendPixel(img, x, y, col)
			}
		}
	}
}

func drawPad2D(img *image.RGBA, cx, cy, w, h, rotDeg float64, shape string, rratio float64, c color.RGBA) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	rad := rotDeg * math.Pi / 180.0
	cosA := math.Cos(rad)
	sinA := math.Sin(rad)
	boundR := math.Hypot(w, h)*0.5 + 2
	minX := max(0, int(math.Floor(cx-boundR)))
	maxX := min(img.Bounds().Dx()-1, int(math.Ceil(cx+boundR)))
	minY := max(0, int(math.Floor(cy-boundR)))
	maxY := min(img.Bounds().Dy()-1, int(math.Ceil(cy+boundR)))

	hw, hh := w*0.5, h*0.5
	cornerR := 0.0
	switch shape {
	case "circle":
		rx := math.Max(hw, hh)
		hw, hh = rx, rx
		cornerR = rx
	case "oval":
		cornerR = math.Min(hw, hh)
	case "roundrect":
		cornerR = math.Min(w, h) * rratio
		if cornerR > math.Min(hw, hh) {
			cornerR = math.Min(hw, hh)
		}
	}

	for y := minY; y <= maxY; y++ {
		dy := float64(y) + 0.5 - cy
		for x := minX; x <= maxX; x++ {
			dx := float64(x) + 0.5 - cx
			// Rotate into pad local coordinates (KiCad footprint rotation is CCW in screen Y-down)
			lx := dx*cosA - dy*sinA
			ly := dx*sinA + dy*cosA

			qx := math.Abs(lx) - (hw - cornerR)
			qy := math.Abs(ly) - (hh - cornerR)
			ox := math.Max(qx, 0)
			oy := math.Max(qy, 0)
			dist := math.Hypot(ox, oy) + math.Min(math.Max(qx, qy), 0) - cornerR
			cov := clamp01(0.5 - dist)
			if cov > 0 {
				col := c
				col.A = uint8(float64(c.A) * cov)
				blendPixel(img, x, y, col)
			}
		}
	}
}

func fillPolygon2D(img *image.RGBA, pts [][2]float64, c color.RGBA) {
	if len(pts) < 3 {
		return
	}
	minY, maxY := pts[0][1], pts[0][1]
	for _, pt := range pts[1:] {
		if pt[1] < minY {
			minY = pt[1]
		}
		if pt[1] > maxY {
			maxY = pt[1]
		}
	}
	yStart := max(0, int(math.Floor(minY)))
	yEnd := min(img.Bounds().Dy()-1, int(math.Ceil(maxY)))

	var xs []float64
	for y := yStart; y <= yEnd; y++ {
		sy := float64(y) + 0.5
		xs = xs[:0]
		for i := 0; i < len(pts); i++ {
			j := (i + 1) % len(pts)
			y1, y2 := pts[i][1], pts[j][1]
			if (y1 <= sy && y2 > sy) || (y2 <= sy && y1 > sy) {
				x := pts[i][0] + (sy-y1)*(pts[j][0]-pts[i][0])/(y2-y1)
				xs = append(xs, x)
			}
		}
		for i := 1; i < len(xs); i++ {
			for k := i; k > 0 && xs[k] < xs[k-1]; k-- {
				xs[k], xs[k-1] = xs[k-1], xs[k]
			}
		}
		for i := 0; i+1 < len(xs); i += 2 {
			xStart := max(0, int(math.Floor(xs[i])))
			xEnd := min(img.Bounds().Dx()-1, int(math.Ceil(xs[i+1])))
			for x := xStart; x <= xEnd; x++ {
				blendPixel(img, x, y, c)
			}
		}
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// --- Built-in 5x7 Bitmap Font ---

var font5x7 = map[byte][7]uint8{
	' ': {0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
	'!': {0x04, 0x04, 0x04, 0x04, 0x04, 0x00, 0x04},
	'%': {0x18, 0x19, 0x02, 0x04, 0x08, 0x13, 0x03},
	'(': {0x02, 0x04, 0x08, 0x08, 0x08, 0x04, 0x02},
	')': {0x08, 0x04, 0x02, 0x02, 0x02, 0x04, 0x08},
	'+': {0x00, 0x04, 0x04, 0x1F, 0x04, 0x04, 0x00},
	',': {0x00, 0x00, 0x00, 0x00, 0x04, 0x04, 0x08},
	'-': {0x00, 0x00, 0x00, 0x1F, 0x00, 0x00, 0x00},
	'.': {0x00, 0x00, 0x00, 0x00, 0x00, 0x0C, 0x0C},
	'/': {0x01, 0x01, 0x02, 0x04, 0x08, 0x10, 0x10},
	'0': {0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E},
	'1': {0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'2': {0x0E, 0x11, 0x01, 0x06, 0x08, 0x10, 0x1F},
	'3': {0x1F, 0x02, 0x04, 0x06, 0x01, 0x11, 0x0E},
	'4': {0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02},
	'5': {0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E},
	'6': {0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E},
	'7': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08},
	'8': {0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E},
	'9': {0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C},
	':': {0x00, 0x0C, 0x0C, 0x00, 0x0C, 0x0C, 0x00},
	'=': {0x00, 0x00, 0x1F, 0x00, 0x1F, 0x00, 0x00},
	'A': {0x0E, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'B': {0x1E, 0x11, 0x11, 0x1E, 0x11, 0x11, 0x1E},
	'C': {0x0E, 0x11, 0x10, 0x10, 0x10, 0x11, 0x0E},
	'D': {0x1C, 0x12, 0x11, 0x11, 0x11, 0x12, 0x1C},
	'E': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x1F},
	'F': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x10},
	'G': {0x0E, 0x11, 0x10, 0x17, 0x11, 0x11, 0x0F},
	'H': {0x11, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'I': {0x0E, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'J': {0x07, 0x02, 0x02, 0x02, 0x12, 0x12, 0x0C},
	'K': {0x11, 0x12, 0x14, 0x18, 0x14, 0x12, 0x11},
	'L': {0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1F},
	'M': {0x11, 0x1B, 0x15, 0x15, 0x11, 0x11, 0x11},
	'N': {0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11},
	'O': {0x0E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'P': {0x1E, 0x11, 0x11, 0x1E, 0x10, 0x10, 0x10},
	'Q': {0x0E, 0x11, 0x11, 0x11, 0x15, 0x12, 0x0D},
	'R': {0x1E, 0x11, 0x11, 0x1E, 0x14, 0x12, 0x11},
	'S': {0x0F, 0x10, 0x10, 0x0E, 0x01, 0x01, 0x1E},
	'T': {0x1F, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04},
	'U': {0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'V': {0x11, 0x11, 0x11, 0x11, 0x11, 0x0A, 0x04},
	'W': {0x11, 0x11, 0x11, 0x15, 0x15, 0x1B, 0x11},
	'X': {0x11, 0x11, 0x0A, 0x04, 0x0A, 0x11, 0x11},
	'Y': {0x11, 0x11, 0x0A, 0x04, 0x04, 0x04, 0x04},
	'Z': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x10, 0x1F},
	'_': {0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x1F},
	'a': {0x00, 0x00, 0x0E, 0x01, 0x0F, 0x11, 0x0F},
	'b': {0x10, 0x10, 0x1E, 0x11, 0x11, 0x11, 0x1E},
	'c': {0x00, 0x00, 0x0E, 0x10, 0x10, 0x11, 0x0E},
	'd': {0x01, 0x01, 0x0F, 0x11, 0x11, 0x11, 0x0F},
	'e': {0x00, 0x00, 0x0E, 0x11, 0x1F, 0x10, 0x0E},
	'f': {0x06, 0x08, 0x1E, 0x08, 0x08, 0x08, 0x08},
	'g': {0x00, 0x00, 0x0F, 0x11, 0x0F, 0x01, 0x0E},
	'h': {0x10, 0x10, 0x1E, 0x11, 0x11, 0x11, 0x11},
	'i': {0x04, 0x00, 0x0C, 0x04, 0x04, 0x04, 0x0E},
	'j': {0x02, 0x00, 0x06, 0x02, 0x02, 0x12, 0x0C},
	'k': {0x10, 0x10, 0x12, 0x14, 0x18, 0x14, 0x12},
	'l': {0x0C, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'm': {0x00, 0x00, 0x1A, 0x15, 0x15, 0x11, 0x11},
	'n': {0x00, 0x00, 0x1E, 0x11, 0x11, 0x11, 0x11},
	'o': {0x00, 0x00, 0x0E, 0x11, 0x11, 0x11, 0x0E},
	'p': {0x00, 0x00, 0x1E, 0x11, 0x1E, 0x10, 0x10},
	'q': {0x00, 0x00, 0x0F, 0x11, 0x0F, 0x01, 0x01},
	'r': {0x00, 0x00, 0x16, 0x18, 0x10, 0x10, 0x10},
	's': {0x00, 0x00, 0x0F, 0x10, 0x0E, 0x01, 0x1E},
	't': {0x08, 0x08, 0x1E, 0x08, 0x08, 0x09, 0x06},
	'u': {0x00, 0x00, 0x11, 0x11, 0x11, 0x13, 0x0D},
	'v': {0x00, 0x00, 0x11, 0x11, 0x11, 0x0A, 0x04},
	'w': {0x00, 0x00, 0x11, 0x11, 0x15, 0x15, 0x0A},
	'x': {0x00, 0x00, 0x11, 0x0A, 0x04, 0x0A, 0x11},
	'y': {0x00, 0x00, 0x11, 0x11, 0x0F, 0x01, 0x0E},
	'z': {0x00, 0x00, 0x1F, 0x02, 0x04, 0x08, 0x1F},
	'~': {0x00, 0x00, 0x08, 0x15, 0x02, 0x00, 0x00},
}

func drawTextCentered(img *image.RGBA, cx, cy int, text string, scale int, c color.RGBA) {
	if scale < 1 {
		scale = 1
	}
	w := len(text) * 6 * scale
	h := 7 * scale
	drawTextBitmap(img, cx-w/2, cy-h/2, text, scale, c)
}

func drawTextBitmap(img *image.RGBA, x0, y0 int, text string, scale int, c color.RGBA) {
	if scale < 1 {
		scale = 1
	}
	cx := x0
	for i := 0; i < len(text); i++ {
		ch := text[i]
		glyph, ok := font5x7[ch]
		if !ok {
			if ch >= 'a' && ch <= 'z' {
				glyph = font5x7[ch-'a'+'A']
			} else {
				glyph = font5x7[' ']
			}
		}
		for row := 0; row < 7; row++ {
			bits := glyph[row]
			for col := 0; col < 5; col++ {
				if bits&(1<<(4-col)) != 0 {
					for sy := 0; sy < scale; sy++ {
						for sx := 0; sx < scale; sx++ {
							blendPixel(img, cx+col*scale+sx, y0+row*scale+sy, c)
						}
					}
				}
			}
		}
		cx += 6 * scale
	}
}
