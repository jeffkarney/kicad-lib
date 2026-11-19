package kicad

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ParsedSymbol holds metadata and the standalone `(symbol ...)` S-expression extracted from a .kicad_sym file.
type ParsedSymbol struct {
	Name        string
	Value       string
	Footprint   string
	Datasheet   string
	Description string
	Keywords    string
	PinCount    int
	Properties  map[string]string
	RawSExpr    string
}

// ParseSymbolLibrary parses a .kicad_sym file (or standalone `(symbol ...)` node) and returns all top-level symbols.
func ParseSymbolLibrary(data []byte) ([]ParsedSymbol, error) {
	root, err := ParseSExpr(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse symbol library: %w", err)
	}

	var symNodes []*Node
	switch root.Tag() {
	case "kicad_symbol_lib":
		symNodes = root.FindChildren("symbol")
	case "symbol":
		symNodes = []*Node{root}
	default:
		return nil, fmt.Errorf("unexpected root tag %q in symbol file", root.Tag())
	}

	out := make([]ParsedSymbol, 0, len(symNodes))
	for _, sn := range symNodes {
		ps := extractParsedSymbol(sn)
		out = append(out, ps)
	}
	return out, nil
}

func extractParsedSymbol(sn *Node) ParsedSymbol {
	name := sn.Arg(1)
	ps := ParsedSymbol{
		Name:       name,
		Properties: make(map[string]string),
		RawSExpr:   sn.PrettyString(1),
		PinCount:   countPinsRecursive(sn),
	}

	for _, prop := range sn.FindChildren("property") {
		if len(prop.Children) < 3 {
			continue
		}
		k := prop.Children[1].Value
		v := prop.Children[2].Value
		switch k {
		case "Value":
			ps.Value = v
		case "Footprint":
			ps.Footprint = v
		case "Datasheet":
			ps.Datasheet = v
		case "Description", "ki_description":
			if ps.Description == "" || k == "Description" {
				ps.Description = v
			}
		case "ki_keywords":
			ps.Keywords = v
		default:
			if k != "Reference" && k != "ki_fp_filters" && k != "" {
				ps.Properties[k] = v
			}
		}
	}
	if ps.Value == "" {
		ps.Value = name
	}
	return ps
}

func countPinsRecursive(n *Node) int {
	if n == nil || n.IsAtom {
		return 0
	}
	count := 0
	if n.Tag() == "pin" {
		count++
	}
	for _, ch := range n.Children {
		count += countPinsRecursive(ch)
	}
	return count
}

// PrepareSymbolNode updates a symbol S-expression with target name, linked footprint (`Lib:Footprint`),
// standard properties, and custom searchable metadata properties.
func PrepareSymbolNode(rawSymbolSExpr, partName, footprintRef, value, datasheet, description, keywords string, metadata map[string]string) (string, error) {
	node, err := ParseSExpr(rawSymbolSExpr)
	if err != nil {
		return "", fmt.Errorf("parse symbol sexpr: %w", err)
	}
	if node.Tag() == "kicad_symbol_lib" {
		syms := node.FindChildren("symbol")
		if len(syms) == 0 {
			return "", fmt.Errorf("kicad_symbol_lib contains no symbol nodes")
		}
		node = syms[0]
	}
	if node.Tag() != "symbol" {
		return "", fmt.Errorf("expected symbol node, got %q", node.Tag())
	}

	oldName := node.Arg(1)
	if partName != "" && len(node.Children) > 1 {
		node.Children[1] = QString(partName)
		if oldName != "" && oldName != partName {
			renameSubSymbols(node, oldName, partName)
		}
	}

	if value != "" {
		node.SetOrAddProperty("Value", value)
	}
	if footprintRef != "" {
		node.SetOrAddProperty("Footprint", footprintRef)
	}
	if datasheet != "" {
		node.SetOrAddProperty("Datasheet", datasheet)
	}
	if description != "" {
		node.SetOrAddProperty("Description", description)
	}
	if keywords != "" {
		node.SetOrAddProperty("ki_keywords", keywords)
	}

	// Deterministic sorted insertion for custom metadata properties
	if len(metadata) > 0 {
		keys := sortedKeys(metadata)
		for _, k := range keys {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			node.SetOrAddProperty(k, metadata[k])
		}
	}

	return node.PrettyString(1), nil
}

func renameSubSymbols(parent *Node, oldPrefix, newPrefix string) {
	for _, ch := range parent.Children {
		if ch.Tag() == "symbol" && len(ch.Children) > 1 && ch.Children[1].IsAtom {
			subName := ch.Children[1].Value
			if strings.HasPrefix(subName, oldPrefix+"_") {
				suffix := strings.TrimPrefix(subName, oldPrefix)
				ch.Children[1] = QString(newPrefix + suffix)
			}
		}
	}
}

// BuildSymbolLibraryFile wraps standalone `(symbol ...)` S-expressions in a valid KiCad `.kicad_sym` document.
func BuildSymbolLibraryFile(symbols []string) string {
	var sb strings.Builder
	sb.WriteString("(kicad_symbol_lib\n")
	sb.WriteString("\t(version 20231120)\n")
	sb.WriteString("\t(generator \"kicad_lib_manager\")\n")
	sb.WriteString("\t(generator_version \"1.0\")\n")
	for _, raw := range symbols {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		// Ensure top-level symbol starts indented by 1 tab
		lines := strings.Split(trimmed, "\n")
		for idx, ln := range lines {
			if idx == 0 && !strings.HasPrefix(ln, "\t") {
				sb.WriteByte('\t')
			}
			sb.WriteString(ln)
			sb.WriteByte('\n')
		}
	}
	sb.WriteString(")\n")
	return sb.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort to avoid extra import overhead
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// ParseSymbolLibraryFast scans a `.kicad_sym` buffer by tracking parenthesis depth and quoted strings
// without constructing a full S-expression AST, making it fast for libraries with thousands of symbols.
func ParseSymbolLibraryFast(data []byte) ([]ParsedSymbol, error) {
	n := len(data)
	depth := 0
	inStr := false
	esc := false
	inComment := false

	topSymDepth := 2
	rootDetected := false

	var out []ParsedSymbol
	inTopSym := false
	symStart := 0
	var cur ParsedSymbol

	for i := 0; i < n; i++ {
		b := data[i]
		if inComment {
			if b == '\n' {
				inComment = false
			}
			continue
		}
		if inStr {
			if esc {
				esc = false
				continue
			}
			if b == '\\' {
				esc = true
				continue
			}
			if b == '"' {
				inStr = false
			}
			continue
		}
		if b == ';' {
			inComment = true
			continue
		}
		if b == '"' {
			inStr = true
			continue
		}

		if b == '(' {
			depth++
			tag, afterTag := readTagFast(data, i+1)
			if !rootDetected && depth == 1 {
				rootDetected = true
				if tag == "symbol" {
					topSymDepth = 1
				} else if tag == "kicad_symbol_lib" {
					topSymDepth = 2
				} else {
					return nil, fmt.Errorf("unexpected root tag %q in symbol file", tag)
				}
			}

			if !inTopSym && depth == topSymDepth && tag == "symbol" {
				symName, _ := readArgFast(data, afterTag)
				inTopSym = true
				symStart = i
				cur = ParsedSymbol{
					Name:       symName,
					Properties: make(map[string]string),
				}
			} else if inTopSym {
				if depth == topSymDepth+1 && tag == "property" {
					k, nextPos := readArgFast(data, afterTag)
					v, _ := readArgFast(data, nextPos)
					switch k {
					case "Value":
						cur.Value = v
					case "Footprint":
						cur.Footprint = v
					case "Datasheet":
						cur.Datasheet = v
					case "Description", "ki_description":
						if cur.Description == "" || k == "Description" {
							cur.Description = v
						}
					case "ki_keywords":
						cur.Keywords = v
					default:
						if k != "Reference" && k != "ki_fp_filters" && k != "" {
							cur.Properties[k] = v
						}
					}
				} else if depth >= topSymDepth+1 && tag == "pin" {
					cur.PinCount++
				}
			}
			continue
		}

		if b == ')' {
			if inTopSym && depth == topSymDepth {
				cur.RawSExpr = string(data[symStart : i+1])
				if cur.Value == "" {
					cur.Value = cur.Name
				}
				out = append(out, cur)
				inTopSym = false
			}
			if depth > 0 {
				depth--
			}
		}
	}

	if !rootDetected {
		return nil, fmt.Errorf("empty symbol file")
	}
	return out, nil
}

// ExtractSymbolSExprFromFile reads symLibPath (which can be a .kicad_sym file or a symbol directory)
// and extracts the standalone `(symbol "<symbolName>" ...)` S-expression.
func ExtractSymbolSExprFromFile(symLibPath, symbolName string) (string, error) {
	info, err := os.Stat(symLibPath)
	if err != nil {
		return "", err
	}

	if info.IsDir() {
		// First check for direct <symbolName>.kicad_sym match (standard in .kicad_symdir)
		direct := filepath.Join(symLibPath, symbolName+".kicad_sym")
		if dInfo, err := os.Stat(direct); err == nil && !dInfo.IsDir() {
			if s, err := extractSymbolFromSingleFile(direct, symbolName); err == nil {
				return s, nil
			}
		}
		// Search all .kicad_sym files in the directory
		entries, err := os.ReadDir(symLibPath)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".kicad_sym") {
				continue
			}
			fpath := filepath.Join(symLibPath, e.Name())
			if s, err := extractSymbolFromSingleFile(fpath, symbolName); err == nil {
				return s, nil
			}
		}
		return "", fmt.Errorf("symbol %q not found in directory %s", symbolName, symLibPath)
	}

	// symLibPath is a file
	s, err := extractSymbolFromSingleFile(symLibPath, symbolName)
	if err == nil {
		return s, nil
	}

	// If not found in the file, check if a sibling file in the same directory contains it
	// (handles extends references in .kicad_symdir where each symbol has its own file)
	dir := filepath.Dir(symLibPath)
	sibling := filepath.Join(dir, symbolName+".kicad_sym")
	if sibling != symLibPath {
		if sInfo, sErr := os.Stat(sibling); sErr == nil && !sInfo.IsDir() {
			if s, err := extractSymbolFromSingleFile(sibling, symbolName); err == nil {
				return s, nil
			}
		}
	}

	return "", err
}

func extractSymbolFromSingleFile(filePath, symbolName string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	syms, err := ParseSymbolLibraryFast(data)
	if err != nil {
		return "", err
	}
	for _, s := range syms {
		if s.Name == symbolName {
			return s.RawSExpr, nil
		}
	}
	return "", fmt.Errorf("symbol %q not found in %s", symbolName, filePath)
}

func readTagFast(data []byte, pos int) (string, int) {
	n := len(data)
	for pos < n && isSpaceByte(data[pos]) {
		pos++
	}
	start := pos
	for pos < n {
		c := data[pos]
		if isSpaceByte(c) || c == '(' || c == ')' || c == '"' {
			break
		}
		pos++
	}
	return string(data[start:pos]), pos
}

func readArgFast(data []byte, pos int) (string, int) {
	n := len(data)
	for pos < n && isSpaceByte(data[pos]) {
		pos++
	}
	if pos >= n {
		return "", pos
	}
	if data[pos] == '"' {
		pos++
		var sb strings.Builder
		for pos < n {
			c := data[pos]
			if c == '"' {
				return sb.String(), pos + 1
			}
			if c == '\\' && pos+1 < n {
				pos++
				switch data[pos] {
				case 'n':
					sb.WriteByte('\n')
				case 'r':
					sb.WriteByte('\r')
				case 't':
					sb.WriteByte('\t')
				default:
					sb.WriteByte(data[pos])
				}
				pos++
				continue
			}
			sb.WriteByte(c)
			pos++
		}
		return sb.String(), pos
	}
	start := pos
	for pos < n {
		c := data[pos]
		if isSpaceByte(c) || c == '(' || c == ')' || c == '"' {
			break
		}
		pos++
	}
	return string(data[start:pos]), pos
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
