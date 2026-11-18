package kicad

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ParsedModel3D describes a `(model ...)` node inside a KiCad footprint.
type ParsedModel3D struct {
	Path    string
	OffsetX float64
	OffsetY float64
	OffsetZ float64
	ScaleX  float64
	ScaleY  float64
	ScaleZ  float64
	RotateX float64
	RotateY float64
	RotateZ float64
}

// ParsedFootprint holds metadata and raw S-expression of a `.kicad_mod` file.
type ParsedFootprint struct {
	Name     string
	PadCount int
	Models   []ParsedModel3D
	RawSExpr string
}

// ParseFootprintFile parses a `.kicad_mod` file (either `(footprint ...)` or legacy `(module ...)`).
func ParseFootprintFile(data []byte) (*ParsedFootprint, error) {
	node, err := ParseSExpr(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse footprint: %w", err)
	}
	tag := node.Tag()
	if tag != "footprint" && tag != "module" {
		return nil, fmt.Errorf("expected footprint/module root tag, got %q", tag)
	}

	pf := &ParsedFootprint{
		Name:     node.Arg(1),
		PadCount: len(node.FindChildren("pad")),
		RawSExpr: node.PrettyString(0),
	}

	for _, mn := range node.FindChildren("model") {
		pm := ParsedModel3D{
			Path:   mn.Arg(1),
			ScaleX: 1,
			ScaleY: 1,
			ScaleZ: 1,
		}
		if off := mn.FindChild("offset"); off != nil {
			if xyz := off.FindChild("xyz"); xyz != nil && len(xyz.Children) >= 4 {
				_, _ = fmt.Sscanf(xyz.Arg(1)+" "+xyz.Arg(2)+" "+xyz.Arg(3), "%f %f %f", &pm.OffsetX, &pm.OffsetY, &pm.OffsetZ)
			}
		}
		if sc := mn.FindChild("scale"); sc != nil {
			if xyz := sc.FindChild("xyz"); xyz != nil && len(xyz.Children) >= 4 {
				_, _ = fmt.Sscanf(xyz.Arg(1)+" "+xyz.Arg(2)+" "+xyz.Arg(3), "%f %f %f", &pm.ScaleX, &pm.ScaleY, &pm.ScaleZ)
			}
		}
		if rot := mn.FindChild("rotate"); rot != nil {
			if xyz := rot.FindChild("xyz"); xyz != nil && len(xyz.Children) >= 4 {
				_, _ = fmt.Sscanf(xyz.Arg(1)+" "+xyz.Arg(2)+" "+xyz.Arg(3), "%f %f %f", &pm.RotateX, &pm.RotateY, &pm.RotateZ)
			}
		}
		pf.Models = append(pf.Models, pm)
	}

	return pf, nil
}

// ModelTransform describes a 3D model path plus optional transform offsets/scale/rotation.
type ModelTransform struct {
	Path    string
	OffsetX float64
	OffsetY float64
	OffsetZ float64
	ScaleX  float64
	ScaleY  float64
	ScaleZ  float64
	RotateX float64
	RotateY float64
	RotateZ float64
}

// RewriteFootprint updates the footprint name and replaces `(model ...)` paths with managed 3D model paths.
// If existing `(model ...)` nodes match by base filename and transform values are zero/default, existing transforms are preserved.
func RewriteFootprint(rawFootprintSExpr, footprintName string, models []ModelTransform) (string, error) {
	node, err := ParseSExpr(rawFootprintSExpr)
	if err != nil {
		return "", fmt.Errorf("parse footprint sexpr: %w", err)
	}
	tag := node.Tag()
	if tag != "footprint" && tag != "module" {
		return "", fmt.Errorf("expected footprint/module root tag, got %q", tag)
	}

	if footprintName != "" && len(node.Children) > 1 {
		node.Children[1] = QString(footprintName)
	}

	// Capture existing model transforms by base filename before removing old model entries.
	existingByBase := make(map[string]*Node)
	for _, mn := range node.FindChildren("model") {
		base := strings.ToLower(filepath.Base(mn.Arg(1)))
		if base != "" {
			existingByBase[base] = mn.Clone()
		}
	}

	node.RemoveChildren("model")

	for _, m := range models {
		if strings.TrimSpace(m.Path) == "" {
			continue
		}
		sx, sy, sz := m.ScaleX, m.ScaleY, m.ScaleZ
		if sx == 0 && sy == 0 && sz == 0 {
			sx, sy, sz = 1, 1, 1
		}

		// Preserve original offset/scale/rotate if caller left transforms at default and filename matches
		base := strings.ToLower(filepath.Base(m.Path))
		if prev, ok := existingByBase[base]; ok &&
			m.OffsetX == 0 && m.OffsetY == 0 && m.OffsetZ == 0 &&
			m.RotateX == 0 && m.RotateY == 0 && m.RotateZ == 0 &&
			sx == 1 && sy == 1 && sz == 1 {
			if len(prev.Children) > 1 {
				prev.Children[1] = QString(m.Path)
			}
			node.Children = append(node.Children, prev)
			continue
		}

		modelNode := List(
			Atom("model"),
			QString(m.Path),
			List(Atom("offset"), List(Atom("xyz"), Atom(fmtFloat(m.OffsetX)), Atom(fmtFloat(m.OffsetY)), Atom(fmtFloat(m.OffsetZ)))),
			List(Atom("scale"), List(Atom("xyz"), Atom(fmtFloat(sx)), Atom(fmtFloat(sy)), Atom(fmtFloat(sz)))),
			List(Atom("rotate"), List(Atom("xyz"), Atom(fmtFloat(m.RotateX)), Atom(fmtFloat(m.RotateY)), Atom(fmtFloat(m.RotateZ)))),
		)
		node.Children = append(node.Children, modelNode)
	}

	return node.PrettyString(0) + "\n", nil
}

func fmtFloat(v float64) string {
	s := fmt.Sprintf("%.4f", v)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}
