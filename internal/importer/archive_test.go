package importer

import (
	"archive/zip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeffk/kicad-lib/internal/kicad"
)

const sampleTestSym = `(kicad_symbol_lib
	(version 20231120)
	(generator "kicad_symbol_editor")
	(symbol "TPS62130"
		(in_bom yes)
		(on_board yes)
		(property "Reference" "U" (at 0 5.08 0) (effects (font (size 1.27 1.27))))
		(property "Value" "TPS62130" (at 0 -5.08 0) (effects (font (size 1.27 1.27))))
		(property "Footprint" "Vendor:QFN-16_3x3mm" (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))
		(property "Datasheet" "https://ti.com/tps62130.pdf" (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))
		(property "Manufacturer" "Texas Instruments" (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))
		(symbol "TPS62130_1_1"
			(pin input line (at -5.08 0 0) (length 2.54) (name "VIN" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
		)
	)
)
`

const sampleTestFP = `(footprint "QFN-16_3x3mm"
	(layer "F.Cu")
	(attr smd)
	(pad "1" smd rect (at -1.5 -0.75) (size 0.8 0.28) (layers "F.Cu" "F.Mask" "F.Paste"))
	(model "C:\\Vendor\\3D\\TPS62130.step" (offset (xyz 0 0 0)) (scale (xyz 1 1 1)) (rotate (xyz 0 0 0)))
)
`

const sampleLegacyV5Lib = `EESchema-LIBRARY Version 2.4
#encoding utf-8
DEF LM7805 U 0 40 Y Y 1 F N
F0 "U" 0 150 50 H V C CNN
F1 "LM7805" 0 -150 50 H V C CNN
F2 "TO-220" 0 0 50 H I C CNN
F3 "" 0 0 50 H I C CNN
DRAW
S -200 100 200 -100 0 1 0 f
X IN 1 -400 0 200 R 50 50 1 1 I
X GND 2 0 -300 200 U 50 50 1 1 W
X OUT 3 400 0 200 L 50 50 1 1 O
ENDDRAW
ENDDEF
#
#End Library
`

func createZip(t *testing.T, zipPath string, entries map[string][]byte) {
	t.Helper()
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestScanDroppedZipUltraLibrarianNestedAndSamacSys(t *testing.T) {
	tmp := t.TempDir()

	// Inner SamacSys-style zip
	innerZipBytesPath := filepath.Join(tmp, "inner.zip")
	createZip(t, innerZipBytesPath, map[string][]byte{
		"SamacSys_Part/KiCad/TPS62130.kicad_sym":                     []byte(sampleTestSym),
		"SamacSys_Part/KiCad/SamacSys.pretty/QFN-16_3x3mm.kicad_mod": []byte(sampleTestFP),
		"SamacSys_Part/3D/TPS62130.step":                             []byte("ISO-10303-21;\nHEADER;\nENDSEC;\nEND-ISO-10303-21;\n"),
		"SamacSys_Part/Datasheet/TPS62130.pdf":                       []byte("%PDF-1.4\n"),
	})
	innerData, err := os.ReadFile(innerZipBytesPath)
	if err != nil {
		t.Fatal(err)
	}

	// Outer dropped zip containing the nested zip
	outerZipPath := filepath.Join(tmp, "LIB_TPS62130.zip")
	createZip(t, outerZipPath, map[string][]byte{
		"LIB_TPS62130/TPS62130.zip": innerData,
	})

	cliPath, _ := exec.LookPath("kicad-cli")
	res, err := ScanDroppedPaths([]string{outerZipPath}, cliPath)
	if err != nil {
		t.Fatalf("ScanDroppedPaths failed: %v", err)
	}
	defer res.Cleanup()

	if res.VendorHint != "SamacSys" {
		t.Fatalf("expected VendorHint=SamacSys, got %q", res.VendorHint)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("expected 1 candidate part, got %d (warnings: %v)", len(res.Candidates), res.Warnings)
	}
	cand := res.Candidates[0]
	if cand.PartName != "TPS62130" || cand.Manufacturer != "Texas Instruments" {
		t.Fatalf("unexpected candidate metadata: %+v", cand)
	}
	if cand.FootprintName != "QFN-16_3x3mm" || cand.PadCount != 1 {
		t.Fatalf("expected paired footprint QFN-16_3x3mm, got %q (pads=%d)", cand.FootprintName, cand.PadCount)
	}
	if len(cand.Models3D) != 1 || cand.Models3D[0].FileName != "TPS62130.step" {
		t.Fatalf("expected attached 3D STEP model, got %+v", cand.Models3D)
	}
}

func TestScanLegacyV5LibUpgrade(t *testing.T) {
	tmp := t.TempDir()
	libPath := filepath.Join(tmp, "LM7805.lib")
	if err := os.WriteFile(libPath, []byte(sampleLegacyV5Lib), 0o644); err != nil {
		t.Fatal(err)
	}
	fpPath := filepath.Join(tmp, "TO-220.kicad_mod")
	if err := os.WriteFile(fpPath, []byte(`(footprint "TO-220" (layer "F.Cu") (pad "1" thru_hole rect (at 0 0) (size 1.5 1.5) (drill 0.8) (layers "*.Cu" "*.Mask")))`), 0o644); err != nil {
		t.Fatal(err)
	}

	cliPath, _ := exec.LookPath("kicad-cli")
	res, err := ScanDroppedPaths([]string{tmp}, cliPath)
	if err != nil {
		t.Fatalf("ScanDroppedPaths failed: %v", err)
	}
	defer res.Cleanup()

	if len(res.Candidates) != 1 {
		t.Fatalf("expected 1 upgraded candidate, got %d (warnings: %v)", len(res.Candidates), res.Warnings)
	}
	cand := res.Candidates[0]
	if cand.PartName != "LM7805" || cand.PinCount != 3 {
		t.Fatalf("expected upgraded LM7805 with 3 pins, got %+v", cand)
	}
	if cand.FootprintName != "TO-220" {
		t.Fatalf("expected paired TO-220 footprint, got %q", cand.FootprintName)
	}
}

func TestFetchLCSCPartMockAPI(t *testing.T) {
	stepBody := "ISO-10303-21;\nHEADER;\nFILE_DESCRIPTION(('STEP'),'2;1');\nENDSEC;\nEND-ISO-10303-21;\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "step-uuid-123") {
			_, _ = w.Write([]byte(stepBody))
			return
		}
		resp := map[string]any{
			"success": true,
			"result": map[string]any{
				"uuid":        "comp-uuid",
				"title":       "RP2040",
				"description": "Dual ARM Cortex-M0+ MCU",
				"dataStr": map[string]any{
					"head": map[string]any{
						"x": 0, "y": 0,
						"c_para": map[string]string{
							"Manufacturer":      "Raspberry Pi",
							"Manufacturer Part": "RP2040",
							"package":           "QFN-56",
							"pre":               "U?",
							"link":              "https://datasheets.raspberrypi.com/rp2040/rp2040-datasheet.pdf",
						},
					},
					"shape": []string{
						"R~-20~-20~0~0~40~40~#880000~1~0~none",
						"P~show~0~1~-30~0~180~gge1^^0~0^^M -30 0 h 10^^#000~GPIO0^^#000~1",
					},
				},
				"packageDetail": map[string]any{
					"title": "QFN-56_EP",
					"dataStr": map[string]any{
						"head": map[string]any{"x": 0, "y": 0},
						"shape": []string{
							"PAD~RECT~-10~0~2~5~1~~1~0~~0",
							`SVGNODE~{"attrs":{"uuid":"step-uuid-123"}}`,
						},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	cand, err := fetchLCSCPartFromBase(
		context.Background(),
		"C2040",
		tmp,
		srv.URL+"/api/products/%s/components",
		srv.URL+"/3d/%s",
	)
	if err != nil {
		t.Fatalf("fetchLCSCPartFromBase failed: %v", err)
	}
	if cand.PartName != "RP2040" || cand.LCSCPart != "C2040" || cand.PinCount != 1 || cand.PadCount != 1 {
		t.Fatalf("unexpected LCSC candidate: %+v", cand)
	}
	if len(cand.Models3D) != 1 || cand.Models3D[0].Format != "step" {
		t.Fatalf("expected downloaded STEP model, got %+v", cand.Models3D)
	}
}

func TestScanLocalGitRepo(t *testing.T) {
	tmp := t.TempDir()
	repoSrc := filepath.Join(tmp, "remote-repo")
	if err := os.MkdirAll(filepath.Join(repoSrc, "Parts.pretty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoSrc, "Parts.kicad_sym"), []byte(sampleTestSym), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoSrc, "Parts.pretty", "QFN-16_3x3mm.kicad_mod"), []byte(sampleTestFP), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoSrc, "TPS62130.step"), []byte("ISO-10303-21;\nEND-ISO-10303-21;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repoSrc}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
	}
	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	runGit("add", ".")
	runGit("commit", "-m", "init parts")

	cacheDir := filepath.Join(tmp, "git-cache")
	cliPath, _ := exec.LookPath("kicad-cli")
	res, _, err := ScanGitRepo(context.Background(), "file://"+repoSrc, cacheDir, cliPath)
	if err != nil {
		t.Fatalf("ScanGitRepo failed: %v", err)
	}
	defer res.Cleanup()

	if len(res.Candidates) != 1 || res.Candidates[0].PartName != "TPS62130" {
		t.Fatalf("unexpected Git scan candidates: %+v", res.Candidates)
	}
	if len(res.Candidates[0].Models3D) != 1 {
		t.Fatalf("expected 1 3D model from Git repo, got %+v", res.Candidates[0].Models3D)
	}
}

func TestScanKiCadProjectSchematicAndPCB(t *testing.T) {
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "SensorBoard")
	modelDir := filepath.Join(projDir, "3d")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}

	stepPath := filepath.Join(modelDir, "stm32.step")
	if err := os.WriteFile(stepPath, []byte("ISO-10303-21;\nHEADER;\nENDSEC;\nEND-ISO-10303-21;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	proPath := filepath.Join(projDir, "SensorBoard.kicad_pro")
	if err := os.WriteFile(proPath, []byte(`{"meta":{"filename":"SensorBoard.kicad_pro","version":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	schContent := `(kicad_sch
	(version 20250114)
	(generator "eeschema")
	(lib_symbols
		(symbol "power:GND"
			(power)
			(property "Reference" "#PWR" (at 0 0 0))
			(property "Value" "GND" (at 0 0 0))
			(symbol "GND_1_1"
				(pin power_in line (at 0 0 0) (length 0) (name "GND") (number "1"))
			)
		)
		(symbol "Device:R_Custom"
			(property "Reference" "R" (at 0 2.54 0))
			(property "Value" "R_Custom" (at 0 -2.54 0))
			(property "Footprint" "" (at 0 0 0))
			(symbol "R_Custom_1_1"
				(pin passive line (at -2.54 0 0) (length 1.27) (name "1") (number "1"))
				(pin passive line (at 2.54 0 180) (length 1.27) (name "2") (number "2"))
			)
		)
		(symbol "MCU:STM32F405"
			(property "Reference" "U" (at 0 5.08 0))
			(property "Value" "STM32F405RGT6" (at 0 -5.08 0))
			(property "Footprint" "" (at 0 0 0))
			(symbol "MCU:STM32F405_1_1"
				(pin power_in line (at -5.08 0 0) (length 2.54) (name "VDD") (number "1"))
			)
		)
	)
	(symbol
		(lib_id "power:GND")
		(at 10 10 0)
		(property "Reference" "#PWR01" (at 0 0 0))
		(property "Value" "GND" (at 0 0 0))
	)
	(symbol
		(lib_id "Device:R_Custom")
		(at 20 20 0)
		(property "Reference" "R1" (at 0 0 0))
		(property "Value" "10k" (at 0 0 0))
		(property "Footprint" "Resistor_SMD:R_0603_1608Metric" (at 0 0 0))
		(property "MPN" "RC0603FR-0710KL" (at 0 0 0))
	)
	(symbol
		(lib_id "MCU:STM32F405")
		(at 50 50 0)
		(property "Reference" "U1" (at 0 0 0))
		(property "Value" "STM32F405RGT6" (at 0 0 0))
		(property "Footprint" "Package_QFP:LQFP-64_10x10mm_P0.5mm" (at 0 0 0))
		(property "LCSC" "C15742" (at 0 0 0))
	)
)
`
	schPath := filepath.Join(projDir, "SensorBoard.kicad_sch")
	if err := os.WriteFile(schPath, []byte(schContent), 0o644); err != nil {
		t.Fatal(err)
	}

	pcbContent := `(kicad_pcb
	(version 20241229)
	(generator "pcbnew")
	(net 1 "GND")
	(footprint "Resistor_SMD:R_0603_1608Metric"
		(layer "B.Cu")
		(uuid "fp-uuid-1")
		(at 100 50 90)
		(path "/sheet/r1")
		(sheetname "/")
		(sheetfile "SensorBoard.kicad_sch")
		(property "Reference" "R1" (at 0 -1.5 90) (layer "B.SilkS") (effects (font (size 1 1)) (justify mirror)))
		(property "Value" "10k" (at 0 1.5 90) (layer "B.Fab"))
		(fp_line (start -0.5 -0.4) (end 0.5 -0.4) (layer "B.SilkS"))
		(pad "1" smd roundrect (at -0.8 0 90) (size 0.8 0.9) (layers "B.Cu" "B.Mask" "B.Paste") (net 1 "GND") (uuid "pad-u1"))
		(pad "2" smd roundrect (at 0.8 0 90) (size 0.8 0.9) (layers "B.Cu" "B.Mask" "B.Paste") (uuid "pad-u2"))
	)
	(footprint "Package_QFP:LQFP-64_10x10mm_P0.5mm"
		(layer "F.Cu")
		(uuid "fp-uuid-2")
		(at 120 60 0)
		(path "/sheet/u1")
		(property "Reference" "U1" (at 0 -6 0) (layer "F.SilkS"))
		(property "Value" "STM32F405RGT6" (at 0 6 0) (layer "F.Fab"))
		(pad "1" smd rect (at -5.7 -3.75) (size 1.2 0.3) (layers "F.Cu" "F.Mask") (net 1 "GND"))
		(model "${KIPRJMOD}/3d/stm32.step" (offset (xyz 0 0 0)) (scale (xyz 1 1 1)) (rotate (xyz 0 0 0)))
	)
	(footprint "MountingHole:MountingHole_3.2mm_M3"
		(layer "F.Cu")
		(at 10 10)
		(property "Reference" "H1" (at 0 -3 0) (layer "F.SilkS"))
		(property "Value" "MountingHole" (at 0 3 0) (layer "F.Fab"))
		(pad "" np_thru_hole circle (at 0 0) (size 3.2 3.2) (drill 3.2) (layers "*.Cu" "*.Mask"))
	)
)
`
	pcbPath := filepath.Join(projDir, "SensorBoard.kicad_pcb")
	if err := os.WriteFile(pcbPath, []byte(pcbContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cliPath, _ := exec.LookPath("kicad-cli")

	// 1. Drop entire project directory (or .kicad_pro)
	for _, dropTarget := range []string{projDir, proPath} {
		res, err := ScanDroppedPaths([]string{dropTarget}, cliPath)
		if err != nil {
			t.Fatalf("ScanDroppedPaths(%s) failed: %v", dropTarget, err)
		}
		if res.VendorHint != "KiCad Project" {
			res.Cleanup()
			t.Fatalf("expected VendorHint=KiCad Project for %s, got %q", dropTarget, res.VendorHint)
		}
		// Expect 2 schematic parts (R_Custom, STM32F405 — GND filtered out) + 1 unpaired board footprint (MountingHole_3.2mm_M3)
		if len(res.Candidates) != 3 {
			res.Cleanup()
			t.Fatalf("expected 3 candidates for %s, got %d (%+v)", dropTarget, len(res.Candidates), res.Candidates)
		}

		byName := make(map[string]CandidatePart)
		for _, c := range res.Candidates {
			byName[c.PartName] = c
		}

		if _, hasGND := byName["GND"]; hasGND {
			res.Cleanup()
			t.Fatalf("expected power:GND symbol to be filtered out")
		}

		rCand, ok := byName["R_Custom"]
		if !ok {
			res.Cleanup()
			t.Fatalf("missing R_Custom candidate")
		}
		if rCand.MPN != "RC0603FR-0710KL" || rCand.FootprintName != "R_0603_1608Metric" {
			res.Cleanup()
			t.Fatalf("unexpected R_Custom enrichment/pairing: %+v", rCand)
		}
		if strings.Contains(rCand.FootprintSExpr, "(net ") || strings.Contains(rCand.FootprintSExpr, "B.Cu") {
			res.Cleanup()
			t.Fatalf("expected extracted PCB footprint stripped of nets and normalized from B.Cu to F.Cu, got:\n%s", rCand.FootprintSExpr)
		}
		if _, err := kicad.ParseFootprintFile([]byte(rCand.FootprintSExpr)); err != nil {
			res.Cleanup()
			t.Fatalf("normalized R_Custom footprint invalid: %v", err)
		}
		if _, err := kicad.ParseSymbolLibrary([]byte(rCand.SymbolSExpr)); err != nil {
			res.Cleanup()
			t.Fatalf("extracted R_Custom symbol invalid: %v", err)
		}

		stmCand, ok := byName["STM32F405"]
		if !ok {
			res.Cleanup()
			t.Fatalf("missing STM32F405 candidate")
		}
		if stmCand.LCSCPart != "C15742" || stmCand.FootprintName != "LQFP-64_10x10mm_P0.5mm" || len(stmCand.Models3D) != 1 || stmCand.Models3D[0].FileName != "stm32.step" {
			res.Cleanup()
			t.Fatalf("unexpected STM32F405 candidate: %+v", stmCand)
		}

		if _, hasMH := byName["MountingHole_3.2mm_M3"]; !hasMH {
			res.Cleanup()
			t.Fatalf("expected unpaired board footprint MountingHole_3.2mm_M3 candidate")
		}
		res.Cleanup()
	}

	// 2. Drop standalone .kicad_sch
	schRes, err := ScanDroppedPaths([]string{schPath}, cliPath)
	if err != nil {
		t.Fatalf("ScanDroppedPaths(.kicad_sch) failed: %v", err)
	}
	if schRes.VendorHint != "KiCad Schematic" || len(schRes.Candidates) != 2 {
		schRes.Cleanup()
		t.Fatalf("expected 2 candidates with VendorHint=KiCad Schematic, got %q (%d)", schRes.VendorHint, len(schRes.Candidates))
	}
	schRes.Cleanup()

	// 3. Drop standalone .kicad_pcb (should also resolve ${KIPRJMOD}/3d/stm32.step via origDirs)
	pcbRes, err := ScanDroppedPaths([]string{pcbPath}, cliPath)
	if err != nil {
		t.Fatalf("ScanDroppedPaths(.kicad_pcb) failed: %v", err)
	}
	if pcbRes.VendorHint != "KiCad PCB" || len(pcbRes.Candidates) != 3 {
		pcbRes.Cleanup()
		t.Fatalf("expected 3 footprint candidates with VendorHint=KiCad PCB, got %q (%d)", pcbRes.VendorHint, len(pcbRes.Candidates))
	}
	foundStmModel := false
	for _, c := range pcbRes.Candidates {
		if c.FootprintName == "LQFP-64_10x10mm_P0.5mm" && len(c.Models3D) == 1 && c.Models3D[0].FileName == "stm32.step" {
			foundStmModel = true
		}
	}
	if !foundStmModel {
		pcbRes.Cleanup()
		t.Fatalf("expected ${KIPRJMOD}/3d/stm32.step attached when dropping standalone .kicad_pcb")
	}
	pcbRes.Cleanup()

	// 4. Smoke-check real KiCad 9/10 template project if present on this system
	arduinoDir := "/nix/store/vgw44aci72yf0ffk4w4ld823dshc4sl1-KiCad_template_dir/Arduino_Nano"
	if info, statErr := os.Stat(arduinoDir); statErr == nil && info.IsDir() {
		realRes, rerr := ScanDroppedPaths([]string{arduinoDir}, cliPath)
		if rerr != nil {
			t.Fatalf("ScanDroppedPaths(Arduino_Nano) failed: %v", rerr)
		}
		defer realRes.Cleanup()
		if realRes.VendorHint != "KiCad Project" {
			t.Fatalf("expected KiCad Project hint for Arduino_Nano, got %q", realRes.VendorHint)
		}
		if len(realRes.Candidates) == 0 {
			t.Fatalf("expected extracted candidates from Arduino_Nano, got 0")
		}
		for _, c := range realRes.Candidates {
			if c.SymbolSExpr != "" {
				if _, serr := kicad.ParseSymbolLibrary([]byte(c.SymbolSExpr)); serr != nil {
					t.Fatalf("invalid extracted symbol %q in Arduino_Nano: %v", c.PartName, serr)
				}
			}
			if c.FootprintSExpr != "" {
				if _, ferr := kicad.ParseFootprintFile([]byte(c.FootprintSExpr)); ferr != nil {
					t.Fatalf("invalid extracted footprint %q in Arduino_Nano: %v", c.FootprintName, ferr)
				}
			}
		}
		t.Logf("Arduino_Nano template scan OK: %d candidates, %d footprints, warnings=%v",
			len(realRes.Candidates), len(realRes.AllFootprints), realRes.Warnings)
	}
}

func TestScanDroppedArchivesDeduplicatesModernAndLegacyAndCopies(t *testing.T) {
	tmp := t.TempDir()

	// Zip with both modern .kicad_sym and legacy .lib
	zip1Path := filepath.Join(tmp, "part.zip")
	createZip(t, zip1Path, map[string][]byte{
		"TPS62130/KiCad/TPS62130.kicad_sym": []byte(sampleTestSym),
		"TPS62130/KiCad/TPS62130.lib":       []byte(sampleLegacyV5Lib),
		"TPS62130/KiCad/QFN-16.kicad_mod":   []byte(sampleTestFP),
	})

	// Duplicate copy of the same zip
	zip2Path := filepath.Join(tmp, "part copy.zip")
	data, err := os.ReadFile(zip1Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zip2Path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ScanDroppedPaths([]string{zip1Path, zip2Path}, "")
	if err != nil {
		t.Fatalf("ScanDroppedPaths failed: %v", err)
	}
	defer res.Cleanup()

	// Should be deduplicated to exactly 1 candidate part
	if len(res.Candidates) != 1 {
		var names []string
		for _, c := range res.Candidates {
			names = append(names, c.PartName)
		}
		t.Fatalf("expected 1 deduplicated candidate, got %d: %v", len(res.Candidates), names)
	}
	if res.Candidates[0].PartName != "TPS62130" {
		t.Fatalf("expected candidate name TPS62130, got %q", res.Candidates[0].PartName)
	}
}

