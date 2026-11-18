package kicad

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// LibTableEntry represents a parsed entry from KiCad's sym-lib-table or fp-lib-table.
type LibTableEntry struct {
	Name         string
	Type         string
	URI          string
	Options      string
	Description  string
	ResolvedPath string
}

// RegisterSymbolLibrary adds or updates a KiCad symbol library entry in <kicadConfigDir>/sym-lib-table.
func RegisterSymbolLibrary(kicadConfigDir, libName, libFilePath, description string) error {
	return registerLibTableEntry(filepath.Join(kicadConfigDir, "sym-lib-table"), "sym_lib_table", libName, libFilePath, description)
}

// RegisterFootprintLibrary adds or updates a KiCad footprint library entry in <kicadConfigDir>/fp-lib-table.
func RegisterFootprintLibrary(kicadConfigDir, libName, prettyDirPath, description string) error {
	return registerLibTableEntry(filepath.Join(kicadConfigDir, "fp-lib-table"), "fp_lib_table", libName, prettyDirPath, description)
}

// UnregisterLibrary removes a library entry from both sym-lib-table and fp-lib-table if present.
func UnregisterLibrary(kicadConfigDir, libName string) error {
	if err := unregisterLibTableEntry(filepath.Join(kicadConfigDir, "sym-lib-table"), "sym_lib_table", libName); err != nil {
		return err
	}
	return unregisterLibTableEntry(filepath.Join(kicadConfigDir, "fp-lib-table"), "fp_lib_table", libName)
}

func registerLibTableEntry(tablePath, rootTag, libName, uri, description string) error {
	libName = strings.TrimSpace(libName)
	if libName == "" {
		return errors.New("library name cannot be empty")
	}
	if err := os.MkdirAll(filepath.Dir(tablePath), 0o755); err != nil {
		return fmt.Errorf("create kicad config dir: %w", err)
	}

	root, existed, rawOld, err := loadOrInitLibTable(tablePath, rootTag)
	if err != nil {
		return err
	}

	if existed {
		if err := os.WriteFile(tablePath+".bak", rawOld, 0o644); err != nil {
			return fmt.Errorf("backup %s: %w", tablePath, err)
		}
	}

	updated := false
	for _, libNode := range root.FindChildren("lib") {
		nameNode := libNode.FindChild("name")
		if nameNode != nil && nameNode.Arg(1) == libName {
			setLibField(libNode, "type", "KiCad")
			setLibField(libNode, "uri", uri)
			setLibField(libNode, "descr", description)
			updated = true
			break
		}
	}

	if !updated {
		entry := List(
			Atom("lib"),
			List(Atom("name"), QString(libName)),
			List(Atom("type"), QString("KiCad")),
			List(Atom("uri"), QString(uri)),
			List(Atom("options"), QString("")),
			List(Atom("descr"), QString(description)),
		)
		root.Children = append(root.Children, entry)
	}

	return writeAtomic(tablePath, []byte(formatLibTable(root)))
}

func unregisterLibTableEntry(tablePath, rootTag, libName string) error {
	data, err := os.ReadFile(tablePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	root, err := ParseSExpr(string(data))
	if err != nil {
		return err
	}
	if root.Tag() != rootTag {
		return fmt.Errorf("unexpected root tag %q in %s", root.Tag(), tablePath)
	}

	kept := make([]*Node, 0, len(root.Children))
	removed := false
	for _, ch := range root.Children {
		if ch.Tag() == "lib" {
			nameNode := ch.FindChild("name")
			if nameNode != nil && nameNode.Arg(1) == libName {
				removed = true
				continue
			}
		}
		kept = append(kept, ch)
	}
	if !removed {
		return nil
	}
	root.Children = kept
	_ = os.WriteFile(tablePath+".bak", data, 0o644)
	return writeAtomic(tablePath, []byte(formatLibTable(root)))
}

func loadOrInitLibTable(tablePath, rootTag string) (*Node, bool, []byte, error) {
	data, err := os.ReadFile(tablePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			root := List(
				Atom(rootTag),
				List(Atom("version"), Atom("7")),
			)
			return root, false, nil, nil
		}
		return nil, false, nil, fmt.Errorf("read %s: %w", tablePath, err)
	}
	root, err := ParseSExpr(string(data))
	if err != nil {
		return nil, false, nil, fmt.Errorf("parse %s: %w", tablePath, err)
	}
	if root.Tag() != rootTag {
		return nil, false, nil, fmt.Errorf("expected %s in %s, got %q", rootTag, tablePath, root.Tag())
	}
	return root, true, data, nil
}

func setLibField(libNode *Node, fieldName, val string) {
	if child := libNode.FindChild(fieldName); child != nil && len(child.Children) >= 2 {
		child.Children[1] = QString(val)
		return
	}
	libNode.Children = append(libNode.Children, List(Atom(fieldName), QString(val)))
}

func formatLibTable(root *Node) string {
	var sb strings.Builder
	sb.WriteByte('(')
	sb.WriteString(root.Tag())
	sb.WriteByte('\n')
	for _, ch := range root.Children[1:] {
		sb.WriteByte('\t')
		sb.WriteString(ch.String())
		sb.WriteByte('\n')
	}
	sb.WriteString(")\n")
	return sb.String()
}

func writeAtomic(target string, data []byte) error {
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

var (
	nixExportRe = regexp.MustCompile(`export\s+([A-Z0-9_]+)=\$\{[A-Z0-9_]+-'([^']*)'\}`)
	envVarRe    = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)\}|\$\(([A-Za-z0-9_]+)\)`)
)

// ResolveKiCadVariables builds a complete variable lookup table for expanding KiCad library URIs
// and 3D model paths (${KICAD10_3RD_PARTY}, ${KICAD10_SYMBOL_DIR}, ${KICAD10_FOOTPRINT_DIR}, etc.).
func ResolveKiCadVariables(kicadConfigDir, kicadCLIPath string, ipcStatus *IPCStatus) map[string]string {
	vars := make(map[string]string)

	home, _ := os.UserHomeDir()
	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" && home != "" {
		xdgData = filepath.Join(home, ".local", "share")
	}

	// Infer KiCad version dir (e.g. "10.0") from kicadConfigDir
	verDir := "10.0"
	if kicadConfigDir != "" {
		base := filepath.Base(filepath.Clean(kicadConfigDir))
		if strings.Contains(base, ".") {
			verDir = base
		}
	}

	// 1. User 3rdparty / user 3D models directories
	var thirdPartyCandidates []string
	if xdgData != "" {
		thirdPartyCandidates = append(thirdPartyCandidates,
			filepath.Join(xdgData, "kicad", verDir, "3rdparty"),
			filepath.Join(xdgData, "kicad", "10.0", "3rdparty"),
			filepath.Join(xdgData, "kicad", "9.0", "3rdparty"),
		)
	}
	if home != "" {
		thirdPartyCandidates = append(thirdPartyCandidates,
			filepath.Join(home, "Documents", "KiCad", verDir, "3rdparty"),
			filepath.Join(home, "Documents", "KiCad", "10.0", "3rdparty"),
		)
	}
	for _, c := range thirdPartyCandidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			for _, k := range []string{"KICAD10_3RD_PARTY", "KICAD9_3RD_PARTY", "KICAD8_3RD_PARTY", "KICAD7_3RD_PARTY", "KICAD6_3RD_PARTY"} {
				vars[k] = c
			}
			break
		}
	}
	if home != "" {
		user3D := filepath.Join(home, "Documents", "KiCad", verDir, "3dmodels")
		if info, err := os.Stat(user3D); err == nil && info.IsDir() {
			vars["KICAD_USER_3DMODEL_DIR"] = user3D
		} else if xdgData != "" {
			alt3D := filepath.Join(xdgData, "kicad", verDir, "3dmodels")
			if info, err := os.Stat(alt3D); err == nil && info.IsDir() {
				vars["KICAD_USER_3DMODEL_DIR"] = alt3D
			}
		}
	}

	// 2. Parse NixOS kicad-cli wrapper exports if present
	if kicadCLIPath != "" {
		resolvedCLI := kicadCLIPath
		if real, err := filepath.EvalSymlinks(kicadCLIPath); err == nil {
			resolvedCLI = real
		}
		if data, err := os.ReadFile(resolvedCLI); err == nil && len(data) < 64*1024 {
			for _, m := range nixExportRe.FindAllStringSubmatch(string(data), -1) {
				if len(m) == 3 && m[1] != "" && m[2] != "" {
					vars[m[1]] = m[2]
				}
			}
		}
	}

	// 3. Parse <kicadConfigDir>/kicad_common.json environment.vars
	if kicadConfigDir != "" {
		if data, err := os.ReadFile(filepath.Join(kicadConfigDir, "kicad_common.json")); err == nil {
			var common struct {
				Environment struct {
					Vars map[string]string `json:"vars"`
				} `json:"environment"`
			}
			if json.Unmarshal(data, &common) == nil {
				for k, v := range common.Environment.Vars {
					if v != "" {
						vars[k] = v
					}
				}
			}
		}
	}

	// 4. Overlay live paths reported by KiCad IPC GetPaths
	if ipcStatus != nil && len(ipcStatus.Paths) > 0 {
		if p := ipcStatus.Paths[PathStockSymbols]; p != "" {
			for _, k := range []string{"KICAD10_SYMBOL_DIR", "KICAD9_SYMBOL_DIR", "KICAD8_SYMBOL_DIR", "KICAD_SYMBOL_DIR"} {
				vars[k] = p
			}
		}
		if p := ipcStatus.Paths[PathStockFootprints]; p != "" {
			for _, k := range []string{"KICAD10_FOOTPRINT_DIR", "KICAD9_FOOTPRINT_DIR", "KICAD8_FOOTPRINT_DIR", "KICAD_FOOTPRINT_DIR"} {
				vars[k] = p
			}
		}
		if p := ipcStatus.Paths[PathStock3DModels]; p != "" {
			for _, k := range []string{"KICAD10_3DMODEL_DIR", "KICAD9_3DMODEL_DIR", "KICAD8_3DMODEL_DIR", "KICAD_3DMODEL_DIR"} {
				vars[k] = p
			}
		}
		if p := ipcStatus.Paths[PathStockTemplates]; p != "" {
			for _, k := range []string{"KICAD10_TEMPLATE_DIR", "KICAD9_TEMPLATE_DIR", "KICAD8_TEMPLATE_DIR"} {
				vars[k] = p
			}
		}
	}

	// Fill generic aliases and cross-version fallbacks
	fillAliasGroup(vars, []string{"KICAD10_SYMBOL_DIR", "KICAD9_SYMBOL_DIR", "KICAD8_SYMBOL_DIR", "KICAD7_SYMBOL_DIR", "KICAD6_SYMBOL_DIR", "KICAD_SYMBOL_DIR"})
	fillAliasGroup(vars, []string{"KICAD10_FOOTPRINT_DIR", "KICAD9_FOOTPRINT_DIR", "KICAD8_FOOTPRINT_DIR", "KICAD7_FOOTPRINT_DIR", "KICAD6_FOOTPRINT_DIR", "KICAD_FOOTPRINT_DIR"})
	fillAliasGroup(vars, []string{"KICAD10_3DMODEL_DIR", "KICAD9_3DMODEL_DIR", "KICAD8_3DMODEL_DIR", "KICAD7_3DMODEL_DIR", "KICAD6_3DMODEL_DIR", "KICAD_3DMODEL_DIR"})
	fillAliasGroup(vars, []string{"KICAD10_TEMPLATE_DIR", "KICAD9_TEMPLATE_DIR", "KICAD8_TEMPLATE_DIR", "KICAD7_TEMPLATE_DIR", "KICAD6_TEMPLATE_DIR"})

	// 5. Process environment variables take precedence when set
	for _, kv := range os.Environ() {
		if idx := strings.IndexByte(kv, '='); idx > 0 {
			k, v := kv[:idx], kv[idx+1:]
			if strings.HasPrefix(k, "KICAD") && v != "" {
				vars[k] = v
			}
		}
	}

	return vars
}

func fillAliasGroup(vars map[string]string, keys []string) {
	var found string
	for _, k := range keys {
		if v := vars[k]; v != "" {
			found = v
			break
		}
	}
	if found == "" {
		return
	}
	for _, k := range keys {
		if vars[k] == "" {
			vars[k] = found
		}
	}
}

// ExpandKiCadURI expands `${VAR}` and `$(VAR)` placeholders against vars and os.Getenv.
func ExpandKiCadURI(uri string, vars map[string]string) string {
	out := envVarRe.ReplaceAllStringFunc(uri, func(match string) string {
		sub := envVarRe.FindStringSubmatch(match)
		name := sub[1]
		if name == "" {
			name = sub[2]
		}
		if v, ok := vars[name]; ok && v != "" {
			return v
		}
		if v := os.Getenv(name); v != "" {
			return v
		}
		return match
	})
	return filepath.Clean(out)
}

// LoadEffectiveLibTables parses global (and open-project) sym-lib-table and fp-lib-table files,
// resolving variables and optionally expanding `(type "Table")` nested table entries.
func LoadEffectiveLibTables(kicadConfigDir, kicadCLIPath string, ipcStatus *IPCStatus, includeStock bool) ([]LibTableEntry, []LibTableEntry, map[string]string, error) {
	vars := ResolveKiCadVariables(kicadConfigDir, kicadCLIPath, ipcStatus)

	var symEntries []LibTableEntry
	var fpEntries []LibTableEntry

	if kicadConfigDir != "" {
		s, err := parseLibTableRecursive(filepath.Join(kicadConfigDir, "sym-lib-table"), "sym_lib_table", vars, includeStock, 0)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, vars, err
		}
		symEntries = append(symEntries, s...)

		f, err := parseLibTableRecursive(filepath.Join(kicadConfigDir, "fp-lib-table"), "fp_lib_table", vars, includeStock, 0)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, vars, err
		}
		fpEntries = append(fpEntries, f...)
	}

	// Include open project library tables reported via IPC
	if ipcStatus != nil {
		seenProj := make(map[string]bool)
		for _, doc := range ipcStatus.OpenDocuments {
			projDir := strings.TrimSpace(doc.ProjectPath)
			if projDir == "" || seenProj[projDir] {
				continue
			}
			seenProj[projDir] = true
			projVars := make(map[string]string, len(vars)+1)
			for k, v := range vars {
				projVars[k] = v
			}
			projVars["KIPRJMOD"] = projDir

			if s, err := parseLibTableRecursive(filepath.Join(projDir, "sym-lib-table"), "sym_lib_table", projVars, includeStock, 0); err == nil {
				symEntries = append(symEntries, s...)
			}
			if f, err := parseLibTableRecursive(filepath.Join(projDir, "fp-lib-table"), "fp_lib_table", projVars, includeStock, 0); err == nil {
				fpEntries = append(fpEntries, f...)
			}
		}
	}

	return dedupeLibTableEntries(symEntries), dedupeLibTableEntries(fpEntries), vars, nil
}

func parseLibTableRecursive(tablePath, rootTag string, vars map[string]string, includeStock bool, depth int) ([]LibTableEntry, error) {
	if depth > 3 {
		return nil, nil
	}
	data, err := os.ReadFile(tablePath)
	if err != nil {
		return nil, err
	}
	root, err := ParseSExpr(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", tablePath, err)
	}
	if root.Tag() != rootTag {
		return nil, fmt.Errorf("expected %s in %s, got %q", rootTag, tablePath, root.Tag())
	}

	var entries []LibTableEntry
	for _, libNode := range root.FindChildren("lib") {
		if disabledNode := libNode.FindChild("disabled"); disabledNode != nil {
			continue
		}
		entry := LibTableEntry{
			Name:        fieldValue(libNode, "name"),
			Type:        fieldValue(libNode, "type"),
			URI:         fieldValue(libNode, "uri"),
			Options:     fieldValue(libNode, "options"),
			Description: fieldValue(libNode, "descr"),
		}
		if entry.URI == "" {
			continue
		}
		entry.ResolvedPath = ExpandKiCadURI(entry.URI, vars)

		if strings.EqualFold(entry.Type, "Table") {
			if !includeStock {
				continue
			}
			subTable := resolveNestedTablePath(entry.ResolvedPath, rootTag, vars)
			if subTable != "" {
				subEntries, subErr := parseLibTableRecursive(subTable, rootTag, vars, includeStock, depth+1)
				if subErr == nil {
					entries = append(entries, subEntries...)
				}
			}
			continue
		}

		entries = append(entries, entry)
	}
	return entries, nil
}

func resolveNestedTablePath(resolvedPath, rootTag string, vars map[string]string) string {
	if _, err := os.Stat(resolvedPath); err == nil {
		return resolvedPath
	}
	tableName := "sym-lib-table"
	dirVar := "KICAD10_SYMBOL_DIR"
	if rootTag == "fp_lib_table" {
		tableName = "fp-lib-table"
		dirVar = "KICAD10_FOOTPRINT_DIR"
	}
	var candidates []string
	if d := vars[dirVar]; d != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(d), "template", tableName))
	}
	if t := vars["KICAD10_TEMPLATE_DIR"]; t != "" {
		candidates = append(candidates, filepath.Join(t, tableName))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func fieldValue(libNode *Node, key string) string {
	ch := libNode.FindChild(key)
	if ch == nil || len(ch.Children) < 2 {
		return ""
	}
	return ch.Children[1].Value
}

func dedupeLibTableEntries(in []LibTableEntry) []LibTableEntry {
	seen := make(map[string]bool, len(in))
	out := make([]LibTableEntry, 0, len(in))
	for _, e := range in {
		if e.Name == "" || seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		out = append(out, e)
	}
	return out
}
