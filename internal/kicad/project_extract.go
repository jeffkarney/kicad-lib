package kicad

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type placedInstanceProps struct {
	footprint   string
	datasheet   string
	description string
	keywords    string
	value       string
	custom      map[string]string
}

// ExtractFromSchematic parses a KiCad `.kicad_sch` schematic file, extracts embedded symbols from
// `(lib_symbols ...)`, normalizes `<Lib>:` prefixes, and enriches each symbol with properties
// (Footprint, Datasheet, Description, MPN, Manufacturer, LCSC, etc.) from placed component instances.
// Power/flag symbols are skipped unless the schematic contains no regular components.
func ExtractFromSchematic(data []byte) ([]ParsedSymbol, error) {
	root, err := ParseSExpr(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse schematic: %w", err)
	}
	if root.Tag() != "kicad_sch" {
		return nil, fmt.Errorf("expected kicad_sch root tag, got %q", root.Tag())
	}

	libSyms := root.FindChild("lib_symbols")
	if libSyms == nil {
		return nil, nil
	}

	// Collect properties from placed `(symbol (lib_id ...) ...)` instances
	instByLibID := make(map[string]*placedInstanceProps)
	for _, inst := range root.FindChildren("symbol") {
		libIDNode := inst.FindChild("lib_id")
		if libIDNode == nil {
			continue
		}
		libID := strings.TrimSpace(libIDNode.Arg(1))
		if libID == "" {
			continue
		}
		ref := strings.TrimSpace(inst.GetPropertyValue("Reference"))
		if strings.HasPrefix(ref, "#PWR") || strings.HasPrefix(ref, "#FLG") {
			continue
		}

		acc, ok := instByLibID[libID]
		if !ok {
			acc = &placedInstanceProps{custom: make(map[string]string)}
			instByLibID[libID] = acc
		}
		for _, prop := range inst.FindChildren("property") {
			if len(prop.Children) < 3 {
				continue
			}
			k := strings.TrimSpace(prop.Children[1].Value)
			v := strings.TrimSpace(prop.Children[2].Value)
			if k == "" || v == "" {
				continue
			}
			switch k {
			case "Reference", "ki_fp_filters":
				continue
			case "Footprint":
				if acc.footprint == "" {
					acc.footprint = v
				}
			case "Datasheet":
				if acc.datasheet == "" && v != "~" {
					acc.datasheet = v
				}
			case "Description", "ki_description":
				if acc.description == "" || k == "Description" {
					acc.description = v
				}
			case "ki_keywords":
				if acc.keywords == "" {
					acc.keywords = v
				}
			case "Value":
				if acc.value == "" {
					acc.value = v
				}
			default:
				if acc.custom[k] == "" {
					acc.custom[k] = v
				}
			}
		}
	}

	var regular []ParsedSymbol
	var power []ParsedSymbol
	seenRegular := make(map[string]bool)
	seenPower := make(map[string]bool)

	for _, symNode := range libSyms.FindChildren("symbol") {
		fullID := strings.TrimSpace(symNode.Arg(1))
		if fullID == "" {
			continue
		}
		isPower := symNode.FindChild("power") != nil || strings.HasPrefix(strings.ToLower(fullID), "power:")

		cleanName := fullID
		if idx := strings.LastIndex(cleanName, ":"); idx >= 0 {
			cleanName = cleanName[idx+1:]
		}
		cleanName = strings.TrimSpace(cleanName)
		if cleanName == "" {
			cleanName = fullID
		}

		clone := symNode.Clone()
		if len(clone.Children) > 1 {
			clone.Children[1] = QString(cleanName)
		}
		if fullID != cleanName {
			renameSubSymbols(clone, fullID, cleanName)
		}

		// Merge enriched metadata from placed instances if missing/empty on the lib_symbols definition
		if acc, ok := instByLibID[fullID]; ok {
			if strings.TrimSpace(clone.GetPropertyValue("Footprint")) == "" && acc.footprint != "" {
				clone.SetOrAddProperty("Footprint", acc.footprint)
			}
			curDS := strings.TrimSpace(clone.GetPropertyValue("Datasheet"))
			if (curDS == "" || curDS == "~") && acc.datasheet != "" {
				clone.SetOrAddProperty("Datasheet", acc.datasheet)
			}
			if strings.TrimSpace(clone.GetPropertyValue("Description")) == "" && acc.description != "" {
				clone.SetOrAddProperty("Description", acc.description)
			}
			if strings.TrimSpace(clone.GetPropertyValue("ki_keywords")) == "" && acc.keywords != "" {
				clone.SetOrAddProperty("ki_keywords", acc.keywords)
			}
			if strings.TrimSpace(clone.GetPropertyValue("Value")) == "" && acc.value != "" {
				clone.SetOrAddProperty("Value", acc.value)
			}
			for _, k := range sortedKeys(acc.custom) {
				if strings.TrimSpace(clone.GetPropertyValue(k)) == "" {
					clone.SetOrAddProperty(k, acc.custom[k])
				}
			}
		}

		ps := extractParsedSymbol(clone)
		if ps.Datasheet == "~" {
			ps.Datasheet = ""
		}

		key := strings.ToLower(ps.Name)
		if isPower {
			if !seenPower[key] {
				seenPower[key] = true
				power = append(power, ps)
			}
		} else {
			if !seenRegular[key] {
				seenRegular[key] = true
				regular = append(regular, ps)
			}
		}
	}

	if len(regular) > 0 {
		return regular, nil
	}
	return power, nil
}

// ExtractSchematicSheetFiles returns relative sub-sheet filenames referenced by hierarchical `(sheet ...)`
// nodes inside a `.kicad_sch` schematic.
func ExtractSchematicSheetFiles(data []byte) []string {
	root, err := ParseSExpr(string(data))
	if err != nil || root.Tag() != "kicad_sch" {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, sheet := range root.FindChildren("sheet") {
		sf := strings.TrimSpace(sheet.GetPropertyValue("Sheetfile"))
		if sf == "" {
			if f := sheet.FindChild("file"); f != nil {
				sf = strings.TrimSpace(f.Arg(1))
			}
		}
		if sf != "" && !seen[sf] {
			seen[sf] = true
			out = append(out, sf)
		}
	}
	return out
}

// ExtractFromPCB parses a KiCad `.kicad_pcb` board file, extracts each unique embedded footprint,
// strips board placement / net / instance attributes, and returns clean standalone `.kicad_mod` models.
func ExtractFromPCB(data []byte) ([]ParsedFootprint, error) {
	root, err := ParseSExpr(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse pcb: %w", err)
	}
	if root.Tag() != "kicad_pcb" {
		return nil, fmt.Errorf("expected kicad_pcb root tag, got %q", root.Tag())
	}

	fpNodes := append(root.FindChildren("footprint"), root.FindChildren("module")...)
	if len(fpNodes) == 0 {
		return nil, nil
	}

	type scoredNode struct {
		node  *Node
		name  string
		score int
	}
	bestByName := make(map[string]scoredNode)
	var order []string

	for _, fn := range fpNodes {
		cleanName := cleanFootprintName(fn)
		if cleanName == "" {
			continue
		}
		key := strings.ToLower(cleanName)

		// Score preference: front layer (+10), zero rotation (+5)
		score := 0
		if layerNode := fn.FindChild("layer"); layerNode == nil || !strings.EqualFold(layerNode.Arg(1), "B.Cu") {
			score += 10
		}
		if math.Abs(readNodeAtAngle(fn)) < 1e-6 {
			score += 5
		}

		if cur, ok := bestByName[key]; !ok {
			bestByName[key] = scoredNode{node: fn, name: cleanName, score: score}
			order = append(order, key)
		} else if score > cur.score {
			bestByName[key] = scoredNode{node: fn, name: cleanName, score: score}
		}
	}

	out := make([]ParsedFootprint, 0, len(order))
	for _, key := range order {
		item := bestByName[key]
		norm := normalizePCBFootprintNode(item.node, item.name)
		raw := norm.PrettyString(0) + "\n"
		pf, err := ParseFootprintFile([]byte(raw))
		if err != nil {
			continue
		}
		if pf.Name == "" {
			pf.Name = item.name
		}
		out = append(out, *pf)
	}
	return out, nil
}

func cleanFootprintName(fn *Node) string {
	rawName := strings.TrimSpace(fn.Arg(1))
	if idx := strings.LastIndex(rawName, ":"); idx >= 0 {
		rawName = rawName[idx+1:]
	}
	rawName = strings.TrimSpace(rawName)
	if rawName == "" {
		rawName = strings.TrimSpace(fn.GetPropertyValue("Value"))
	}
	return rawName
}

func readNodeAtAngle(n *Node) float64 {
	atNode := n.FindChild("at")
	if atNode == nil || len(atNode.Children) < 4 {
		return 0
	}
	v, err := strconv.ParseFloat(atNode.Arg(3), 64)
	if err != nil {
		return 0
	}
	return v
}

func normalizePCBFootprintNode(src *Node, cleanName string) *Node {
	clone := src.Clone()
	if len(clone.Children) > 0 && clone.Children[0].IsAtom {
		clone.Children[0] = Atom("footprint")
	}
	if len(clone.Children) > 1 {
		clone.Children[1] = QString(cleanName)
	}

	fpRot := readNodeAtAngle(clone)
	isBack := false
	if layerNode := clone.FindChild("layer"); layerNode != nil && strings.EqualFold(layerNode.Arg(1), "B.Cu") {
		isBack = true
	}

	// Remove board-instance-only root children
	for _, tag := range []string{"at", "locked", "uuid", "tstamp", "path", "sheetname", "sheetfile"} {
		clone.RemoveChildren(tag)
	}

	// Normalize Reference / Value properties and fp_text nodes
	if refProp := clone.FindProperty("Reference"); refProp != nil && len(refProp.Children) >= 3 {
		refProp.Children[2] = QString("REF**")
	}
	if valProp := clone.FindProperty("Value"); valProp != nil && len(valProp.Children) >= 3 {
		valProp.Children[2] = QString(cleanName)
	}

	for _, ch := range clone.Children {
		if ch.IsAtom {
			continue
		}
		stripUUIDsRecursive(ch)
		if isBack {
			normalizeBackLayerNode(ch)
		}

		switch ch.Tag() {
		case "pad":
			for _, drop := range []string{"net", "pinfunction", "pintype", "uuid", "tstamp"} {
				ch.RemoveChildren(drop)
			}
			if fpRot != 0 {
				adjustChildAtAngle(ch, fpRot, true)
			}
		case "property":
			if fpRot != 0 {
				adjustChildAtAngle(ch, fpRot, false)
			}
		case "fp_text":
			if len(ch.Children) >= 3 {
				kind := ch.Arg(1)
				if kind == "reference" {
					ch.Children[2] = QString("REF**")
				} else if kind == "value" {
					ch.Children[2] = QString(cleanName)
				}
			}
			if fpRot != 0 {
				adjustChildAtAngle(ch, fpRot, false)
			}
		}
	}

	return clone
}

func stripUUIDsRecursive(n *Node) {
	if n == nil || n.IsAtom {
		return
	}
	n.RemoveChildren("uuid")
	n.RemoveChildren("tstamp")
	for _, ch := range n.Children {
		stripUUIDsRecursive(ch)
	}
}

func normalizeBackLayerNode(n *Node) {
	if n == nil || n.IsAtom {
		return
	}
	switch n.Tag() {
	case "layer":
		if len(n.Children) >= 2 && n.Children[1].IsAtom {
			n.Children[1].Value = flipLayerName(n.Children[1].Value)
		}
	case "layers":
		for i := 1; i < len(n.Children); i++ {
			if n.Children[i].IsAtom {
				n.Children[i].Value = flipLayerName(n.Children[i].Value)
			}
		}
	case "justify":
		kept := n.Children[:0]
		for _, c := range n.Children {
			if c.IsAtom && strings.EqualFold(c.Value, "mirror") {
				continue
			}
			kept = append(kept, c)
		}
		n.Children = kept
	}
	for _, ch := range n.Children {
		normalizeBackLayerNode(ch)
	}
	// Drop empty (justify) list if mirror was the only argument
	kept := n.Children[:0]
	for _, ch := range n.Children {
		if !ch.IsAtom && ch.Tag() == "justify" && len(ch.Children) <= 1 {
			continue
		}
		kept = append(kept, ch)
	}
	n.Children = kept
}

func flipLayerName(layer string) string {
	switch {
	case strings.HasPrefix(layer, "B."):
		return "F." + strings.TrimPrefix(layer, "B.")
	case strings.HasPrefix(layer, "F."):
		return "B." + strings.TrimPrefix(layer, "F.")
	default:
		return layer
	}
}

func adjustChildAtAngle(n *Node, deltaDeg float64, omitZero bool) {
	atNode := n.FindChild("at")
	if atNode == nil || len(atNode.Children) < 4 {
		return
	}
	curAngle, err := strconv.ParseFloat(atNode.Arg(3), 64)
	if err != nil {
		return
	}
	next := math.Mod(curAngle-deltaDeg, 360)
	if next < 0 {
		next += 360
	}
	if math.Abs(next) < 1e-6 || math.Abs(next-360) < 1e-6 {
		if omitZero {
			atNode.Children = atNode.Children[:3]
			return
		}
		atNode.Children[3] = Atom("0")
		return
	}
	atNode.Children[3] = Atom(fmtFloat(next))
}
