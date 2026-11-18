package kicad

import (
	"archive/zip"
	"compress/gzip"
	"image"
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

func countNonBackgroundPixels(img *image.RGBA) int {
	b := img.Bounds()
	bg := img.RGBAAt(0, 0)
	diff := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if c != bg {
				diff++
			}
		}
	}
	return diff
}

func TestRender2DFootprintAndSymbol(t *testing.T) {
	fpWithGraphics := sampleFootprint[:len(sampleFootprint)-2] + `
	(fp_line (start -3 -2.5) (end 3 -2.5) (layer "F.SilkS") (width 0.12))
	(fp_rect (start -3.5 -3) (end 3.5 3) (layer "F.CrtYd") (width 0.05))
)`
	fpDraw, err := ParseFootprintDrawing(fpWithGraphics)
	if err != nil {
		t.Fatalf("ParseFootprintDrawing failed: %v", err)
	}
	if len(fpDraw.Pads) != 2 || len(fpDraw.Lines) != 1 || len(fpDraw.Rects) != 1 {
		t.Fatalf("unexpected footprint primitives: pads=%d lines=%d rects=%d", len(fpDraw.Pads), len(fpDraw.Lines), len(fpDraw.Rects))
	}
	fpImg := RenderDrawing2D(fpDraw, 320, 240, View2D{Zoom: 1.5, PanX: 10, PanY: -5})
	if countNonBackgroundPixels(fpImg) < 50 {
		t.Fatalf("expected rendered footprint pixels, got too few")
	}

	symDraw, err := ParseSymbolDrawing(sampleSymbolLib)
	if err != nil {
		t.Fatalf("ParseSymbolDrawing failed: %v", err)
	}
	if len(symDraw.Pins) != 2 || len(symDraw.Rects) != 1 {
		t.Fatalf("unexpected symbol primitives: pins=%d rects=%d", len(symDraw.Pins), len(symDraw.Rects))
	}
	symImg := RenderDrawing2D(symDraw, 320, 240, DefaultView2D())
	if countNonBackgroundPixels(symImg) < 50 {
		t.Fatalf("expected rendered symbol pixels, got too few")
	}
}

func TestLoadAndRenderMesh3DSTEPAndWRL(t *testing.T) {
	stepSrc := `ISO-10303-21;
HEADER;
ENDSEC;
DATA;
#1 = CARTESIAN_POINT('',(0.,0.,0.));
#2 = CARTESIAN_POINT('',(4.,0.,0.));
#3 = CARTESIAN_POINT('',(4.,3.,0.));
#4 = CARTESIAN_POINT('',(0.,3.,0.));
#5 = VERTEX_POINT('',#1);
#6 = VERTEX_POINT('',#2);
#7 = VERTEX_POINT('',#3);
#8 = VERTEX_POINT('',#4);
#9 = DIRECTION('',(1.,0.,0.));
#10 = DIRECTION('',(0.,1.,0.));
#11 = VECTOR('',#9,1.);
#12 = VECTOR('',#10,1.);
#13 = LINE('',#1,#11);
#14 = LINE('',#2,#12);
#15 = LINE('',#3,#11);
#16 = LINE('',#4,#12);
#17 = EDGE_CURVE('',#5,#6,#13,.T.);
#18 = EDGE_CURVE('',#6,#7,#14,.T.);
#19 = EDGE_CURVE('',#7,#8,#15,.T.);
#20 = EDGE_CURVE('',#8,#5,#16,.T.);
#21 = ORIENTED_EDGE('',*,*,#17,.T.);
#22 = ORIENTED_EDGE('',*,*,#18,.T.);
#23 = ORIENTED_EDGE('',*,*,#19,.T.);
#24 = ORIENTED_EDGE('',*,*,#20,.T.);
#25 = EDGE_LOOP('',(#21,#22,#23,#24));
#26 = FACE_OUTER_BOUND('',#25,.T.);
#27 = DIRECTION('',(0.,0.,1.));
#28 = AXIS2_PLACEMENT_3D('',#1,#27,#9);
#29 = PLANE('',#28);
#30 = ADVANCED_FACE('',(#26),#29,.T.);
#31 = COLOUR_RGB('',0.2,0.6,0.9);
#32 = FILL_AREA_STYLE_COLOUR('',#31);
#33 = FILL_AREA_STYLE('',(#32));
#34 = SURFACE_STYLE_FILL_AREA(#33);
#35 = SURFACE_SIDE_STYLE('',(#34));
#36 = SURFACE_STYLE_USAGE(.BOTH.,#35);
#37 = PRESENTATION_STYLE_ASSIGNMENT((#36));
#38 = STYLED_ITEM('color',(#37),#30);
ENDSEC;
END-ISO-10303-21;`

	tmp := t.TempDir()
	stepPath := filepath.Join(tmp, "box.step")
	if err := os.WriteFile(stepPath, []byte(stepSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	stpzPath := filepath.Join(tmp, "box.stpZ")
	f, err := os.Create(stpzPath)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	_, _ = gw.Write([]byte(stepSrc))
	_ = gw.Close()
	_ = f.Close()

	stpzZipPath := filepath.Join(tmp, "box_zip.stpZ")
	zf, err := os.Create(stpzZipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	zwEntry, err := zw.Create("box.step")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = zwEntry.Write([]byte(stepSrc))
	_ = zw.Close()
	_ = zf.Close()

	// Verify sibling .wrl -> .stpZ fallback resolution
	if resolved := Resolve3DModelPath(filepath.Join(tmp, "box_zip.wrl")); resolved != stpzZipPath {
		t.Fatalf("expected Resolve3DModelPath to resolve box_zip.wrl -> %s, got %s", stpzZipPath, resolved)
	}

	for _, p := range []string{stepPath, stpzPath, stpzZipPath} {
		mesh, err := LoadMesh3D(p)
		if err != nil {
			t.Fatalf("LoadMesh3D(%s) failed: %v", p, err)
		}
		if len(mesh.Triangles) != 2 || len(mesh.Edges) < 4 {
			t.Fatalf("expected 2 triangles and >=4 edges for %s, got tris=%d edges=%d", p, len(mesh.Triangles), len(mesh.Edges))
		}
		img := RenderMesh3D(mesh, 320, 240, Camera3D{Yaw: 20, Pitch: 45, Zoom: 1.4, PanX: 5, PanY: -5})
		if countNonBackgroundPixels(img) < 50 {
			t.Fatalf("expected rendered 3D pixels for %s", p)
		}
	}

	wrlSrc := `#VRML V2.0 utf8
Shape {
  appearance Appearance { material DEF CAP-CERAMIC-06 Material { diffuseColor 0.379 0.270 0.215 } }
}
Shape { geometry IndexedFaceSet
  { creaseAngle 0.50 coordIndex [ 0, 1, 2, -1, 0, 2, 3, -1 ]
    coord Coordinate { point [ 0 0 0, 1 0 0, 1 1 0, 0 1 0 ] }
  }
  appearance Appearance{material USE CAP-CERAMIC-06 }
}`
	wrlPath := filepath.Join(tmp, "part.wrl")
	if err := os.WriteFile(wrlPath, []byte(wrlSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	wrlMesh, err := LoadMesh3D(wrlPath)
	if err != nil {
		t.Fatalf("LoadMesh3D(wrl) failed: %v", err)
	}
	if len(wrlMesh.Triangles) != 2 {
		t.Fatalf("expected 2 VRML triangles, got %d", len(wrlMesh.Triangles))
	}
	if wrlMesh.Triangles[0].Color.R < 80 {
		t.Fatalf("expected DEF/USE material color applied, got %+v", wrlMesh.Triangles[0].Color)
	}
	wrlImg := RenderMesh3D(wrlMesh, 320, 240, DefaultCamera3D())
	if countNonBackgroundPixels(wrlImg) < 50 {
		t.Fatalf("expected rendered VRML pixels")
	}

	// Verify real KiCad .stpZ / .step / .wrl models if present on this machine
	var realModels []string
	if home, herr := os.UserHomeDir(); herr == nil {
		realModels = append(realModels,
			filepath.Join(home, ".local/share/kicad/10.0/3rdparty/3dmodels/com_github_sparkfun_SparkFun-KiCad-Libraries/Sensor.3dshapes/BMV080.step"),
			filepath.Join(home, ".local/share/kicad/10.0/3rdparty/3dmodels/com_github_Sajitha-Aldeniya_KiCad-Simple-Libraries/SL_Devices.3dshapes/Cap_SMD_0603.wrl"),
			filepath.Join(home, ".local/share/kicad/10.0/3rdparty/3dmodels/com_github_Sajitha-Aldeniya_KiCad-Simple-Libraries/SL_Development_Boards.3dshapes/ArduinoUNO_3D.wrl"),
		)
	}
	if matches, _ := filepath.Glob("/nix/store/*kicad-packages3d*/share/kicad/3dmodels/Resistor_THT.3dshapes/R_Axial_DIN0207_L6.3mm_D2.5mm_P10.16mm_Horizontal.stpZ"); len(matches) > 0 {
		realModels = append(realModels, matches[0])
	}
	if matches, _ := filepath.Glob("/nix/store/*kicad-packages3d*/share/kicad/3dmodels/Package_TO_SOT_SMD.3dshapes/SOT-23-5.stpZ"); len(matches) > 0 {
		realModels = append(realModels, matches[0])
	}
	for _, rp := range realModels {
		if _, err := os.Stat(rp); err == nil {
			m, err := LoadMesh3D(rp)
			if err != nil {
				t.Fatalf("LoadMesh3D(%s) failed: %v", rp, err)
			}
			if len(m.Triangles) == 0 {
				t.Fatalf("expected triangles from %s, got 0", rp)
			}
			rImg := RenderMesh3D(m, 400, 300, DefaultCamera3D())
			if countNonBackgroundPixels(rImg) < 100 {
				t.Fatalf("expected rendered pixels from %s", rp)
			}
		}
	}
}
