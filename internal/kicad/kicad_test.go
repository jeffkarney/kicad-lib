package kicad

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.nanomsg.org/mangos/v3/protocol/rep"
	_ "go.nanomsg.org/mangos/v3/transport/ipc"
)

const sampleSymbolLib = `(kicad_symbol_lib
	(version 20231120)
	(generator "kicad_symbol_editor")
	(symbol "NE555"
		(in_bom yes)
		(on_board yes)
		(property "Reference" "U" (at 0 5.08 0) (effects (font (size 1.27 1.27))))
		(property "Value" "NE555" (at 0 -5.08 0) (effects (font (size 1.27 1.27))))
		(property "Footprint" "" (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))
		(property "Datasheet" "" (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))
		(symbol "NE555_0_1"
			(rectangle (start -5.08 5.08) (end 5.08 -5.08) (stroke (width 0.254)) (fill (type background)))
		)
		(symbol "NE555_1_1"
			(pin power_in line (at 0 7.62 270) (length 2.54) (name "VCC" (effects (font (size 1.27 1.27)))) (number "8" (effects (font (size 1.27 1.27)))))
			(pin power_in line (at 0 -7.62 90) (length 2.54) (name "GND" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
		)
	)
)
`

const sampleFootprint = `(footprint "SOIC-8_Vendor"
	(layer "F.Cu")
	(attr smd)
	(pad "1" smd roundrect (at -2.475 -1.905) (size 1.95 0.6) (layers "F.Cu" "F.Mask" "F.Paste") (roundrect_rratio 0.25))
	(pad "8" smd roundrect (at 2.475 -1.905) (size 1.95 0.6) (layers "F.Cu" "F.Mask" "F.Paste") (roundrect_rratio 0.25))
	(model "/tmp/old-vendor/SOIC-8.step"
		(offset (xyz 0 0 0.1))
		(scale (xyz 1 1 1))
		(rotate (xyz 0 0 90))
	)
)
`

func TestSymbolParsingPrepareAndBuild(t *testing.T) {
	syms, err := ParseSymbolLibrary([]byte(sampleSymbolLib))
	if err != nil {
		t.Fatalf("ParseSymbolLibrary failed: %v", err)
	}
	if len(syms) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(syms))
	}
	if syms[0].Name != "NE555" || syms[0].PinCount != 2 {
		t.Fatalf("unexpected parsed symbol: %+v", syms[0])
	}

	prepared, err := PrepareSymbolNode(
		syms[0].RawSExpr,
		"LM555",
		"Timers:SOIC-8",
		"LM555",
		"https://example.com/lm555.pdf",
		"Precision timer IC",
		"timer 555",
		map[string]string{"MPN": "LM555CMX", "Package": "SOIC-8"},
	)
	if err != nil {
		t.Fatalf("PrepareSymbolNode failed: %v", err)
	}

	if !strings.Contains(prepared, `"LM555_1_1"`) {
		t.Fatalf("expected sub-symbol renamed to LM555_1_1, got:\n%s", prepared)
	}
	if !strings.Contains(prepared, `(property "Footprint" "Timers:SOIC-8"`) {
		t.Fatalf("expected linked footprint property, got:\n%s", prepared)
	}
	if !strings.Contains(prepared, `(property "MPN" "LM555CMX"`) {
		t.Fatalf("expected custom MPN property, got:\n%s", prepared)
	}

	libContent := BuildSymbolLibraryFile([]string{prepared})
	reParsed, err := ParseSymbolLibrary([]byte(libContent))
	if err != nil {
		t.Fatalf("re-parse built symbol lib failed: %v", err)
	}
	if len(reParsed) != 1 || reParsed[0].Name != "LM555" || reParsed[0].Footprint != "Timers:SOIC-8" {
		t.Fatalf("round-trip mismatch: %+v", reParsed)
	}

	// Verify kicad-cli accepts our generated .kicad_sym if installed
	if cliPath, err := exec.LookPath("kicad-cli"); err == nil {
		tmp := t.TempDir()
		symFile := filepath.Join(tmp, "Timers.kicad_sym")
		if err := os.WriteFile(symFile, []byte(libContent), 0o644); err != nil {
			t.Fatal(err)
		}
		svgPath, err := ExportSymbolSVG(cliPath, symFile, "LM555", filepath.Join(tmp, "svg"))
		if err != nil {
			t.Fatalf("kicad-cli rejected generated symbol library: %v", err)
		}
		if _, err := os.Stat(svgPath); err != nil {
			t.Fatalf("expected rendered SVG at %s: %v", svgPath, err)
		}
	}
}

func TestFootprintParsingAndModelRewrite(t *testing.T) {
	pf, err := ParseFootprintFile([]byte(sampleFootprint))
	if err != nil {
		t.Fatalf("ParseFootprintFile failed: %v", err)
	}
	if pf.Name != "SOIC-8_Vendor" || pf.PadCount != 2 || len(pf.Models) != 1 {
		t.Fatalf("unexpected parsed footprint: %+v", pf)
	}

	managed3D := "/home/user/.local/share/kicad-lib/libraries/3dmodels/Timers.3dshapes/SOIC-8.step"
	rewritten, err := RewriteFootprint(pf.RawSExpr, "SOIC-8", []ModelTransform{{Path: managed3D}})
	if err != nil {
		t.Fatalf("RewriteFootprint failed: %v", err)
	}
	if !strings.Contains(rewritten, `(footprint "SOIC-8"`) {
		t.Fatalf("expected footprint rename, got:\n%s", rewritten)
	}
	if !strings.Contains(rewritten, managed3D) {
		t.Fatalf("expected rewritten 3D model path, got:\n%s", rewritten)
	}
	// Existing rotation (0 0 90) should be preserved when filename matches
	if !strings.Contains(rewritten, "(xyz 0 0 90)") {
		t.Fatalf("expected preserved rotation transform, got:\n%s", rewritten)
	}
}

func TestLibTableRegistrationIdempotentAndBackup(t *testing.T) {
	cfgDir := t.TempDir()
	symPath := "/tmp/libs/Timers.kicad_sym"
	fpPath := "/tmp/libs/Timers.pretty"

	if err := RegisterSymbolLibrary(cfgDir, "Timers", symPath, "Timer ICs"); err != nil {
		t.Fatalf("RegisterSymbolLibrary 1 failed: %v", err)
	}
	if err := RegisterSymbolLibrary(cfgDir, "Timers", symPath+".v2", "Timer ICs v2"); err != nil {
		t.Fatalf("RegisterSymbolLibrary 2 failed: %v", err)
	}
	if err := RegisterFootprintLibrary(cfgDir, "Timers", fpPath, "Timer Footprints"); err != nil {
		t.Fatalf("RegisterFootprintLibrary failed: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(cfgDir, "sym-lib-table"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), `(name "Timers")`) != 1 {
		t.Fatalf("expected single Timers entry, got:\n%s", string(raw))
	}
	if !strings.Contains(string(raw), symPath+".v2") {
		t.Fatalf("expected updated URI, got:\n%s", string(raw))
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "sym-lib-table.bak")); err != nil {
		t.Fatalf("expected .bak file to exist: %v", err)
	}
}

func TestIPCClientHandshakeMockSocket(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "kicad-test.sock")
	srv, err := rep.NewSocket()
	if err != nil {
		t.Fatalf("rep.NewSocket failed: %v", err)
	}
	defer srv.Close()

	if err := srv.Listen("ipc://" + sockPath); err != nil {
		t.Fatalf("srv.Listen failed: %v", err)
	}

	go func() {
		for {
			msg, err := srv.Recv()
			if err != nil || len(msg) == 0 {
				return
			}
			msgStr := string(msg)
			switch {
			case strings.Contains(msgStr, "GetPaths"):
				pathsPayload := EncodeGetPathsResponse(map[int32]string{
					PathStockSymbols:    "/opt/kicad/symbols",
					PathStockFootprints: "/opt/kicad/footprints",
				})
				_ = srv.Send(EncodeApiResponse("tok-123", ApiStatusOK, "", "type.googleapis.com/kiapi.common.commands.GetPathsResponse", pathsPayload))
			case strings.Contains(msgStr, "GetOpenDocuments"):
				_ = srv.Send(EncodeApiResponse("tok-123", ApiStatusOK, "", "type.googleapis.com/kiapi.common.commands.GetOpenDocumentsResponse", nil))
			default:
				verPayload := EncodeGetVersionResponse(10, 0, 8, "10.0.8")
				_ = srv.Send(EncodeApiResponse("tok-123", ApiStatusOK, "", "type.googleapis.com/kiapi.common.commands.GetVersionResponse", verPayload))
			}
		}
	}()

	client := NewIPCClient(sockPath)
	st := client.CheckStatus()
	if !st.Connected {
		t.Fatalf("expected Connected=true, got %+v", st)
	}
	if st.KiCadVersion != "10.0.8" {
		t.Fatalf("expected KiCadVersion=10.0.8, got %q", st.KiCadVersion)
	}
	if st.Paths[PathStockSymbols] != "/opt/kicad/symbols" {
		t.Fatalf("expected PathStockSymbols from IPC GetPaths, got %+v", st.Paths)
	}
}

func TestParseSymbolLibraryFastAndLibTableVars(t *testing.T) {
	fast, err := ParseSymbolLibraryFast([]byte(sampleSymbolLib))
	if err != nil {
		t.Fatalf("ParseSymbolLibraryFast failed: %v", err)
	}
	if len(fast) != 1 || fast[0].Name != "NE555" || fast[0].PinCount != 2 {
		t.Fatalf("unexpected fast parsed symbols: %+v", fast)
	}
	if !strings.Contains(fast[0].RawSExpr, `"NE555_1_1"`) {
		t.Fatalf("expected RawSExpr slice, got %q", fast[0].RawSExpr)
	}

	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "kicad")
	thirdParty := filepath.Join(tmp, "3rdparty")
	if err := os.MkdirAll(thirdParty, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KICAD10_3RD_PARTY", thirdParty)

	symTable := `(sym_lib_table
	(version 7)
	(lib (name "PCM_Test") (type "KiCad") (uri "${KICAD10_3RD_PARTY}/symbols/Test.kicad_sym") (options "") (descr "3rd party"))
)
`
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "sym-lib-table"), []byte(symTable), 0o644); err != nil {
		t.Fatal(err)
	}

	syms, _, vars, err := LoadEffectiveLibTables(cfgDir, "", nil, false)
	if err != nil {
		t.Fatalf("LoadEffectiveLibTables failed: %v", err)
	}
	if len(syms) != 1 || syms[0].ResolvedPath != filepath.Join(thirdParty, "symbols", "Test.kicad_sym") {
		t.Fatalf("unexpected resolved entries: %+v (vars=%+v)", syms, vars)
	}
}
