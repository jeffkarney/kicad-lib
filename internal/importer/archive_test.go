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
		"SamacSys_Part/KiCad/TPS62130.kicad_sym":                []byte(sampleTestSym),
		"SamacSys_Part/KiCad/SamacSys.pretty/QFN-16_3x3mm.kicad_mod": []byte(sampleTestFP),
		"SamacSys_Part/3D/TPS62130.step":                        []byte("ISO-10303-21;\nHEADER;\nENDSEC;\nEND-ISO-10303-21;\n"),
		"SamacSys_Part/Datasheet/TPS62130.pdf":                  []byte("%PDF-1.4\n"),
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
