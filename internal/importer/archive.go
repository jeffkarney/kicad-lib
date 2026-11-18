package importer

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jeffk/kicad-lib/internal/kicad"
)

// CandidateModel3D represents a 3D model discovered during scanning.
type CandidateModel3D struct {
	SourcePath string
	FileName   string
	Format     string
}

// CandidateFootprint represents a KiCad footprint discovered during scanning.
type CandidateFootprint struct {
	Name       string
	SourcePath string
	RawSExpr   string
	PadCount   int
	ModelRefs  []string
}

// CandidatePart represents a parsed component ready for review in the Import Staging Dialog.
type CandidatePart struct {
	Selected     bool
	PartName     string
	Value        string
	Description  string
	Keywords     string
	Datasheet    string
	MPN          string
	Manufacturer string
	LCSCPart     string
	SourceType   string
	SourceRef    string

	SymbolName       string
	SymbolSourcePath string
	SymbolSExpr      string
	PinCount         int

	FootprintName       string
	FootprintSourcePath string
	FootprintSExpr      string
	PadCount            int

	Models3D []CandidateModel3D
	Metadata map[string]string
}

// ScanResult summarizes all KiCad assets and paired candidate parts found in dropped paths or a Git repo.
type ScanResult struct {
	VendorHint    string
	TempWorkDir   string
	Candidates    []CandidatePart
	AllFootprints []CandidateFootprint
	AllModels3D   []CandidateModel3D
	Datasheets    []string
	Warnings      []string
}

// Cleanup removes any temporary directory created during archive extraction.
func (r *ScanResult) Cleanup() {
	if r != nil && r.TempWorkDir != "" {
		_ = os.RemoveAll(r.TempWorkDir)
		r.TempWorkDir = ""
	}
}

// ScanDroppedPaths inspects one or more dropped files (.zip, .kicad_sym, .lib, .kicad_mod, .step, .wrl)
// or directories, extracts archives (including nested zips), upgrades legacy .lib files, and pairs
// symbols, footprints, 3D models, and datasheets into CandidateParts.
func ScanDroppedPaths(paths []string, kicadCLIPath string) (*ScanResult, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no paths provided")
	}

	tempDir, err := os.MkdirTemp("", "kicad-lib-scan-*")
	if err != nil {
		return nil, err
	}

	res := &ScanResult{
		VendorHint:  "Generic",
		TempWorkDir: tempDir,
	}

	origDirs := make(map[string]string)
	var rootsToWalk []string
	for idx, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("cannot access %q: %v", p, err))
			continue
		}
		if info.IsDir() {
			rootsToWalk = append(rootsToWalk, p)
			continue
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".zip" {
			extractDst := filepath.Join(tempDir, fmt.Sprintf("zip_%d", idx))
			if err := extractZipRecursive(p, extractDst, 0, 3); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("extract %q: %v", filepath.Base(p), err))
				continue
			}
			rootsToWalk = append(rootsToWalk, extractDst)
		} else if ext == ".kicad_pro" {
			// Dropping a KiCad project file scans the entire project directory for schematics, PCBs, libraries, and 3D models.
			rootsToWalk = append(rootsToWalk, filepath.Dir(p))
		} else {
			// Single dropped file; copy into temp staging dir
			dst := filepath.Join(tempDir, "single", filepath.Base(p))
			if err := copyFileSimple(p, dst); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("stage %q: %v", p, err))
				continue
			}
			origDirs[dst] = filepath.Dir(p)
			if ext == ".kicad_sch" {
				stageHierarchicalSheets(p, filepath.Dir(p), filepath.Dir(dst), res.TempWorkDir, map[string]bool{filepath.Clean(p): true})
			}
			rootsToWalk = append(rootsToWalk, filepath.Dir(dst))
		}
	}

	return scanDirectories(rootsToWalk, res, kicadCLIPath, "drop", strings.Join(paths, "; "), origDirs)
}

func stageHierarchicalSheets(schPath, origRootDir, stageRootDir, _ string, visited map[string]bool) {
	data, err := os.ReadFile(schPath)
	if err != nil {
		return
	}
	for _, rel := range kicad.ExtractSchematicSheetFiles(data) {
		rel = strings.ReplaceAll(rel, "\\", string(filepath.Separator))
		src := filepath.Clean(filepath.Join(filepath.Dir(schPath), rel))
		if visited[src] {
			continue
		}
		visited[src] = true
		info, err := os.Stat(src)
		if err != nil || info.IsDir() {
			continue
		}
		subRel, err := filepath.Rel(origRootDir, src)
		if err != nil || strings.HasPrefix(subRel, "..") {
			subRel = filepath.Base(src)
		}
		dst := filepath.Join(stageRootDir, subRel)
		if err := copyFileSimple(src, dst); err == nil {
			stageHierarchicalSheets(src, origRootDir, stageRootDir, "", visited)
		}
	}
}

// ScanDirectory scans an already-extracted directory (such as a cloned Git repository).
func ScanDirectory(dirPath, kicadCLIPath, sourceType, sourceRef string) (*ScanResult, error) {
	tempDir, err := os.MkdirTemp("", "kicad-lib-dirscan-*")
	if err != nil {
		return nil, err
	}
	res := &ScanResult{
		VendorHint:  "Generic",
		TempWorkDir: tempDir,
	}
	return scanDirectories([]string{dirPath}, res, kicadCLIPath, sourceType, sourceRef, nil)
}

type discoveredSymbol struct {
	parsed     kicad.ParsedSymbol
	sourcePath string
}

func scanDirectories(roots []string, res *ScanResult, kicadCLIPath, sourceType, sourceRef string, origDirs map[string]string) (*ScanResult, error) {
	var symFiles []string
	var legacyLibFiles []string
	var fpFiles []string
	var modelFiles []string
	var pdfFiles []string
	var proFiles []string
	var schFiles []string
	var pcbFiles []string

	seenFiles := make(map[string]bool)
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if name == ".git" || name == "__MACOSX" {
					return filepath.SkipDir
				}
				detectVendorFromPath(path, res)
				return nil
			}
			if seenFiles[path] {
				return nil
			}
			seenFiles[path] = true
			detectVendorFromPath(path, res)

			ext := strings.ToLower(filepath.Ext(name))
			switch ext {
			case ".kicad_sym":
				symFiles = append(symFiles, path)
			case ".lib":
				legacyLibFiles = append(legacyLibFiles, path)
			case ".kicad_mod":
				fpFiles = append(fpFiles, path)
			case ".step", ".stp", ".stpz", ".wrl":
				modelFiles = append(modelFiles, path)
			case ".pdf":
				pdfFiles = append(pdfFiles, path)
			case ".kicad_pro":
				proFiles = append(proFiles, path)
			case ".kicad_sch":
				schFiles = append(schFiles, path)
			case ".kicad_pcb":
				pcbFiles = append(pcbFiles, path)
			}
			return nil
		})
	}

	if res.VendorHint == "Generic" {
		switch {
		case len(proFiles) > 0 || (len(schFiles) > 0 && len(pcbFiles) > 0):
			res.VendorHint = "KiCad Project"
		case len(schFiles) > 0:
			res.VendorHint = "KiCad Schematic"
		case len(pcbFiles) > 0:
			res.VendorHint = "KiCad PCB"
		}
	}

	// Collect 3D models already discovered in the dropped tree
	seenModelPaths := make(map[string]bool)
	addModelFile := func(mf string) {
		clean := filepath.Clean(mf)
		if seenModelPaths[clean] {
			return
		}
		info, err := os.Stat(clean)
		if err != nil || info.IsDir() {
			return
		}
		seenModelPaths[clean] = true
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(clean)), ".")
		if ext == "stp" {
			ext = "step"
		}
		res.AllModels3D = append(res.AllModels3D, CandidateModel3D{
			SourcePath: clean,
			FileName:   filepath.Base(clean),
			Format:     ext,
		})
	}
	for _, mf := range modelFiles {
		addModelFile(mf)
	}
	res.Datasheets = append(res.Datasheets, pdfFiles...)

	// Collect standalone .kicad_mod footprints
	seenFPNames := make(map[string]bool)
	for _, fpf := range fpFiles {
		data, err := os.ReadFile(fpf)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("read footprint %q: %v", filepath.Base(fpf), err))
			continue
		}
		pf, err := kicad.ParseFootprintFile(data)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("parse footprint %q: %v", filepath.Base(fpf), err))
			continue
		}
		name := pf.Name
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(fpf), filepath.Ext(fpf))
		}
		var mRefs []string
		for _, m := range pf.Models {
			mRefs = append(mRefs, filepath.Base(strings.ReplaceAll(m.Path, "\\", "/")))
		}
		seenFPNames[strings.ToLower(name)] = true
		res.AllFootprints = append(res.AllFootprints, CandidateFootprint{
			Name:       name,
			SourcePath: fpf,
			RawSExpr:   pf.RawSExpr,
			PadCount:   pf.PadCount,
			ModelRefs:  mRefs,
		})
	}

	// Extract embedded footprints and referenced 3D models from .kicad_pcb files
	if len(pcbFiles) > 0 {
		baseVars := kicad.ResolveKiCadVariables("", kicadCLIPath, nil)
		pcbExtractDir := filepath.Join(res.TempWorkDir, "pcb_extracted.pretty")
		_ = os.MkdirAll(pcbExtractDir, 0o755)
		for _, pcbf := range pcbFiles {
			data, err := os.ReadFile(pcbf)
			if err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("read pcb %q: %v", filepath.Base(pcbf), err))
				continue
			}
			extractedFPs, err := kicad.ExtractFromPCB(data)
			if err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("extract pcb footprints %q: %v", filepath.Base(pcbf), err))
				continue
			}

			prjDir := filepath.Dir(pcbf)
			if od, ok := origDirs[pcbf]; ok && od != "" {
				prjDir = od
			}
			localVars := make(map[string]string, len(baseVars)+1)
			for k, v := range baseVars {
				localVars[k] = v
			}
			localVars["KIPRJMOD"] = prjDir

			for _, pf := range extractedFPs {
				var mRefs []string
				for _, m := range pf.Models {
					rawModel := strings.ReplaceAll(strings.TrimSpace(m.Path), "\\", "/")
					mRefs = append(mRefs, filepath.Base(rawModel))
					expanded := kicad.ExpandKiCadURI(rawModel, localVars)
					resolved := kicad.Resolve3DModelPath(expanded)
					if resolved == expanded {
						if _, statErr := os.Stat(resolved); statErr != nil && !filepath.IsAbs(expanded) {
							resolved = kicad.Resolve3DModelPath(filepath.Join(prjDir, expanded))
						}
					}
					addModelFile(resolved)
				}

				fpKey := strings.ToLower(pf.Name)
				if pf.Name == "" || seenFPNames[fpKey] {
					continue
				}
				seenFPNames[fpKey] = true
				safeName := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(pf.Name)
				stagedFP := filepath.Join(pcbExtractDir, safeName+".kicad_mod")
				_ = os.WriteFile(stagedFP, []byte(pf.RawSExpr), 0o644)
				res.AllFootprints = append(res.AllFootprints, CandidateFootprint{
					Name:       pf.Name,
					SourcePath: stagedFP,
					RawSExpr:   pf.RawSExpr,
					PadCount:   pf.PadCount,
					ModelRefs:  mRefs,
				})
			}
		}
	}

	// Upgrade legacy KiCad v5 .lib files
	for idx, lf := range legacyLibFiles {
		if !isKiCadV5SymbolLib(lf) {
			continue
		}
		upgradedPath := filepath.Join(res.TempWorkDir, fmt.Sprintf("upgraded_%d_%s.kicad_sym", idx, strings.TrimSuffix(filepath.Base(lf), ".lib")))
		if kicadCLIPath != "" {
			if err := kicad.UpgradeSymbolLib(kicadCLIPath, lf, upgradedPath); err == nil {
				symFiles = append(symFiles, upgradedPath)
				continue
			}
		}
		// Fallback pure-Go converter if kicad-cli unavailable or failed
		converted, err := convertLegacyV5LibFile(lf)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("upgrade legacy %q: %v", filepath.Base(lf), err))
			continue
		}
		if err := os.WriteFile(upgradedPath, []byte(converted), 0o644); err == nil {
			symFiles = append(symFiles, upgradedPath)
		}
	}

	// Parse .kicad_sym files
	var symbols []discoveredSymbol
	seenSymNames := make(map[string]bool)
	for _, sf := range symFiles {
		data, err := os.ReadFile(sf)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("read symbol %q: %v", filepath.Base(sf), err))
			continue
		}
		parsedSyms, err := kicad.ParseSymbolLibrary(data)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("parse symbol %q: %v", filepath.Base(sf), err))
			continue
		}
		for _, ps := range parsedSyms {
			seenSymNames[strings.ToLower(ps.Name)] = true
			symbols = append(symbols, discoveredSymbol{parsed: ps, sourcePath: sf})
		}
	}

	// Extract symbols from .kicad_sch schematics (also recursively follow hierarchical sheets in folder/zip scans)
	if len(schFiles) > 0 {
		processedSch := make(map[string]bool)
		queue := append([]string(nil), schFiles...)
		for len(queue) > 0 {
			sf := queue[0]
			queue = queue[1:]
			cleanSF := filepath.Clean(sf)
			if processedSch[cleanSF] {
				continue
			}
			processedSch[cleanSF] = true

			data, err := os.ReadFile(cleanSF)
			if err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("read schematic %q: %v", filepath.Base(cleanSF), err))
				continue
			}
			for _, rel := range kicad.ExtractSchematicSheetFiles(data) {
				rel = strings.ReplaceAll(rel, "\\", string(filepath.Separator))
				baseDir := filepath.Dir(cleanSF)
				if od, ok := origDirs[sf]; ok && od != "" {
					baseDir = od
				}
				subPath := filepath.Clean(filepath.Join(baseDir, rel))
				if !processedSch[subPath] {
					if info, sErr := os.Stat(subPath); sErr == nil && !info.IsDir() {
						queue = append(queue, subPath)
					}
				}
			}

			extractedSyms, err := kicad.ExtractFromSchematic(data)
			if err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("extract schematic symbols %q: %v", filepath.Base(cleanSF), err))
				continue
			}
			for _, ps := range extractedSyms {
				symKey := strings.ToLower(ps.Name)
				if ps.Name == "" || seenSymNames[symKey] {
					continue
				}
				seenSymNames[symKey] = true
				symbols = append(symbols, discoveredSymbol{parsed: ps, sourcePath: cleanSF})
			}
		}
	}

	// Pair symbols with footprints and 3D models
	usedFootprints := make(map[int]bool)
	for _, ds := range symbols {
		ps := ds.parsed
		cand := CandidatePart{
			Selected:         true,
			PartName:         ps.Name,
			Value:            ps.Value,
			Description:      ps.Description,
			Keywords:         ps.Keywords,
			Datasheet:        ps.Datasheet,
			SourceType:       sourceType,
			SourceRef:        sourceRef,
			SymbolName:       ps.Name,
			SymbolSourcePath: ds.sourcePath,
			SymbolSExpr:      ps.RawSExpr,
			PinCount:         ps.PinCount,
			Metadata:         make(map[string]string),
		}

		for k, v := range ps.Properties {
			cand.Metadata[k] = v
			lk := strings.ToLower(k)
			switch lk {
			case "mpn", "manufacturer_part_number", "part number", "part_number", "mfr. no", "mfg part":
				if cand.MPN == "" {
					cand.MPN = v
				}
			case "manufacturer", "mfr", "mf", "mfg":
				if cand.Manufacturer == "" {
					cand.Manufacturer = v
				}
			case "lcsc", "lcsc part", "lcsc_part":
				if cand.LCSCPart == "" {
					cand.LCSCPart = v
				}
			}
		}
		if cand.MPN == "" && ps.Name != "" {
			cand.MPN = ps.Name
		}
		if cand.Datasheet == "" && len(res.Datasheets) == 1 {
			cand.Datasheet = res.Datasheets[0]
		}

		// Match footprint
		fpIdx := matchFootprint(ps, res.AllFootprints, len(symbols) == 1)
		if fpIdx >= 0 {
			usedFootprints[fpIdx] = true
			fp := res.AllFootprints[fpIdx]
			cand.FootprintName = fp.Name
			cand.FootprintSourcePath = fp.SourcePath
			cand.FootprintSExpr = fp.RawSExpr
			cand.PadCount = fp.PadCount
			cand.Models3D = matchModelsForFootprint(fp, res.AllModels3D, len(symbols) == 1)
		} else if len(symbols) == 1 && len(res.AllModels3D) > 0 {
			cand.Models3D = append(cand.Models3D, res.AllModels3D...)
		}

		res.Candidates = append(res.Candidates, cand)
	}

	// Emit standalone footprints that weren't paired with any symbol
	for idx, fp := range res.AllFootprints {
		if usedFootprints[idx] {
			continue
		}
		if len(symbols) == 0 || len(pcbFiles) > 0 || len(proFiles) > 0 {
			cand := CandidatePart{
				Selected:            true,
				PartName:            fp.Name,
				Value:               fp.Name,
				SourceType:          sourceType,
				SourceRef:           sourceRef,
				FootprintName:       fp.Name,
				FootprintSourcePath: fp.SourcePath,
				FootprintSExpr:      fp.RawSExpr,
				PadCount:            fp.PadCount,
				Models3D:            matchModelsForFootprint(fp, res.AllModels3D, len(res.AllFootprints) == 1),
				Metadata:            make(map[string]string),
			}
			res.Candidates = append(res.Candidates, cand)
		}
	}

	return res, nil
}

func matchFootprint(ps kicad.ParsedSymbol, fps []CandidateFootprint, singleSymbol bool) int {
	if len(fps) == 0 {
		return -1
	}
	if len(fps) == 1 && singleSymbol {
		return 0
	}
	fpRef := ps.Footprint
	if idx := strings.Index(fpRef, ":"); idx >= 0 {
		fpRef = fpRef[idx+1:]
	}
	fpRefNorm := normalizeMatchName(fpRef)
	symNorm := normalizeMatchName(ps.Name)

	// 1. Exact match on Footprint property
	for i, fp := range fps {
		if fpRef != "" && strings.EqualFold(fp.Name, fpRef) {
			return i
		}
	}
	// 2. Normalized match on Footprint property
	if fpRefNorm != "" {
		for i, fp := range fps {
			if normalizeMatchName(fp.Name) == fpRefNorm {
				return i
			}
		}
	}
	// 3. Match by symbol name
	for i, fp := range fps {
		fpNorm := normalizeMatchName(fp.Name)
		if fpNorm == symNorm || (len(symNorm) >= 4 && strings.Contains(fpNorm, symNorm)) {
			return i
		}
	}
	// 4. Fallback if single footprint and either single symbol or no explicit Footprint reference
	if len(fps) == 1 && (singleSymbol || fpRef == "") {
		return 0
	}
	return -1
}

func matchModelsForFootprint(fp CandidateFootprint, all []CandidateModel3D, singlePart bool) []CandidateModel3D {
	if len(all) == 0 {
		return nil
	}
	var matched []CandidateModel3D
	seen := make(map[string]bool)

	// Match by explicit `(model ...)` filename reference inside the footprint
	for _, ref := range fp.ModelRefs {
		refNorm := normalizeMatchName(strings.TrimSuffix(ref, filepath.Ext(ref)))
		for _, m := range all {
			if seen[m.SourcePath] {
				continue
			}
			mNorm := normalizeMatchName(strings.TrimSuffix(m.FileName, filepath.Ext(m.FileName)))
			if strings.EqualFold(m.FileName, ref) || (refNorm != "" && mNorm == refNorm) {
				matched = append(matched, m)
				seen[m.SourcePath] = true
			}
		}
	}

	// Match by footprint name stem
	if len(matched) == 0 {
		fpNorm := normalizeMatchName(fp.Name)
		for _, m := range all {
			mNorm := normalizeMatchName(strings.TrimSuffix(m.FileName, filepath.Ext(m.FileName)))
			if fpNorm != "" && (mNorm == fpNorm || strings.Contains(mNorm, fpNorm) || strings.Contains(fpNorm, mNorm)) {
				matched = append(matched, m)
				seen[m.SourcePath] = true
			}
		}
	}

	// In single-part vendor zips (UltraLibrarian, SnapMagic, SamacSys), attach all 3D models in the archive
	if len(matched) == 0 && singlePart {
		return append([]CandidateModel3D(nil), all...)
	}
	return matched
}

func normalizeMatchName(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func detectVendorFromPath(p string, res *ScanResult) {
	if res.VendorHint != "Generic" {
		return
	}
	lp := strings.ToLower(p)
	switch {
	case strings.Contains(lp, "ultralibrarian") || strings.Contains(lp, "ul_"):
		res.VendorHint = "UltraLibrarian"
	case strings.Contains(lp, "snapmagic") || strings.Contains(lp, "snapeda"):
		res.VendorHint = "SnapMagic"
	case strings.Contains(lp, "samacsys") || strings.Contains(lp, "componentsearchengine"):
		res.VendorHint = "SamacSys"
	case strings.Contains(lp, "easyeda"):
		res.VendorHint = "EasyEDA"
	}
}

func extractZipRecursive(zipPath, destDir string, depth, maxDepth int) error {
	if depth > maxDepth {
		return nil
	}
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	cleanDest := filepath.Clean(destDir)
	for _, f := range r.File {
		target := filepath.Join(cleanDest, f.Name)
		if !strings.HasPrefix(filepath.Clean(target), cleanDest+string(os.PathSeparator)) && filepath.Clean(target) != cleanDest {
			continue // protect against zip-slip
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		_ = out.Close()
		_ = rc.Close()
		if copyErr != nil {
			return copyErr
		}

		if strings.EqualFold(filepath.Ext(target), ".zip") && depth < maxDepth {
			nestedDir := strings.TrimSuffix(target, filepath.Ext(target)) + "_extracted"
			_ = extractZipRecursive(target, nestedDir, depth+1, maxDepth)
		}
	}
	return nil
}

func isKiCadV5SymbolLib(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	return strings.Contains(string(buf[:n]), "EESchema-LIBRARY")
}

// convertLegacyV5LibFile provides a pure-Go fallback converter from KiCad v5 `.lib` to `.kicad_sym`
// when `kicad-cli` is unavailable or fails on malformed vendor headers.
func convertLegacyV5LibFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var symBlocks []string

	var curName, curRef, curFootprint, curDatasheet string
	var drawLines []string
	inDef := false
	inDraw := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "DEF ") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				inDef = true
				inDraw = false
				curName = strings.TrimPrefix(fields[1], "~")
				curRef = fields[2]
				curFootprint = ""
				curDatasheet = ""
				drawLines = nil
			}
			continue
		}
		if !inDef {
			continue
		}
		if line == "ENDDEF" {
			symBlocks = append(symBlocks, buildFallbackKiCadSym(curName, curRef, curFootprint, curDatasheet, drawLines))
			inDef = false
			inDraw = false
			continue
		}
		if line == "DRAW" {
			inDraw = true
			continue
		}
		if line == "ENDDRAW" {
			inDraw = false
			continue
		}
		if inDraw {
			drawLines = append(drawLines, line)
			continue
		}
		// Parse F0..F3 properties
		switch {
		case strings.HasPrefix(line, "F0 "):
			if q := extractFirstQuoted(line); q != "" {
				curRef = q
			}
		case strings.HasPrefix(line, "F1 "):
			if q := extractFirstQuoted(line); q != "" {
				curName = q
			}
		case strings.HasPrefix(line, "F2 "):
			if q := extractFirstQuoted(line); q != "" {
				curFootprint = q
			}
		case strings.HasPrefix(line, "F3 "):
			if q := extractFirstQuoted(line); q != "" {
				curDatasheet = q
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if len(symBlocks) == 0 {
		return "", fmt.Errorf("no symbol definitions found in %s", filepath.Base(path))
	}
	return kicad.BuildSymbolLibraryFile(symBlocks), nil
}

func buildFallbackKiCadSym(name, ref, fp, ds string, drawLines []string) string {
	if ref == "" {
		ref = "U"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\t(symbol %q\n", name)
	sb.WriteString("\t\t(in_bom yes)\n\t\t(on_board yes)\n")
	fmt.Fprintf(&sb, "\t\t(property \"Reference\" %q (at 0 2.54 0) (effects (font (size 1.27 1.27))))\n", ref)
	fmt.Fprintf(&sb, "\t\t(property \"Value\" %q (at 0 -2.54 0) (effects (font (size 1.27 1.27))))\n", name)
	fmt.Fprintf(&sb, "\t\t(property \"Footprint\" %q (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))\n", fp)
	fmt.Fprintf(&sb, "\t\t(property \"Datasheet\" %q (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))\n", ds)

	var shapes []string
	var pins []string
	for _, dl := range drawLines {
		fields := strings.Fields(dl)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "S": // Rectangle: S startx starty endx endy unit dmg pen fill
			if len(fields) >= 5 {
				x1 := milToMM(fields[1])
				y1 := milToMM(fields[2])
				x2 := milToMM(fields[3])
				y2 := milToMM(fields[4])
				shapes = append(shapes, fmt.Sprintf("\t\t\t(rectangle (start %.3f %.3f) (end %.3f %.3f) (stroke (width 0.254) (type default)) (fill (type background)))", x1, y1, x2, y2))
			}
		case "X": // Pin: X name num posx posy length dir sizenum sizename unit dmg etype
			if len(fields) >= 7 {
				pname := fields[1]
				pnum := fields[2]
				px := milToMM(fields[3])
				py := milToMM(fields[4])
				plen := milToMM(fields[5])
				if plen <= 0 {
					plen = 2.54
				}
				ang := 0
				switch fields[6] {
				case "R":
					ang = 0
				case "U":
					ang = 90
				case "L":
					ang = 180
				case "D":
					ang = 270
				}
				pins = append(pins, fmt.Sprintf("\t\t\t(pin passive line (at %.3f %.3f %d) (length %.3f) (name %q (effects (font (size 1.27 1.27)))) (number %q (effects (font (size 1.27 1.27)))))", px, py, ang, plen, pname, pnum))
			}
		}
	}
	if len(shapes) > 0 {
		fmt.Fprintf(&sb, "\t\t(symbol \"%s_0_1\"\n%s\n\t\t)\n", name, strings.Join(shapes, "\n"))
	}
	if len(pins) > 0 {
		fmt.Fprintf(&sb, "\t\t(symbol \"%s_1_1\"\n%s\n\t\t)\n", name, strings.Join(pins, "\n"))
	}
	sb.WriteString("\t)\n")
	return sb.String()
}

func milToMM(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v * 0.0254
}

func extractFirstQuoted(line string) string {
	start := strings.IndexByte(line, '"')
	if start < 0 {
		return ""
	}
	end := strings.IndexByte(line[start+1:], '"')
	if end < 0 {
		return ""
	}
	return line[start+1 : start+1+end]
}

func copyFileSimple(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
