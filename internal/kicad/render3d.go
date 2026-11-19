package kicad

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Vec3 represents a 3D point or vector.
type Vec3 struct {
	X, Y, Z float64
}

func (v Vec3) Add(o Vec3) Vec3 { return Vec3{v.X + o.X, v.Y + o.Y, v.Z + o.Z} }
func (v Vec3) Sub(o Vec3) Vec3 { return Vec3{v.X - o.X, v.Y - o.Y, v.Z - o.Z} }
func (v Vec3) Scale(s float64) Vec3 {
	return Vec3{v.X * s, v.Y * s, v.Z * s}
}
func (v Vec3) Dot(o Vec3) float64 { return v.X*o.X + v.Y*o.Y + v.Z*o.Z }
func (v Vec3) Cross(o Vec3) Vec3 {
	return Vec3{
		v.Y*o.Z - v.Z*o.Y,
		v.Z*o.X - v.X*o.Z,
		v.X*o.Y - v.Y*o.X,
	}
}
func (v Vec3) Len() float64 { return math.Sqrt(v.X*v.X + v.Y*v.Y + v.Z*v.Z) }
func (v Vec3) Norm() Vec3 {
	l := v.Len()
	if l < 1e-12 {
		return Vec3{0, 0, 1}
	}
	return Vec3{v.X / l, v.Y / l, v.Z / l}
}

// Triangle3D stores a shaded 3D triangle.
type Triangle3D struct {
	V0, V1, V2 Vec3
	Normal     Vec3
	Color      color.RGBA
}

// Edge3D stores a 3D wireframe edge segment.
type Edge3D struct {
	A, B  Vec3
	Color color.RGBA
}

// Mesh3D holds tessellated 3D triangles, boundary edges, and bounding box extents.
type Mesh3D struct {
	Name      string
	Triangles []Triangle3D
	Edges     []Edge3D
	Min, Max  Vec3
	hasBounds bool
}

// Camera3D defines orbit yaw/pitch (degrees), zoom multiplier, and screen-space pan.
type Camera3D struct {
	Yaw   float64
	Pitch float64
	Zoom  float64
	PanX  float64
	PanY  float64
}

// DefaultCamera3D returns an isometric KiCad 3D viewer camera.
func DefaultCamera3D() Camera3D {
	return Camera3D{
		Yaw:   -35,
		Pitch: 55,
		Zoom:  1.0,
	}
}

func (m *Mesh3D) expand(v Vec3) {
	if !m.hasBounds {
		m.Min, m.Max = v, v
		m.hasBounds = true
		return
	}
	if v.X < m.Min.X {
		m.Min.X = v.X
	}
	if v.Y < m.Min.Y {
		m.Min.Y = v.Y
	}
	if v.Z < m.Min.Z {
		m.Min.Z = v.Z
	}
	if v.X > m.Max.X {
		m.Max.X = v.X
	}
	if v.Y > m.Max.Y {
		m.Max.Y = v.Y
	}
	if v.Z > m.Max.Z {
		m.Max.Z = v.Z
	}
}

func (m *Mesh3D) finalize() {
	if !m.hasBounds {
		m.Min = Vec3{-1, -1, -1}
		m.Max = Vec3{1, 1, 1}
	}
}

// Resolve3DModelPath returns path if it exists on disk, or probes sibling 3D model extensions
// (.stpZ, .stpz, .step, .stp, .wrl) in the same directory when footprints reference a missing extension.
func Resolve3DModelPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	if stem == "" {
		return path
	}
	for _, candExt := range []string{".stpZ", ".stpz", ".step", ".stp", ".wrl", ".STEP", ".STP", ".WRL"} {
		cand := stem + candExt
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return path
}

// LoadMesh3D loads a `.step`, `.stp`, `.stpZ` (ZIP or gzip-compressed STEP), or `.wrl` (VRML) 3D model.
func LoadMesh3D(path string) (*Mesh3D, error) {
	path = Resolve3DModelPath(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) >= 4 && raw[0] == 0x50 && raw[1] == 0x4b && raw[2] == 0x03 && raw[3] == 0x04 {
		zr, zerr := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if zerr != nil {
			return nil, fmt.Errorf("open zip stpZ: %w", zerr)
		}
		extracted := false
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, oerr := f.Open()
			if oerr != nil {
				return nil, fmt.Errorf("open entry %q in stpZ: %w", f.Name, oerr)
			}
			raw, err = io.ReadAll(io.LimitReader(rc, 64<<20))
			_ = rc.Close()
			if err != nil {
				return nil, fmt.Errorf("read entry %q in stpZ: %w", f.Name, err)
			}
			extracted = true
			break
		}
		if !extracted {
			return nil, fmt.Errorf("empty zip archive in %s", path)
		}
	} else if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gz, gerr := gzip.NewReader(bytes.NewReader(raw))
		if gerr != nil {
			return nil, fmt.Errorf("decompress stpZ: %w", gerr)
		}
		defer gz.Close()
		raw, err = io.ReadAll(io.LimitReader(gz, 64<<20))
		if err != nil {
			return nil, fmt.Errorf("read decompressed stpZ: %w", err)
		}
	}

	head := string(raw[:min(256, len(raw))])
	if strings.Contains(head, "ISO-10303-21") || strings.HasSuffix(strings.ToLower(path), ".step") ||
		strings.HasSuffix(strings.ToLower(path), ".stp") || strings.HasSuffix(strings.ToLower(path), ".stpz") {
		return parseSTEPMesh(path, raw)
	}
	if strings.Contains(head, "#VRML") || strings.HasSuffix(strings.ToLower(path), ".wrl") {
		return parseVRMLMesh(path, raw)
	}
	return nil, fmt.Errorf("unsupported 3D model format: %s", path)
}

// --- STEP Parser ---

type stepEntity struct {
	name string
	args []string
}

func parseSTEPMesh(name string, raw []byte) (*Mesh3D, error) {
	entities := parseSTEPEntities(string(raw))
	if len(entities) == 0 {
		return nil, fmt.Errorf("no STEP entities found in %s", name)
	}

	// Build color map from STYLED_ITEM / OVER_RIDING_STYLED_ITEM -> itemID
	colorCache := make(map[int]color.RGBA)
	itemColors := make(map[int]color.RGBA)
	for _, ent := range entities {
		if ent.name == "STYLED_ITEM" || ent.name == "OVER_RIDING_STYLED_ITEM" {
			if len(ent.args) >= 3 {
				targetID := parseStepRef(ent.args[2])
				if targetID > 0 {
					for _, sid := range parseStepRefList(ent.args[1]) {
						if col, ok := resolveSTEPColor(sid, entities, colorCache, 0); ok {
							itemColors[targetID] = col
							break
						}
					}
				}
			}
		}
	}

	defaultBodyCol := color.RGBA{0x7A, 0x88, 0x9C, 0xFF}
	edgeCol := color.RGBA{0x1D, 0x24, 0x30, 0xFF}

	mesh := &Mesh3D{Name: name}
	ptCache := make(map[int]Vec3)
	edgeCache := make(map[int][]Vec3)

	// Extract ADVANCED_FACE entities
	for id, ent := range entities {
		if ent.name != "ADVANCED_FACE" || len(ent.args) < 3 {
			continue
		}
		faceCol := defaultBodyCol
		if c, ok := itemColors[id]; ok {
			faceCol = c
		} else if len(ent.args) >= 3 {
			surfID := parseStepRef(ent.args[2])
			if c, ok := itemColors[surfID]; ok {
				faceCol = c
			}
		}

		boundIDs := parseStepRefList(ent.args[1])
		sameSense := len(ent.args) < 4 || strings.Contains(ent.args[3], ".T.")

		var outerLoop []Vec3
		var innerLoops [][]Vec3
		var outerEdgeParts [][]Vec3

		surfID := 0
		if len(ent.args) >= 3 {
			surfID = parseStepRef(ent.args[2])
		}
		surfName := ""
		if sEnt, okS := entities[surfID]; okS {
			surfName = sEnt.name
		}
		isCurvedSurface := surfName == "CYLINDRICAL_SURFACE" || surfName == "CONICAL_SURFACE" ||
			surfName == "TOROIDAL_SURFACE" || surfName == "SPHERICAL_SURFACE" || surfName == "SURFACE_OF_REVOLUTION"

		for _, bid := range boundIDs {
			bEnt, ok := entities[bid]
			if !ok {
				continue
			}
			isOuter := bEnt.name == "FACE_OUTER_BOUND"
			if bEnt.name != "FACE_OUTER_BOUND" && bEnt.name != "FACE_BOUND" {
				continue
			}
			if len(bEnt.args) < 2 {
				continue
			}
			loopID := parseStepRef(bEnt.args[1])
			pts, edgeParts := resolveEdgeLoopStructured(loopID, entities, ptCache, edgeCache)
			if len(pts) < 3 {
				continue
			}
			if isOuter || len(outerLoop) == 0 {
				if isOuter && len(outerLoop) > 0 {
					innerLoops = append(innerLoops, outerLoop)
				}
				outerLoop = pts
				outerEdgeParts = edgeParts
			} else {
				innerLoops = append(innerLoops, pts)
			}
		}

		if len(outerLoop) < 3 {
			continue
		}

		// Record crisp edges for outer & inner boundary loops
		addLoopEdges := func(loop []Vec3) {
			for i := 0; i < len(loop); i++ {
				a := loop[i]
				b := loop[(i+1)%len(loop)]
				mesh.Edges = append(mesh.Edges, Edge3D{A: a, B: b, Color: edgeCol})
				mesh.expand(a)
			}
		}
		addLoopEdges(outerLoop)
		for _, il := range innerLoops {
			addLoopEdges(il)
		}

		// Handle cylindrical/conical/toroidal/annular faces via 3D strip bridging
		if len(innerLoops) == 1 && len(innerLoops[0]) >= 3 && (isCurvedSurface || isLoopCircular(outerLoop) && isLoopCircular(innerLoops[0])) {
			tris := bridgeClosedLoops3D(outerLoop, innerLoops[0], faceCol)
			mesh.Triangles = append(mesh.Triangles, tris...)
			continue
		}
		if isCurvedSurface && len(outerEdgeParts) == 4 {
			if tris := tryBridgeFourEdgeSeamFace(outerEdgeParts, faceCol); len(tris) > 0 {
				mesh.Triangles = append(mesh.Triangles, tris...)
				continue
			}
		}

		tris := triangulateLoop3D(outerLoop, sameSense, faceCol)
		mesh.Triangles = append(mesh.Triangles, tris...)
	}

	// Fallback if STEP file contained curves/edges without ADVANCED_FACE
	if len(mesh.Triangles) == 0 {
		for id, ent := range entities {
			if ent.name == "EDGE_CURVE" {
				pts := resolveEdgeCurve(id, entities, ptCache, edgeCache)
				for i := 0; i+1 < len(pts); i++ {
					mesh.Edges = append(mesh.Edges, Edge3D{
						A:     pts[i],
						B:     pts[i+1],
						Color: color.RGBA{0x70, 0xC0, 0xF0, 0xFF},
					})
					mesh.expand(pts[i])
					mesh.expand(pts[i+1])
				}
			}
		}
	}

	// Final fallback: point cloud bounds
	if len(mesh.Triangles) == 0 && len(mesh.Edges) == 0 {
		for _, ent := range entities {
			if ent.name == "CARTESIAN_POINT" {
				if v, ok := parseCartesianPoint(ent); ok {
					mesh.expand(v)
				}
			}
		}
	}

	mesh.finalize()
	return mesh, nil
}

func parseSTEPEntities(src string) map[int]stepEntity {
	out := make(map[int]stepEntity)
	n := len(src)
	inStr := false
	inComment := false
	start := 0

	for i := 0; i < n; i++ {
		c := src[i]
		if inComment {
			if c == '*' && i+1 < n && src[i+1] == '/' {
				inComment = false
				i++
			}
			continue
		}
		if c == '\'' {
			if inStr && i+1 < n && src[i+1] == '\'' {
				i++
				continue
			}
			inStr = !inStr
			continue
		}
		if !inStr && c == '/' && i+1 < n && src[i+1] == '*' {
			inComment = true
			i++
			continue
		}
		if !inStr && c == ';' {
			stmt := strings.TrimSpace(src[start:i])
			start = i + 1
			if len(stmt) < 4 || stmt[0] != '#' {
				continue
			}
			eqIdx := strings.IndexByte(stmt, '=')
			if eqIdx <= 1 {
				continue
			}
			id, err := strconv.Atoi(strings.TrimSpace(stmt[1:eqIdx]))
			if err != nil {
				continue
			}
			rhs := strings.TrimSpace(stmt[eqIdx+1:])
			parenOpen := strings.IndexByte(rhs, '(')
			parenClose := strings.LastIndexByte(rhs, ')')
			if parenOpen <= 0 || parenClose <= parenOpen {
				continue
			}
			name := strings.ToUpper(strings.TrimSpace(rhs[:parenOpen]))
			args := splitStepArgs(rhs[parenOpen+1 : parenClose])
			out[id] = stepEntity{name: name, args: args}
		}
	}
	return out
}

func splitStepArgs(s string) []string {
	var args []string
	depth := 0
	inStr := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' {
			if inStr && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if tail := strings.TrimSpace(s[start:]); tail != "" || len(args) > 0 {
		args = append(args, tail)
	}
	return args
}

func parseStepRef(arg string) int {
	arg = strings.TrimSpace(arg)
	if len(arg) < 2 || arg[0] != '#' {
		return 0
	}
	id, err := strconv.Atoi(arg[1:])
	if err != nil {
		return 0
	}
	return id
}

func parseStepRefList(arg string) []int {
	arg = strings.TrimSpace(arg)
	if strings.HasPrefix(arg, "(") && strings.HasSuffix(arg, ")") {
		arg = arg[1 : len(arg)-1]
	}
	var ids []int
	for _, part := range strings.Split(arg, ",") {
		if id := parseStepRef(part); id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func parseStepFloatList(arg string) []float64 {
	arg = strings.TrimSpace(arg)
	if strings.HasPrefix(arg, "(") && strings.HasSuffix(arg, ")") {
		arg = arg[1 : len(arg)-1]
	}
	var vals []float64
	for _, part := range strings.Split(arg, ",") {
		v, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err == nil {
			vals = append(vals, v)
		}
	}
	return vals
}

func resolvePoint(id int, entities map[int]stepEntity, cache map[int]Vec3) (Vec3, bool) {
	if v, ok := cache[id]; ok {
		return v, true
	}
	ent, ok := entities[id]
	if !ok {
		return Vec3{}, false
	}
	if ent.name == "VERTEX_POINT" && len(ent.args) >= 2 {
		pid := parseStepRef(ent.args[1])
		v, ok := resolvePoint(pid, entities, cache)
		if ok {
			cache[id] = v
		}
		return v, ok
	}
	if ent.name == "CARTESIAN_POINT" || ent.name == "DIRECTION" {
		v, ok := parseCartesianPoint(ent)
		if ok {
			cache[id] = v
		}
		return v, ok
	}
	return Vec3{}, false
}

func parseCartesianPoint(ent stepEntity) (Vec3, bool) {
	if len(ent.args) < 2 {
		return Vec3{}, false
	}
	coords := parseStepFloatList(ent.args[1])
	if len(coords) < 3 {
		return Vec3{}, false
	}
	return Vec3{coords[0], coords[1], coords[2]}, true
}

func resolveAxis2(id int, entities map[int]stepEntity, ptCache map[int]Vec3) (origin, xAxis, yAxis, zAxis Vec3, ok bool) {
	ent, exists := entities[id]
	if !exists || ent.name != "AXIS2_PLACEMENT_3D" || len(ent.args) < 2 {
		return Vec3{}, Vec3{1, 0, 0}, Vec3{0, 1, 0}, Vec3{0, 0, 1}, false
	}
	origin, ok = resolvePoint(parseStepRef(ent.args[1]), entities, ptCache)
	if !ok {
		return Vec3{}, Vec3{1, 0, 0}, Vec3{0, 1, 0}, Vec3{0, 0, 1}, false
	}
	zAxis = Vec3{0, 0, 1}
	if len(ent.args) >= 3 {
		if z, okZ := resolvePoint(parseStepRef(ent.args[2]), entities, ptCache); okZ && z.Len() > 1e-9 {
			zAxis = z.Norm()
		}
	}
	xAxis = Vec3{1, 0, 0}
	if len(ent.args) >= 4 {
		if x, okX := resolvePoint(parseStepRef(ent.args[3]), entities, ptCache); okX && x.Len() > 1e-9 {
			xAxis = x.Norm()
		}
	}
	// Orthonormalize frame
	xAxis = xAxis.Sub(zAxis.Scale(xAxis.Dot(zAxis)))
	if xAxis.Len() < 1e-9 {
		if math.Abs(zAxis.X) < 0.9 {
			xAxis = Vec3{1, 0, 0}.Cross(zAxis).Norm()
		} else {
			xAxis = Vec3{0, 1, 0}.Cross(zAxis).Norm()
		}
	} else {
		xAxis = xAxis.Norm()
	}
	yAxis = zAxis.Cross(xAxis).Norm()
	return origin, xAxis, yAxis, zAxis, true
}

func resolveEdgeLoopStructured(loopID int, entities map[int]stepEntity, ptCache map[int]Vec3, edgeCache map[int][]Vec3) ([]Vec3, [][]Vec3) {
	ent, ok := entities[loopID]
	if !ok || ent.name != "EDGE_LOOP" || len(ent.args) < 2 {
		return nil, nil
	}
	oEdgeIDs := parseStepRefList(ent.args[1])
	var loop []Vec3
	edgeParts := make([][]Vec3, 0, len(oEdgeIDs))
	for _, oeID := range oEdgeIDs {
		oe, ok := entities[oeID]
		if !ok || oe.name != "ORIENTED_EDGE" || len(oe.args) < 5 {
			continue
		}
		edgeCurveID := parseStepRef(oe.args[3])
		pts := resolveEdgeCurve(edgeCurveID, entities, ptCache, edgeCache)
		if len(pts) < 2 {
			continue
		}
		orient := strings.Contains(oe.args[4], ".T.")
		ordered := make([]Vec3, len(pts))
		copy(ordered, pts)
		if !orient {
			for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
				ordered[i], ordered[j] = ordered[j], ordered[i]
			}
		}
		edgeParts = append(edgeParts, ordered)
		if len(loop) == 0 {
			loop = append(loop, ordered...)
		} else {
			// Avoid duplicating shared vertex
			last := loop[len(loop)-1]
			startIdx := 0
			if last.Sub(ordered[0]).Len() < 1e-4 {
				startIdx = 1
			}
			loop = append(loop, ordered[startIdx:]...)
		}
	}
	if len(loop) >= 2 && loop[0].Sub(loop[len(loop)-1]).Len() < 1e-4 {
		loop = loop[:len(loop)-1]
	}
	return loop, edgeParts
}

func isLoopCircular(loop []Vec3) bool {
	return len(loop) >= 8
}

func tryBridgeFourEdgeSeamFace(edgeParts [][]Vec3, col color.RGBA) []Triangle3D {
	if len(edgeParts) != 4 {
		return nil
	}
	// Look for pair of non-adjacent curved boundaries: (1,3) or (0,2)
	for _, pair := range [][2]int{{1, 3}, {0, 2}} {
		cA := edgeParts[pair[0]]
		cB := edgeParts[pair[1]]
		if len(cA) >= 4 && len(cB) >= 4 {
			revB := make([]Vec3, len(cB))
			for i := range cB {
				revB[i] = cB[len(cB)-1-i]
			}
			return bridgeCurvePolylines3D(cA, revB, col)
		}
	}
	return nil
}

func bridgeCurvePolylines3D(a, b []Vec3, col color.RGBA) []Triangle3D {
	if len(a) < 2 || len(b) < 2 {
		return nil
	}
	// Resample smaller curve if lengths differ, or pair by proportional index
	steps := max(len(a), len(b)) - 1
	sampleAt := func(pts []Vec3, i int) Vec3 {
		if len(pts)-1 == steps {
			return pts[i]
		}
		t := float64(i) * float64(len(pts)-1) / float64(steps)
		idx := int(t)
		if idx >= len(pts)-1 {
			return pts[len(pts)-1]
		}
		frac := t - float64(idx)
		return pts[idx].Add(pts[idx+1].Sub(pts[idx]).Scale(frac))
	}

	var tris []Triangle3D
	for i := 0; i < steps; i++ {
		a0 := sampleAt(a, i)
		a1 := sampleAt(a, i+1)
		b0 := sampleAt(b, i)
		b1 := sampleAt(b, i+1)
		n1 := a1.Sub(a0).Cross(b0.Sub(a0))
		if n1.Len() > 1e-9 {
			tris = append(tris, Triangle3D{V0: a0, V1: a1, V2: b0, Normal: n1.Norm(), Color: col})
		}
		n2 := b0.Sub(a1).Cross(b1.Sub(a1))
		if n2.Len() > 1e-9 {
			tris = append(tris, Triangle3D{V0: a1, V1: b1, V2: b0, Normal: n2.Norm(), Color: col})
		}
	}
	return tris
}

func bridgeClosedLoops3D(loopA, loopB []Vec3, col color.RGBA) []Triangle3D {
	n := len(loopA)
	m := len(loopB)
	if n < 3 || m < 3 {
		return nil
	}
	// Align loopB starting offset and orientation to minimize total seam twist
	bestOff := 0
	bestRev := false
	bestScore := 1e18
	for _, rev := range []bool{false, true} {
		for off := 0; off < m; off++ {
			score := 0.0
			for k := 0; k < min(n, 4); k++ {
				ia := (k * n) / min(n, 4)
				ib := (k * m) / min(n, 4)
				j := (off + ib) % m
				if rev {
					j = (off - ib%m + m) % m
				}
				score += loopA[ia].Sub(loopB[j]).Len()
			}
			if score < bestScore {
				bestScore = score
				bestOff = off
				bestRev = rev
			}
		}
	}

	alignedB := make([]Vec3, m+1)
	for k := 0; k < m; k++ {
		j := (bestOff + k) % m
		if bestRev {
			j = (bestOff - k + m) % m
		}
		alignedB[k] = loopB[j]
	}
	alignedB[m] = alignedB[0]

	closedA := make([]Vec3, n+1)
	copy(closedA, loopA)
	closedA[n] = loopA[0]
	return bridgeCurvePolylines3D(closedA, alignedB, col)
}

func resolveEdgeCurve(id int, entities map[int]stepEntity, ptCache map[int]Vec3, edgeCache map[int][]Vec3) []Vec3 {
	if pts, ok := edgeCache[id]; ok {
		return pts
	}
	ent, ok := entities[id]
	if !ok || ent.name != "EDGE_CURVE" || len(ent.args) < 4 {
		return nil
	}
	vStart, ok1 := resolvePoint(parseStepRef(ent.args[1]), entities, ptCache)
	vEnd, ok2 := resolvePoint(parseStepRef(ent.args[2]), entities, ptCache)
	if !ok1 || !ok2 {
		return nil
	}
	curveID := parseStepRef(ent.args[3])
	sameSense := len(ent.args) < 5 || strings.Contains(ent.args[4], ".T.")

	pts := sampleSTEPCurve(curveID, vStart, vEnd, sameSense, entities, ptCache)
	if len(pts) < 2 {
		pts = []Vec3{vStart, vEnd}
	}
	edgeCache[id] = pts
	return pts
}

func sampleSTEPCurve(curveID int, vStart, vEnd Vec3, sameSense bool, entities map[int]stepEntity, ptCache map[int]Vec3) []Vec3 {
	ent, ok := entities[curveID]
	if !ok {
		return []Vec3{vStart, vEnd}
	}
	switch ent.name {
	case "CIRCLE":
		if len(ent.args) < 3 {
			return []Vec3{vStart, vEnd}
		}
		origin, xAxis, yAxis, _, okA := resolveAxis2(parseStepRef(ent.args[1]), entities, ptCache)
		r, errR := strconv.ParseFloat(strings.TrimSpace(ent.args[2]), 64)
		if !okA || errR != nil || r <= 0 {
			return []Vec3{vStart, vEnd}
		}
		angleOf := func(p Vec3) float64 {
			d := p.Sub(origin)
			return math.Atan2(d.Dot(yAxis), d.Dot(xAxis))
		}
		a1 := angleOf(vStart)
		a2 := angleOf(vEnd)
		var sweep float64
		if vStart.Sub(vEnd).Len() < 1e-4 {
			sweep = 2 * math.Pi
			if !sameSense {
				sweep = -2 * math.Pi
			}
		} else {
			if sameSense {
				sweep = a2 - a1
				for sweep <= 0 {
					sweep += 2 * math.Pi
				}
			} else {
				sweep = a2 - a1
				for sweep >= 0 {
					sweep -= 2 * math.Pi
				}
			}
		}
		steps := max(4, int(math.Ceil(math.Abs(sweep)/(math.Pi/8))))
		pts := make([]Vec3, 0, steps+1)
		pts = append(pts, vStart)
		for i := 1; i < steps; i++ {
			a := a1 + sweep*float64(i)/float64(steps)
			p := origin.Add(xAxis.Scale(r * math.Cos(a))).Add(yAxis.Scale(r * math.Sin(a)))
			pts = append(pts, p)
		}
		pts = append(pts, vEnd)
		return pts
	case "ELLIPSE":
		if len(ent.args) < 4 {
			return []Vec3{vStart, vEnd}
		}
		origin, xAxis, yAxis, _, okA := resolveAxis2(parseStepRef(ent.args[1]), entities, ptCache)
		rx, err1 := strconv.ParseFloat(strings.TrimSpace(ent.args[2]), 64)
		ry, err2 := strconv.ParseFloat(strings.TrimSpace(ent.args[3]), 64)
		if !okA || err1 != nil || err2 != nil || rx <= 0 || ry <= 0 {
			return []Vec3{vStart, vEnd}
		}
		angleOf := func(p Vec3) float64 {
			d := p.Sub(origin)
			return math.Atan2(d.Dot(yAxis)/ry, d.Dot(xAxis)/rx)
		}
		a1 := angleOf(vStart)
		a2 := angleOf(vEnd)
		var sweep float64
		if vStart.Sub(vEnd).Len() < 1e-4 {
			sweep = 2 * math.Pi
		} else if sameSense {
			sweep = a2 - a1
			for sweep <= 0 {
				sweep += 2 * math.Pi
			}
		} else {
			sweep = a2 - a1
			for sweep >= 0 {
				sweep -= 2 * math.Pi
			}
		}
		steps := max(4, int(math.Ceil(math.Abs(sweep)/(math.Pi/8))))
		pts := make([]Vec3, 0, steps+1)
		pts = append(pts, vStart)
		for i := 1; i < steps; i++ {
			a := a1 + sweep*float64(i)/float64(steps)
			p := origin.Add(xAxis.Scale(rx * math.Cos(a))).Add(yAxis.Scale(ry * math.Sin(a)))
			pts = append(pts, p)
		}
		pts = append(pts, vEnd)
		return pts
	case "POLYLINE", "B_SPLINE_CURVE_WITH_KNOTS":
		argIdx := 1
		if ent.name == "B_SPLINE_CURVE_WITH_KNOTS" {
			argIdx = 2
		}
		if len(ent.args) > argIdx {
			pids := parseStepRefList(ent.args[argIdx])
			if len(pids) >= 2 {
				var pts []Vec3
				for _, pid := range pids {
					if v, ok := resolvePoint(pid, entities, ptCache); ok {
						pts = append(pts, v)
					}
				}
				if len(pts) >= 2 {
					// Ensure endpoints match vStart..vEnd
					if pts[0].Sub(vEnd).Len() < pts[0].Sub(vStart).Len() {
						for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
							pts[i], pts[j] = pts[j], pts[i]
						}
					}
					pts[0] = vStart
					pts[len(pts)-1] = vEnd
					return pts
				}
			}
		}
	}
	return []Vec3{vStart, vEnd}
}

func resolveSTEPColor(id int, entities map[int]stepEntity, cache map[int]color.RGBA, depth int) (color.RGBA, bool) {
	if depth > 12 || id <= 0 {
		return color.RGBA{}, false
	}
	if c, ok := cache[id]; ok {
		return c, true
	}
	ent, ok := entities[id]
	if !ok {
		return color.RGBA{}, false
	}
	if ent.name == "COLOUR_RGB" && len(ent.args) >= 4 {
		r, errR := strconv.ParseFloat(strings.TrimSpace(ent.args[1]), 64)
		g, errG := strconv.ParseFloat(strings.TrimSpace(ent.args[2]), 64)
		b, errB := strconv.ParseFloat(strings.TrimSpace(ent.args[3]), 64)
		if errR == nil && errG == nil && errB == nil {
			col := color.RGBA{
				R: uint8(clamp01(r) * 255),
				G: uint8(clamp01(g) * 255),
				B: uint8(clamp01(b) * 255),
				A: 255,
			}
			cache[id] = col
			return col, true
		}
	}
	for _, arg := range ent.args {
		if ref := parseStepRef(arg); ref > 0 {
			if col, ok := resolveSTEPColor(ref, entities, cache, depth+1); ok {
				cache[id] = col
				return col, true
			}
		}
		for _, ref := range parseStepRefList(arg) {
			if col, ok := resolveSTEPColor(ref, entities, cache, depth+1); ok {
				cache[id] = col
				return col, true
			}
		}
	}
	return color.RGBA{}, false
}

// triangulateLoop3D computes Newell face normal and ear-clipping triangulation (with fan fallback).
func triangulateLoop3D(loop []Vec3, sameSense bool, col color.RGBA) []Triangle3D {
	n := len(loop)
	if n < 3 {
		return nil
	}
	// Newell method for robust polygon normal
	var normal Vec3
	for i := 0; i < n; i++ {
		cur := loop[i]
		nxt := loop[(i+1)%n]
		normal.X += (cur.Y - nxt.Y) * (cur.Z + nxt.Z)
		normal.Y += (cur.Z - nxt.Z) * (cur.X + nxt.X)
		normal.Z += (cur.X - nxt.X) * (cur.Y + nxt.Y)
	}
	if normal.Len() < 1e-9 {
		normal = loop[1].Sub(loop[0]).Cross(loop[2].Sub(loop[0]))
	}
	normal = normal.Norm()
	if !sameSense {
		normal = normal.Scale(-1)
	}

	if n == 3 {
		return []Triangle3D{{V0: loop[0], V1: loop[1], V2: loop[2], Normal: normal, Color: col}}
	}

	// Project onto dominant 2D plane for ear clipping
	ax, ay, az := math.Abs(normal.X), math.Abs(normal.Y), math.Abs(normal.Z)
	proj := func(v Vec3) (float64, float64) {
		if az >= ax && az >= ay {
			return v.X, v.Y
		}
		if ay >= ax && ay >= az {
			return v.X, v.Z
		}
		return v.Y, v.Z
	}

	pts2 := make([][2]float64, n)
	area := 0.0
	for i := 0; i < n; i++ {
		x, y := proj(loop[i])
		pts2[i] = [2]float64{x, y}
	}
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		area += pts2[i][0]*pts2[j][1] - pts2[j][0]*pts2[i][1]
	}

	indices := make([]int, n)
	if area >= 0 {
		for i := 0; i < n; i++ {
			indices[i] = i
		}
	} else {
		for i := 0; i < n; i++ {
			indices[i] = n - 1 - i
		}
	}

	var tris []Triangle3D
	guard := n * n
	for len(indices) > 3 && guard > 0 {
		guard--
		earFound := false
		m := len(indices)
		for i := 0; i < m; i++ {
			i0 := indices[(i+m-1)%m]
			i1 := indices[i]
			i2 := indices[(i+1)%m]
			a, b, c := pts2[i0], pts2[i1], pts2[i2]
			cross := (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0])
			if cross <= 1e-9 {
				continue
			}
			contains := false
			for j := 0; j < m; j++ {
				idx := indices[j]
				if idx == i0 || idx == i1 || idx == i2 {
					continue
				}
				if pointInTri2D(pts2[idx], a, b, c) {
					contains = true
					break
				}
			}
			if contains {
				continue
			}
			tris = append(tris, Triangle3D{V0: loop[i0], V1: loop[i1], V2: loop[i2], Normal: normal, Color: col})
			indices = append(indices[:i], indices[i+1:]...)
			earFound = true
			break
		}
		if !earFound {
			break
		}
	}
	if len(indices) == 3 {
		tris = append(tris, Triangle3D{V0: loop[indices[0]], V1: loop[indices[1]], V2: loop[indices[2]], Normal: normal, Color: col})
	} else if len(tris) == 0 {
		// Fan fallback
		for i := 1; i+1 < n; i++ {
			tris = append(tris, Triangle3D{V0: loop[0], V1: loop[i], V2: loop[i+1], Normal: normal, Color: col})
		}
	}
	return tris
}

func pointInTri2D(p, a, b, c [2]float64) bool {
	s1 := (b[0]-a[0])*(p[1]-a[1]) - (b[1]-a[1])*(p[0]-a[0])
	s2 := (c[0]-b[0])*(p[1]-b[1]) - (c[1]-b[1])*(p[0]-b[0])
	s3 := (a[0]-c[0])*(p[1]-c[1]) - (a[1]-c[1])*(p[0]-c[0])
	return s1 >= -1e-9 && s2 >= -1e-9 && s3 >= -1e-9
}

// --- VRML 2.0 Parser ---

func parseVRMLMesh(name string, raw []byte) (*Mesh3D, error) {
	tokens := tokenizeVRML(string(raw))
	mesh := &Mesh3D{Name: name}
	defaultCol := color.RGBA{0x8A, 0x98, 0xAC, 0xFF}
	materialDefs := make(map[string]color.RGBA)

	type shapeScope struct {
		startDepth int
		color      color.RGBA
		points     []Vec3
		indices    []int
	}

	braceDepth := 0
	var activeShape *shapeScope
	var activeMatDef string
	matDefDepth := 0

	flushShape := func(s *shapeScope) {
		if s == nil || len(s.points) == 0 || len(s.indices) == 0 {
			return
		}
		var faceIdx []int
		emitFace := func() {
			if len(faceIdx) < 3 {
				return
			}
			var loop []Vec3
			for _, fi := range faceIdx {
				if fi >= 0 && fi < len(s.points) {
					loop = append(loop, s.points[fi])
				}
			}
			if len(loop) >= 3 {
				tris := triangulateLoop3D(loop, true, s.color)
				mesh.Triangles = append(mesh.Triangles, tris...)
				for k := 0; k < len(loop); k++ {
					mesh.Edges = append(mesh.Edges, Edge3D{
						A: loop[k], B: loop[(k+1)%len(loop)],
						Color: color.RGBA{0x1D, 0x24, 0x30, 0xFF},
					})
				}
			}
		}
		for _, idx := range s.indices {
			if idx < 0 {
				emitFace()
				faceIdx = faceIdx[:0]
			} else {
				faceIdx = append(faceIdx, idx)
			}
		}
		emitFace()
	}

	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch tok {
		case "{":
			braceDepth++
		case "}":
			if braceDepth > 0 {
				braceDepth--
			}
			if activeMatDef != "" && braceDepth < matDefDepth {
				activeMatDef = ""
			}
			if activeShape != nil && braceDepth < activeShape.startDepth {
				flushShape(activeShape)
				activeShape = nil
			}
		case "Shape":
			if i+1 < len(tokens) && tokens[i+1] == "{" {
				activeShape = &shapeScope{
					startDepth: braceDepth + 1,
					color:      defaultCol,
				}
			}
		case "DEF":
			if i+2 < len(tokens) && tokens[i+2] == "Material" {
				activeMatDef = tokens[i+1]
				matDefDepth = braceDepth + 1
				i += 2
			}
		case "USE":
			if i+1 < len(tokens) {
				if col, ok := materialDefs[tokens[i+1]]; ok && activeShape != nil {
					activeShape.color = col
				}
				i++
			}
		case "diffuseColor":
			if i+3 < len(tokens) {
				r, _ := strconv.ParseFloat(tokens[i+1], 64)
				g, _ := strconv.ParseFloat(tokens[i+2], 64)
				b, _ := strconv.ParseFloat(tokens[i+3], 64)
				col := color.RGBA{uint8(clamp01(r) * 255), uint8(clamp01(g) * 255), uint8(clamp01(b) * 255), 255}
				if activeMatDef != "" {
					materialDefs[activeMatDef] = col
				}
				if activeShape != nil {
					activeShape.color = col
				}
				i += 3
			}
		case "point":
			if i+1 < len(tokens) && tokens[i+1] == "[" {
				j := i + 2
				var coords []float64
				for j < len(tokens) && tokens[j] != "]" {
					if v, err := strconv.ParseFloat(tokens[j], 64); err == nil {
						coords = append(coords, v)
					}
					j++
				}
				var pts []Vec3
				for k := 0; k+2 < len(coords); k += 3 {
					// KiCad VRML uses 1 unit = 0.1 inch = 2.54 mm
					v := Vec3{coords[k] * 2.54, coords[k+1] * 2.54, coords[k+2] * 2.54}
					pts = append(pts, v)
					mesh.expand(v)
				}
				if activeShape != nil {
					activeShape.points = pts
				}
				i = j
			}
		case "coordIndex":
			if i+1 < len(tokens) && tokens[i+1] == "[" {
				j := i + 2
				var idxs []int
				for j < len(tokens) && tokens[j] != "]" {
					if idx, err := strconv.Atoi(tokens[j]); err == nil {
						idxs = append(idxs, idx)
					}
					j++
				}
				if activeShape != nil {
					activeShape.indices = idxs
				}
				i = j
			}
		}
	}
	if activeShape != nil {
		flushShape(activeShape)
	}

	if len(mesh.Triangles) == 0 && len(mesh.Edges) == 0 && !mesh.hasBounds {
		return nil, fmt.Errorf("no VRML geometry found in %s", name)
	}
	mesh.finalize()
	return mesh, nil
}

func tokenizeVRML(src string) []string {
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	inComment := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inComment {
			if c == '\n' {
				inComment = false
			}
			continue
		}
		if c == '#' {
			flush()
			inComment = true
			continue
		}
		switch c {
		case ' ', '\t', '\r', '\n', ',':
			flush()
		case '[', ']', '{', '}':
			flush()
			tokens = append(tokens, string(c))
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return tokens
}

// --- Software Z-Buffer 3D Renderer ---

// RenderMesh3D renders a Mesh3D into an image.RGBA using a Z-buffered shaded rasterizer.
func RenderMesh3D(mesh *Mesh3D, width, height int, cam Camera3D) *image.RGBA {
	if width <= 0 {
		width = 1
	}
	if height <= 0 {
		height = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	bg := color.RGBA{0x11, 0x16, 0x1D, 0xFF}
	fillRectSolid(img, 0, 0, width, height, bg)

	if mesh == nil {
		return img
	}

	zoom := cam.Zoom
	if zoom <= 0 {
		zoom = 1.0
	}

	center := mesh.Min.Add(mesh.Max).Scale(0.5)
	extent := mesh.Max.Sub(mesh.Min)
	radius := math.Max(extent.Len()*0.5, 0.5)
	baseScale := (math.Min(float64(width), float64(height)) * 0.40 / radius) * zoom

	yaw := cam.Yaw * math.Pi / 180.0
	pitch := cam.Pitch * math.Pi / 180.0
	cy, sy := math.Cos(yaw), math.Sin(yaw)
	cp, sp := math.Cos(pitch), math.Sin(pitch)

	cxScreen := float64(width)*0.5 + cam.PanX
	cyScreen := float64(height)*0.5 + cam.PanY

	project := func(p Vec3) (sx, syOut, depth float64) {
		v := p.Sub(center)
		// Rotate around Z (yaw), then around X (pitch) for Z-up CAD view
		x1 := v.X*cy - v.Y*sy
		y1 := v.X*sy + v.Y*cy
		z1 := v.Z

		y2 := y1*cp - z1*sp
		z2 := y1*sp + z1*cp

		// Screen X right, Screen Y up->down
		return cxScreen + x1*baseScale, cyScreen - y2*baseScale, z2
	}

	zbuf := make([]float64, width*height)
	for i := range zbuf {
		zbuf[i] = -1e18
	}

	// Ground grid on Z = mesh.Min.Z
	gridStep := math.Max(1.0, math.Pow(10, math.Floor(math.Log10(radius))))
	gridHalf := math.Ceil(radius*1.5/gridStep) * gridStep
	gridCol := color.RGBA{0x1E, 0x26, 0x33, 0xFF}
	for g := -gridHalf; g <= gridHalf+1e-6; g += gridStep {
		ax, ay, az := project(Vec3{center.X + g, center.Y - gridHalf, mesh.Min.Z})
		bx, by, bz := project(Vec3{center.X + g, center.Y + gridHalf, mesh.Min.Z})
		drawLine3DZBuf(img, zbuf, width, height, ax, ay, az-radius*2, bx, by, bz-radius*2, gridCol)

		cx, cy2, cz := project(Vec3{center.X - gridHalf, center.Y + g, mesh.Min.Z})
		dx, dy, dz := project(Vec3{center.X + gridHalf, center.Y + g, mesh.Min.Z})
		drawLine3DZBuf(img, zbuf, width, height, cx, cy2, cz-radius*2, dx, dy, dz-radius*2, gridCol)
	}

	light1 := Vec3{0.45, 0.35, 0.82}.Norm()
	light2 := Vec3{-0.55, -0.30, 0.45}.Norm()

	// Shade and rasterize triangles
	for _, tri := range mesh.Triangles {
		// Transform normal into view space for lighting
		nx1 := tri.Normal.X*cy - tri.Normal.Y*sy
		ny1 := tri.Normal.X*sy + tri.Normal.Y*cy
		nz1 := tri.Normal.Z
		ny2 := ny1*cp - nz1*sp
		nz2 := ny1*sp + nz1*cp
		vn := Vec3{nx1, ny2, nz2}.Norm()

		// Two-sided shading so single-sided shells are always well lit
		nd1 := math.Abs(vn.Dot(light1))
		nd2 := math.Abs(vn.Dot(light2))
		intensity := 0.30 + 0.58*nd1 + 0.22*nd2
		if intensity > 1.0 {
			intensity = 1.0
		}
		shaded := color.RGBA{
			R: uint8(math.Min(255, float64(tri.Color.R)*intensity)),
			G: uint8(math.Min(255, float64(tri.Color.G)*intensity)),
			B: uint8(math.Min(255, float64(tri.Color.B)*intensity)),
			A: 255,
		}

		x0, y0, z0 := project(tri.V0)
		x1, y1, z1 := project(tri.V1)
		x2, y2, z2 := project(tri.V2)
		rasterTriZBuf(img, zbuf, width, height, x0, y0, z0, x1, y1, z1, x2, y2, z2, shaded)
	}

	// Render sharp boundary edges with slight Z bias
	zBias := radius * 0.008
	for _, e := range mesh.Edges {
		ax, ay, az := project(e.A)
		bx, by, bz := project(e.B)
		drawLine3DZBuf(img, zbuf, width, height, ax, ay, az+zBias, bx, by, bz+zBias, e.Color)
	}

	// Draw XYZ axis triad in top-right corner
	drawAxisTriad3D(img, width-34, 34, yaw, pitch)

	// Draw dimensions HUD
	hud := fmt.Sprintf("%.2f x %.2f x %.2f mm  (Zoom %.0f%%)", extent.X, extent.Y, extent.Z, zoom*100)
	drawTextBitmap(img, 8, height-14, hud, 1, color.RGBA{0x94, 0xA3, 0xB8, 0xFF})

	return img
}

func rasterTriZBuf(img *image.RGBA, zbuf []float64, w, h int, x0, y0, z0, x1, y1, z1, x2, y2, z2 float64, c color.RGBA) {
	minX := max(0, int(math.Floor(math.Min(x0, math.Min(x1, x2)))))
	maxX := min(w-1, int(math.Ceil(math.Max(x0, math.Max(x1, x2)))))
	minY := max(0, int(math.Floor(math.Min(y0, math.Min(y1, y2)))))
	maxY := min(h-1, int(math.Ceil(math.Max(y0, math.Max(y1, y2)))))

	area := (x1-x0)*(y2-y0) - (y1-y0)*(x2-x0)
	if math.Abs(area) < 1e-4 {
		return
	}
	invArea := 1.0 / area

	for y := minY; y <= maxY; y++ {
		py := float64(y) + 0.5
		for x := minX; x <= maxX; x++ {
			px := float64(x) + 0.5
			w0 := ((x1-px)*(y2-py) - (y1-py)*(x2-px)) * invArea
			w1 := ((x2-px)*(y0-py) - (y2-py)*(x0-px)) * invArea
			w2 := 1.0 - w0 - w1
			if w0 < -1e-4 || w1 < -1e-4 || w2 < -1e-4 {
				continue
			}
			z := w0*z0 + w1*z1 + w2*z2
			idx := y*w + x
			if z > zbuf[idx] {
				zbuf[idx] = z
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func drawLine3DZBuf(img *image.RGBA, zbuf []float64, w, h int, x0, y0, z0, x1, y1, z1 float64, c color.RGBA) {
	dx := x1 - x0
	dy := y1 - y0
	steps := int(math.Ceil(math.Max(math.Abs(dx), math.Abs(dy))))
	if steps == 0 {
		steps = 1
	}
	if steps > 4000 {
		steps = 4000
	}
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		px := int(math.Round(x0 + dx*t))
		py := int(math.Round(y0 + dy*t))
		if px < 0 || py < 0 || px >= w || py >= h {
			continue
		}
		pz := z0 + (z1-z0)*t
		idx := py*w + px
		if pz >= zbuf[idx] {
			zbuf[idx] = pz
			img.SetRGBA(px, py, c)
		}
	}
}

func drawAxisTriad3D(img *image.RGBA, cx, cy int, yaw, pitch float64) {
	cyA, syA := math.Cos(yaw), math.Sin(yaw)
	cpA, spA := math.Cos(pitch), math.Sin(pitch)
	projAxis := func(v Vec3) (float64, float64) {
		x1 := v.X*cyA - v.Y*syA
		y1 := v.X*syA + v.Y*cyA
		z1 := v.Z
		y2 := y1*cpA - z1*spA
		return float64(cx) + x1*18, float64(cy) - y2*18
	}
	axes := []struct {
		v   Vec3
		lbl string
		col color.RGBA
	}{
		{Vec3{1, 0, 0}, "X", color.RGBA{0xEF, 0x44, 0x44, 0xFF}},
		{Vec3{0, 1, 0}, "Y", color.RGBA{0x22, 0xC5, 0x5E, 0xFF}},
		{Vec3{0, 0, 1}, "Z", color.RGBA{0x3B, 0x82, 0xF6, 0xFF}},
	}
	for _, ax := range axes {
		ex, ey := projAxis(ax.v)
		drawThickLine(img, float64(cx), float64(cy), ex, ey, 1.5, ax.col)
		drawTextCentered(img, int(ex), int(ey), ax.lbl, 1, ax.col)
	}
}
