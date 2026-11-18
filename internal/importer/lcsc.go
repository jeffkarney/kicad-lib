package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	easyedaProductEndpoint = "https://easyeda.com/api/products/%s/components?version=6.4.19.5"
	easyeda3DSTEPEndpoint  = "https://modules.easyeda.com/qAxj6KHrDKw4blvCG8QJPs7Y/%s"
)

// easyedaComponentResponse models the payload returned by EasyEDA's product components API.
type easyedaComponentResponse struct {
	Success bool `json:"success"`
	Result  struct {
		UUID          string `json:"uuid"`
		Title         string `json:"title"`
		Description   string `json:"description"`
		DataStr       easyedaDataStr `json:"dataStr"`
		PackageDetail *struct {
			Title   string         `json:"title"`
			DataStr easyedaDataStr `json:"dataStr"`
		} `json:"packageDetail"`
	} `json:"result"`
}

type easyedaDataStr struct {
	Head struct {
		X     float64           `json:"x"`
		Y     float64           `json:"y"`
		CPara map[string]string `json:"c_para"`
	} `json:"head"`
	Shape []string `json:"shape"`
}

// FetchLCSCPart fetches symbol, footprint, 3D STEP model, and parametric metadata for an LCSC part number (e.g. "C2040").
func FetchLCSCPart(ctx context.Context, lcscID string, workDir string) (*CandidatePart, error) {
	return fetchLCSCPartFromBase(ctx, lcscID, workDir, easyedaProductEndpoint, easyeda3DSTEPEndpoint)
}

func fetchLCSCPartFromBase(ctx context.Context, lcscID, workDir, productURLFmt, stepURLFmt string) (*CandidatePart, error) {
	normID := normalizeLCSCID(lcscID)
	if normID == "" {
		return nil, errors.New("invalid LCSC part number (expected e.g. C2040)")
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
	}

	client := &http.Client{Timeout: 25 * time.Second}
	reqURL := fmt.Sprintf(productURLFmt, normID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "kicad-lib-manager/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request EasyEDA API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("EasyEDA API returned HTTP %d for %s", resp.StatusCode, normID)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}

	var parsed easyedaComponentResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode EasyEDA response: %w", err)
	}
	if !parsed.Success || parsed.Result.Title == "" {
		return nil, fmt.Errorf("LCSC part %s not found in EasyEDA catalog", normID)
	}

	cPara := parsed.Result.DataStr.Head.CPara
	partName := sanitizePartToken(firstNonEmpty(cPara["Manufacturer Part"], parsed.Result.Title, normID))
	mpn := firstNonEmpty(cPara["Manufacturer Part"], parsed.Result.Title)
	mfg := firstNonEmpty(cPara["Manufacturer"], cPara["BOM_Manufacturer"])
	pkg := firstNonEmpty(cPara["package"], "")
	if pkg == "" && parsed.Result.PackageDetail != nil {
		pkg = parsed.Result.PackageDetail.Title
	}
	datasheet := firstNonEmpty(cPara["link"], cPara["Datasheet"])
	prefix := strings.TrimSuffix(firstNonEmpty(cPara["pre"], "U"), "?")
	if prefix == "" {
		prefix = "U"
	}

	symSExpr, pinCount := convertEasyEDASymbol(partName, prefix, datasheet, parsed.Result.Description, parsed.Result.DataStr)

	var fpName, fpSExpr string
	var padCount int
	var models3D []CandidateModel3D
	if parsed.Result.PackageDetail != nil {
		fpName = sanitizePartToken(firstNonEmpty(parsed.Result.PackageDetail.Title, pkg, partName+"_FP"))
		var stepUUID string
		fpSExpr, padCount, stepUUID = convertEasyEDAFootprint(fpName, parsed.Result.PackageDetail.DataStr)

		if stepUUID != "" && workDir != "" {
			if stepPath, err := downloadEasyEDASTEP(ctx, client, fmt.Sprintf(stepURLFmt, stepUUID), workDir, fpName+".step"); err == nil {
				models3D = append(models3D, CandidateModel3D{
					SourcePath: stepPath,
					FileName:   filepath.Base(stepPath),
					Format:     "step",
				})
			}
		}
	}

	meta := make(map[string]string)
	for k, v := range cPara {
		if strings.TrimSpace(v) == "" {
			continue
		}
		meta[k] = v
	}
	meta["LCSC"] = normID
	if pkg != "" {
		meta["Package"] = pkg
	}
	if mpn != "" {
		meta["MPN"] = mpn
	}
	if mfg != "" {
		meta["Manufacturer"] = mfg
	}

	return &CandidatePart{
		Selected:       true,
		PartName:       partName,
		Value:          partName,
		Description:    firstNonEmpty(parsed.Result.Description, cPara["Description"]),
		Datasheet:      datasheet,
		MPN:            mpn,
		Manufacturer:   mfg,
		LCSCPart:       normID,
		SourceType:     "lcsc",
		SourceRef:      normID,
		SymbolName:     partName,
		SymbolSExpr:    symSExpr,
		PinCount:       pinCount,
		FootprintName:  fpName,
		FootprintSExpr: fpSExpr,
		PadCount:       padCount,
		Models3D:       models3D,
		Metadata:       meta,
	}, nil
}

func normalizeLCSCID(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "C") {
		s = "C" + s
	}
	if len(s) < 2 {
		return ""
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}

func downloadEasyEDASTEP(ctx context.Context, client *http.Client, stepURL, workDir, filename string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, stepURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "kicad-lib-manager/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("STEP download HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", err
	}
	if len(data) < 20 || !strings.Contains(string(data[:min(256, len(data))]), "ISO-10303-21") {
		return "", errors.New("response is not a valid STEP model")
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(workDir, filename)
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return "", err
	}
	return dst, nil
}

// EasyEDA unit conversion: 1 EasyEDA unit = 10 mil = 0.254 mm.
func eedaToMM(v, origin float64) float64 {
	return (v - origin) * 0.254
}

func convertEasyEDASymbol(name, refPrefix, datasheet, descr string, ds easyedaDataStr) (string, int) {
	ox, oy := ds.Head.X, ds.Head.Y
	var shapes []string
	var pins []string

	for _, rawShape := range ds.Shape {
		parts := strings.Split(rawShape, "~")
		if len(parts) == 0 {
			continue
		}
		switch parts[0] {
		case "R": // Rectangle: R~x~y~rx~ry~w~h~...
			if len(parts) >= 7 {
				x := eedaToMM(parseFloat(parts[1]), ox)
				y := -eedaToMM(parseFloat(parts[2]), oy)
				w := parseFloat(parts[5]) * 0.254
				h := parseFloat(parts[6]) * 0.254
				shapes = append(shapes, fmt.Sprintf("\t\t\t(rectangle (start %.3f %.3f) (end %.3f %.3f) (stroke (width 0.254) (type default)) (fill (type background)))",
					x, y, x+w, y-h))
			}
		case "PL", "PG": // Polyline / Polygon: PL~points~...
			if len(parts) >= 2 {
				coords := strings.Fields(strings.ReplaceAll(parts[1], ",", " "))
				if len(coords) >= 4 {
					var pts []string
					for i := 0; i+1 < len(coords); i += 2 {
						px := eedaToMM(parseFloat(coords[i]), ox)
						py := -eedaToMM(parseFloat(coords[i+1]), oy)
						pts = append(pts, fmt.Sprintf("(xy %.3f %.3f)", px, py))
					}
					if parts[0] == "PG" && len(pts) > 2 {
						pts = append(pts, pts[0])
					}
					shapes = append(shapes, fmt.Sprintf("\t\t\t(polyline (pts %s) (stroke (width 0.254) (type default)) (fill (type none)))",
						strings.Join(pts, " ")))
				}
			}
		case "P": // Pin: P~show~electric~spicePin~x~y~rot~id~...^^...^^M x y h/v len^^#000~name...^^#000~num...
			segs := strings.Split(rawShape, "^^")
			header := strings.Split(segs[0], "~")
			if len(header) >= 7 {
				px := eedaToMM(parseFloat(header[4]), ox)
				py := -eedaToMM(parseFloat(header[5]), oy)
				rot := int(parseFloat(header[6])) % 360
				if rot < 0 {
					rot += 360
				}
				pinNum := header[3]
				pinName := pinNum
				if len(segs) >= 4 {
					nf := strings.Split(segs[3], "~")
					if len(nf) >= 5 && nf[4] != "" {
						pinName = nf[4]
					}
				}
				if len(segs) >= 5 {
					nf := strings.Split(segs[4], "~")
					if len(nf) >= 5 && nf[4] != "" {
						pinNum = nf[4]
					}
				}
				if pinNum == "" {
					pinNum = fmt.Sprintf("%d", len(pins)+1)
				}
				if pinName == "" {
					pinName = pinNum
				}
				pins = append(pins, fmt.Sprintf("\t\t\t(pin passive line (at %.3f %.3f %d) (length 2.54) (name %q (effects (font (size 1.27 1.27)))) (number %q (effects (font (size 1.27 1.27)))))",
					px, py, rot, pinName, pinNum))
			}
		}
	}

	// Provide a clean fallback rectangle if symbol shapes were sparse
	if len(shapes) == 0 {
		shapes = append(shapes, "\t\t\t(rectangle (start -5.08 5.08) (end 5.08 -5.08) (stroke (width 0.254) (type default)) (fill (type background)))")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "\t(symbol %q\n", name)
	sb.WriteString("\t\t(in_bom yes)\n\t\t(on_board yes)\n")
	fmt.Fprintf(&sb, "\t\t(property \"Reference\" %q (at 0 5.08 0) (effects (font (size 1.27 1.27))))\n", refPrefix)
	fmt.Fprintf(&sb, "\t\t(property \"Value\" %q (at 0 -5.08 0) (effects (font (size 1.27 1.27))))\n", name)
	fmt.Fprintf(&sb, "\t\t(property \"Footprint\" \"\" (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))\n")
	fmt.Fprintf(&sb, "\t\t(property \"Datasheet\" %q (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))\n", datasheet)
	if descr != "" {
		fmt.Fprintf(&sb, "\t\t(property \"Description\" %q (at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))\n", descr)
	}
	fmt.Fprintf(&sb, "\t\t(symbol \"%s_0_1\"\n%s\n\t\t)\n", name, strings.Join(shapes, "\n"))
	if len(pins) > 0 {
		fmt.Fprintf(&sb, "\t\t(symbol \"%s_1_1\"\n%s\n\t\t)\n", name, strings.Join(pins, "\n"))
	}
	sb.WriteString("\t)\n")
	return sb.String(), len(pins)
}

func convertEasyEDAFootprint(name string, ds easyedaDataStr) (string, int, string) {
	ox, oy := ds.Head.X, ds.Head.Y
	var sb strings.Builder
	fmt.Fprintf(&sb, "(footprint %q\n", name)
	sb.WriteString("\t(layer \"F.Cu\")\n")
	sb.WriteString("\t(attr smd)\n")
	fmt.Fprintf(&sb, "\t(fp_text reference \"REF**\" (at 0 -3) (layer \"F.SilkS\") (effects (font (size 1 1) (thickness 0.15))))\n")
	fmt.Fprintf(&sb, "\t(fp_text value %q (at 0 3) (layer \"F.Fab\") (effects (font (size 1 1) (thickness 0.15))))\n", name)

	padCount := 0
	var stepUUID string

	for _, rawShape := range ds.Shape {
		parts := strings.Split(rawShape, "~")
		if len(parts) == 0 {
			continue
		}
		switch parts[0] {
		case "PAD": // PAD~shape~x~y~w~h~layerId~net~num~holeR~points~rot~id~holeLen~...
			if len(parts) >= 10 {
				shapeType := strings.ToUpper(parts[1])
				px := eedaToMM(parseFloat(parts[2]), ox)
				py := eedaToMM(parseFloat(parts[3]), oy)
				pw := parseFloat(parts[4]) * 0.254
				ph := parseFloat(parts[5]) * 0.254
				if pw <= 0 {
					pw = 0.6
				}
				if ph <= 0 {
					ph = 0.6
				}
				layerID := parts[6]
				padNum := parts[8]
				holeD := parseFloat(parts[9]) * 2 * 0.254
				rot := 0.0
				if len(parts) >= 12 {
					rot = parseFloat(parts[11])
				}

				kShape := "rect"
				switch shapeType {
				case "ELLIPSE":
					kShape = "circle"
				case "OVAL":
					kShape = "oval"
				case "RECT":
					kShape = "roundrect"
				}

				padKind := "smd"
				layers := `"F.Cu" "F.Mask" "F.Paste"`
				drillStr := ""
				if holeD > 0 || layerID == "11" {
					padKind = "thru_hole"
					layers = `"*.Cu" "*.Mask"`
					if holeD <= 0 {
						holeD = minFloat(pw, ph) * 0.6
					}
					drillStr = fmt.Sprintf(" (drill %.3f)", holeD)
				} else if layerID == "2" {
					layers = `"B.Cu" "B.Mask" "B.Paste"`
				}

				extra := ""
				if kShape == "roundrect" {
					extra = " (roundrect_rratio 0.15)"
				}
				fmt.Fprintf(&sb, "\t(pad %q %s %s (at %.3f %.3f %.1f) (size %.3f %.3f)%s (layers %s)%s)\n",
					padNum, padKind, kShape, px, py, rot, pw, ph, drillStr, layers, extra)
				padCount++
			}
		case "TRACK": // TRACK~width~layerId~net~points~id
			if len(parts) >= 5 {
				w := parseFloat(parts[1]) * 0.254
				if w <= 0 {
					w = 0.15
				}
				layer := easyedaLayerToKiCad(parts[2])
				coords := strings.Fields(strings.ReplaceAll(parts[4], ",", " "))
				for i := 0; i+3 < len(coords); i += 2 {
					x1 := eedaToMM(parseFloat(coords[i]), ox)
					y1 := eedaToMM(parseFloat(coords[i+1]), oy)
					x2 := eedaToMM(parseFloat(coords[i+2]), ox)
					y2 := eedaToMM(parseFloat(coords[i+3]), oy)
					fmt.Fprintf(&sb, "\t(fp_line (start %.3f %.3f) (end %.3f %.3f) (layer %q) (width %.3f))\n",
						x1, y1, x2, y2, layer, w)
				}
			}
		case "SVGNODE":
			// Embedded JSON containing 3D model UUID
			if idx := strings.Index(rawShape, "{"); idx >= 0 {
				var node struct {
					Attrs struct {
						UUID string `json:"uuid"`
					} `json:"attrs"`
				}
				if err := json.Unmarshal([]byte(rawShape[idx:]), &node); err == nil && node.Attrs.UUID != "" {
					stepUUID = node.Attrs.UUID
				}
			}
		}
	}

	sb.WriteString(")\n")
	return sb.String(), padCount, stepUUID
}

func easyedaLayerToKiCad(layerID string) string {
	switch layerID {
	case "3":
		return "F.SilkS"
	case "4":
		return "B.SilkS"
	case "10":
		return "Edge.Cuts"
	case "12":
		return "F.Fab"
	case "99":
		return "F.CrtYd"
	default:
		return "F.SilkS"
	}
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func sanitizePartToken(s string) string {
	repl := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return strings.TrimSpace(repl.Replace(s))
}
