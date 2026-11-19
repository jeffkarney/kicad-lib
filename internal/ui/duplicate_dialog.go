package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jeffk/kicad-lib/internal/db"
	"github.com/jeffk/kicad-lib/internal/importer"
	"github.com/jeffk/kicad-lib/internal/kicad"
	"github.com/jeffk/kicad-lib/internal/storage"
)

// CandidateResolution tracks duplicate detection and user merge resolution for a staged candidate part.
type CandidateResolution struct {
	DuplicatePart *db.Part
	AllMatches    []db.Part
	Resolved      bool
	Options       storage.MergeOptions
}

// ShowDuplicateCompareDialog presents a side-by-side comparison between an incoming CandidatePart
// and one or more matching existing parts in the database, allowing the user to merge fields/assets,
// overwrite, rename, or skip the duplicate.
func ShowDuplicateCompareDialog(
	parent fyne.Window,
	mgr *storage.Manager,
	targetLibID int64,
	cand *importer.CandidatePart,
	matches []db.Part,
	initial storage.MergeOptions,
	onResolved func(selectedMatch db.Part, opts storage.MergeOptions, applyToAll bool),
) {
	if cand == nil || len(matches) == 0 {
		return
	}

	selectedIdx := 0
	if initial.Strategy == "" {
		initial.Strategy = "merge"
	}

	matchOptions := make([]string, len(matches))
	for i, m := range matches {
		sameLibTag := ""
		if targetLibID > 0 && m.LibraryID == targetLibID {
			sameLibTag = " [Same Target Lib]"
		}
		mpnTag := ""
		if m.MPN != "" {
			mpnTag = " | MPN: " + m.MPN
		}
		matchOptions[i] = fmt.Sprintf("[%s] %s%s%s", m.LibraryName, m.Name, mpnTag, sameLibTag)
	}

	headerBanner := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	headerBanner.Wrapping = fyne.TextWrapWord

	existingFieldsBox := container.NewVBox()
	incomingFieldsBox := container.NewVBox()
	metaDiffBox := container.NewVBox()

	// Visual Previews (Existing vs Incoming)
	exSymPreview := NewInteractivePreviewWidget(PreviewModeEmpty, fyne.NewSize(220, 180))
	inSymPreview := NewInteractivePreviewWidget(PreviewModeEmpty, fyne.NewSize(220, 180))
	exFpPreview := NewInteractivePreviewWidget(PreviewModeEmpty, fyne.NewSize(220, 180))
	inFpPreview := NewInteractivePreviewWidget(PreviewModeEmpty, fyne.NewSize(220, 180))
	ex3dPreview := NewInteractivePreviewWidget(PreviewModeEmpty, fyne.NewSize(220, 180))
	in3dPreview := NewInteractivePreviewWidget(PreviewModeEmpty, fyne.NewSize(220, 180))

	loadSymbolIntoPreview(inSymPreview, cand.SymbolSExpr, cand.SymbolSourcePath, cand.SymbolName)
	loadFootprintIntoPreview(inFpPreview, cand.FootprintSExpr, cand.FootprintSourcePath)
	if len(cand.Models3D) > 0 {
		loadModel3DAsync(in3dPreview, cand.Models3D[0].SourcePath, cand.Models3D[0].FileName)
	} else {
		in3dPreview.SetEmpty("No 3D model on incoming part")
	}

	// Resolution option widgets
	const (
		actMerge     = "Merge Selected Fields & Assets"
		actOverwrite = "Overwrite Existing Part"
		actRename    = "Import as New Part (Rename)"
		actSkip      = "Skip (Keep Existing Unchanged)"
	)
	const (
		detPreferInc = "Prefer Incoming (fill missing from Existing)"
		detKeepEx    = "Keep Existing (fill missing from Incoming)"
	)
	const (
		symKeepEx = "Keep Existing Symbol"
		symUseInc = "Use Incoming Symbol"
		fpKeepEx  = "Keep Existing Footprint"
		fpUseInc  = "Use Incoming Footprint"
	)
	const (
		m3Combine = "Combine Both (Union)"
		m3KeepEx  = "Keep Existing 3D Models"
		m3UseInc  = "Use Incoming 3D Models"
	)
	const (
		metaPreferInc = "Merge (Incoming overrides conflicts)"
		metaKeepEx    = "Merge (Keep existing on conflicts)"
		metaReplace   = "Replace with Incoming Metadata"
	)

	detailsRadio := widget.NewRadioGroup([]string{detPreferInc, detKeepEx}, nil)
	detailsRadio.Horizontal = true
	symRadio := widget.NewRadioGroup([]string{symKeepEx, symUseInc}, nil)
	symRadio.Horizontal = true
	fpRadio := widget.NewRadioGroup([]string{fpKeepEx, fpUseInc}, nil)
	fpRadio.Horizontal = true
	m3dSelect := widget.NewSelect([]string{m3Combine, m3KeepEx, m3UseInc}, nil)
	metaSelect := widget.NewSelect([]string{metaPreferInc, metaKeepEx, metaReplace}, nil)

	mergeOptionsCard := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Part Details", detailsRadio),
			widget.NewFormItem("Symbol", symRadio),
			widget.NewFormItem("Footprint", fpRadio),
			widget.NewFormItem("3D Models", m3dSelect),
			widget.NewFormItem("Metadata", metaSelect),
		),
	)

	renameEntry := widget.NewEntry()
	renameBox := container.NewVBox(
		widget.NewForm(widget.NewFormItem("New Part Name", renameEntry)),
	)

	actionRadio := widget.NewRadioGroup([]string{actMerge, actOverwrite, actRename, actSkip}, func(sel string) {
		switch sel {
		case actMerge:
			mergeOptionsCard.Show()
			renameBox.Hide()
		case actRename:
			mergeOptionsCard.Hide()
			renameBox.Show()
		default:
			mergeOptionsCard.Hide()
			renameBox.Hide()
		}
	})
	actionRadio.Horizontal = true

	renderComparison := func(idx int) {
		if idx < 0 || idx >= len(matches) {
			return
		}
		selectedIdx = idx
		ex := matches[idx]

		scope := "across database"
		if targetLibID > 0 && ex.LibraryID == targetLibID {
			scope = "in target library"
		}
		headerBanner.SetText(fmt.Sprintf("Duplicate detected %s: Existing [%s] %q vs Incoming %q", scope, ex.LibraryName, ex.Name, cand.PartName))

		// Update comparison fields
		existingFieldsBox.Objects = buildFieldComparisonRows(
			ex.LibraryName, ex.Name, ex.Value, ex.MPN, ex.Manufacturer, ex.LCSCPart, ex.Description, ex.Datasheet,
			formatSymSummaryDB(ex.Symbol), formatFPSummaryDB(ex.Footprint), formatModelsDB(ex.Models3D),
			nil,
		)
		existingFieldsBox.Refresh()

		incomingFieldsBox.Objects = buildFieldComparisonRows(
			"(Target Library)", cand.PartName, cand.Value, cand.MPN, cand.Manufacturer, cand.LCSCPart, cand.Description, cand.Datasheet,
			formatSymSummaryCand(cand), formatFPSummaryCand(cand), formatModelsCand(cand.Models3D),
			map[string]bool{
				"Name":         !strings.EqualFold(ex.Name, cand.PartName),
				"Value":        !strings.EqualFold(ex.Value, cand.Value) && cand.Value != "",
				"MPN":          !strings.EqualFold(ex.MPN, cand.MPN) && cand.MPN != "",
				"Manufacturer": !strings.EqualFold(ex.Manufacturer, cand.Manufacturer) && cand.Manufacturer != "",
				"LCSC ID":      !strings.EqualFold(ex.LCSCPart, cand.LCSCPart) && cand.LCSCPart != "",
				"Description":  !strings.EqualFold(ex.Description, cand.Description) && cand.Description != "",
				"Datasheet":    !strings.EqualFold(ex.Datasheet, cand.Datasheet) && cand.Datasheet != "",
				"Symbol":       (cand.SymbolSExpr != "") != (ex.Symbol != nil),
				"Footprint":    (ex.Footprint == nil && cand.FootprintName != "") || (ex.Footprint != nil && cand.FootprintName != "" && !strings.EqualFold(ex.Footprint.Name, cand.FootprintName)),
			},
		)
		incomingFieldsBox.Refresh()

		// Update Metadata Diff
		metaDiffBox.Objects = buildMetadataDiffRows(ex.Metadata, cand.Metadata)
		metaDiffBox.Refresh()

		// Update Existing Visual Previews
		if ex.Symbol != nil {
			loadSymbolIntoPreview(exSymPreview, ex.Symbol.RawSExpr, ex.Symbol.VaultPath, ex.Symbol.Name)
		} else {
			exSymPreview.SetEmpty("No symbol on existing part")
		}
		if ex.Footprint != nil {
			loadFootprintIntoPreview(exFpPreview, ex.Footprint.RawSExpr, ex.Footprint.VaultPath)
		} else {
			exFpPreview.SetEmpty("No footprint on existing part")
		}
		if len(ex.Models3D) > 0 {
			loadModel3DAsync(ex3dPreview, kicad.Resolve3DModelPath(ex.Models3D[0].VaultPath), ex.Models3D[0].FileName)
		} else {
			ex3dPreview.SetEmpty("No 3D model on existing part")
		}

		// Smart defaults for merge/rename controls
		if initial.UseIncomingDetails {
			detailsRadio.SetSelected(detPreferInc)
		} else {
			detailsRadio.SetSelected(detKeepEx)
		}
		if ex.Symbol == nil && (cand.SymbolSExpr != "" || cand.SymbolSourcePath != "") {
			symRadio.SetSelected(symUseInc)
		} else if initial.UseIncomingSymbol && (cand.SymbolSExpr != "" || cand.SymbolSourcePath != "") {
			symRadio.SetSelected(symUseInc)
		} else {
			symRadio.SetSelected(symKeepEx)
		}
		if ex.Footprint == nil && (cand.FootprintSExpr != "" || cand.FootprintSourcePath != "") {
			fpRadio.SetSelected(fpUseInc)
		} else if initial.UseIncomingFootprint && (cand.FootprintSExpr != "" || cand.FootprintSourcePath != "") {
			fpRadio.SetSelected(fpUseInc)
		} else {
			fpRadio.SetSelected(fpKeepEx)
		}

		switch strings.ToLower(initial.Model3DMode) {
		case "keep_existing":
			m3dSelect.SetSelected(m3KeepEx)
		case "use_incoming":
			m3dSelect.SetSelected(m3UseInc)
		default:
			m3dSelect.SetSelected(m3Combine)
		}

		switch strings.ToLower(initial.MetadataMode) {
		case "keep_existing":
			metaSelect.SetSelected(metaKeepEx)
		case "replace":
			metaSelect.SetSelected(metaReplace)
		default:
			metaSelect.SetSelected(metaPreferInc)
		}

		if strings.TrimSpace(initial.NewName) != "" {
			renameEntry.SetText(initial.NewName)
		} else if ex.LibraryID != targetLibID && !strings.EqualFold(ex.Name, cand.PartName) {
			renameEntry.SetText(cand.PartName)
		} else if ex.LibraryID != targetLibID {
			renameEntry.SetText(cand.PartName)
		} else {
			renameEntry.SetText(cand.PartName + "_2")
		}

		switch strings.ToLower(initial.Strategy) {
		case "overwrite":
			actionRadio.SetSelected(actOverwrite)
		case "rename":
			actionRadio.SetSelected(actRename)
		case "skip":
			actionRadio.SetSelected(actSkip)
		default:
			actionRadio.SetSelected(actMerge)
		}
	}

	matchSelector := widget.NewSelect(matchOptions, func(sel string) {
		for i, opt := range matchOptions {
			if opt == sel {
				renderComparison(i)
				break
			}
		}
	})
	matchSelector.SetSelectedIndex(0)

	topMatchBar := container.NewVBox(headerBanner)
	if len(matches) > 1 {
		topMatchBar.Add(widget.NewForm(widget.NewFormItem("Compare Against Existing Part", matchSelector)))
	}

	overviewTab := container.NewGridWithColumns(2,
		widget.NewCard("Existing Part in Database", "", container.NewVScroll(existingFieldsBox)),
		widget.NewCard("Incoming Part", "", container.NewVScroll(incomingFieldsBox)),
	)

	metadataTab := container.NewVScroll(metaDiffBox)

	symCompare := container.NewGridWithColumns(2,
		NewPreviewPanel(parent, "Existing Symbol", exSymPreview, false),
		NewPreviewPanel(parent, "Incoming Symbol", inSymPreview, false),
	)
	fpCompare := container.NewGridWithColumns(2,
		NewPreviewPanel(parent, "Existing Footprint", exFpPreview, false),
		NewPreviewPanel(parent, "Incoming Footprint", inFpPreview, false),
	)
	m3dCompare := container.NewGridWithColumns(2,
		NewPreviewPanel(parent, "Existing 3D Model", ex3dPreview, true),
		NewPreviewPanel(parent, "Incoming 3D Model", in3dPreview, true),
	)
	previewsTab := container.NewAppTabs(
		container.NewTabItem("Symbols", symCompare),
		container.NewTabItem("Footprints", fpCompare),
		container.NewTabItem("3D Models", m3dCompare),
	)

	compareTabs := container.NewAppTabs(
		container.NewTabItemWithIcon("Overview & Fields", theme.DocumentIcon(), overviewTab),
		container.NewTabItemWithIcon("Metadata Diff", theme.ListIcon(), metadataTab),
		container.NewTabItemWithIcon("Visual Previews", theme.VisibilityIcon(), previewsTab),
	)

	resolutionCard := widget.NewCard("Duplicate Resolution Action", "", container.NewVBox(
		actionRadio,
		mergeOptionsCard,
		renameBox,
	))

	var dlg dialog.Dialog
	collectOptions := func() (storage.MergeOptions, bool) {
		opts := storage.MergeOptions{}
		switch actionRadio.Selected {
		case actOverwrite:
			opts.Strategy = "overwrite"
		case actRename:
			opts.Strategy = "rename"
			opts.NewName = strings.TrimSpace(renameEntry.Text)
			if opts.NewName == "" {
				dialog.ShowInformation("Name Required", "Enter a new part name.", parent)
				return opts, false
			}
		case actSkip:
			opts.Strategy = "skip"
		default:
			opts.Strategy = "merge"
			opts.UseIncomingDetails = detailsRadio.Selected == detPreferInc
			opts.UseIncomingSymbol = symRadio.Selected == symUseInc
			opts.UseIncomingFootprint = fpRadio.Selected == fpUseInc
			switch m3dSelect.Selected {
			case m3KeepEx:
				opts.Model3DMode = "keep_existing"
			case m3UseInc:
				opts.Model3DMode = "use_incoming"
			default:
				opts.Model3DMode = "combine"
			}
			switch metaSelect.Selected {
			case metaKeepEx:
				opts.MetadataMode = "keep_existing"
			case metaReplace:
				opts.MetadataMode = "replace"
			default:
				opts.MetadataMode = "prefer_incoming"
			}
		}
		return opts, true
	}

	applyBtn := widget.NewButtonWithIcon("Apply Resolution", theme.ConfirmIcon(), func() {
		opts, ok := collectOptions()
		if !ok {
			return
		}
		dlg.Hide()
		if onResolved != nil {
			onResolved(matches[selectedIdx], opts, false)
		}
	})
	applyBtn.Importance = widget.HighImportance

	applyAllBtn := widget.NewButtonWithIcon("Apply to All Remaining", theme.ConfirmIcon(), func() {
		opts, ok := collectOptions()
		if !ok {
			return
		}
		dlg.Hide()
		if onResolved != nil {
			onResolved(matches[selectedIdx], opts, true)
		}
	})

	cancelBtn := widget.NewButton("Cancel", func() {
		dlg.Hide()
	})

	renderComparison(0)

	content := container.NewBorder(
		topMatchBar,
		container.NewVBox(
			resolutionCard,
			container.NewHBox(layout.NewSpacer(), cancelBtn, applyAllBtn, applyBtn),
		),
		nil, nil,
		compareTabs,
	)

	dlg = dialog.NewCustomWithoutButtons("Compare & Merge Duplicate Part", content, parent)
	dlgSize := fyne.NewSize(1100, 750)
	if parent != nil {
		pSize := parent.Canvas().Size()
		if pSize.Width > 0 && pSize.Height > 0 {
			targetW := pSize.Width * 0.90
			targetH := pSize.Height * 0.90
			if targetW > dlgSize.Width {
				dlgSize.Width = targetW
			}
			if targetH > dlgSize.Height {
				dlgSize.Height = targetH
			}
		}
	}
	if dlgSize.Width > 1500 {
		dlgSize.Width = 1500
	}
	if dlgSize.Height > 950 {
		dlgSize.Height = 950
	}
	dlg.Resize(dlgSize)
	dlg.Show()
}

func buildFieldComparisonRows(
	libName, name, val, mpn, mfg, lcsc, desc, ds, symSummary, fpSummary, m3Summary string,
	diffFlags map[string]bool,
) []fyne.CanvasObject {
	fields := []struct {
		label string
		val   string
	}{
		{"Library", libName},
		{"Part Name", name},
		{"Value", val},
		{"MPN", mpn},
		{"Manufacturer", mfg},
		{"LCSC ID", lcsc},
		{"Description", desc},
		{"Datasheet", ds},
		{"Symbol", symSummary},
		{"Footprint", fpSummary},
		{"3D Models", m3Summary},
	}

	var rows []fyne.CanvasObject
	for _, f := range fields {
		v := strings.TrimSpace(f.val)
		if v == "" {
			v = "-"
		}
		if diffFlags != nil {
			key := f.label
			if key == "Part Name" {
				key = "Name"
			}
			if diffFlags[key] {
				v = "[DIFF] " + v
			}
		}
		kLbl := widget.NewLabelWithStyle(f.label+":", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		vLbl := widget.NewLabel(v)
		vLbl.Wrapping = fyne.TextWrapWord
		rows = append(rows, container.NewBorder(nil, nil, kLbl, nil, vLbl))
	}
	return rows
}

func buildMetadataDiffRows(existingMeta, incomingMeta map[string]string) []fyne.CanvasObject {
	keySet := make(map[string]bool, len(existingMeta)+len(incomingMeta))
	for k := range existingMeta {
		keySet[k] = true
	}
	for k := range incomingMeta {
		keySet[k] = true
	}
	if len(keySet) == 0 {
		return []fyne.CanvasObject{widget.NewLabel("Neither the existing nor the incoming part has custom metadata.")}
	}

	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := []fyne.CanvasObject{
		container.NewGridWithColumns(4,
			widget.NewLabelWithStyle("Key", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle("Existing", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle("Incoming", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle("Status", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		),
		widget.NewSeparator(),
	}

	for _, k := range keys {
		ev, hasE := existingMeta[k]
		iv, hasI := incomingMeta[k]
		status := "IDENTICAL"
		switch {
		case !hasE && hasI:
			status = "NEW"
		case hasE && !hasI:
			status = "EXISTING ONLY"
		case ev != iv:
			status = "CHANGED"
		}
		if ev == "" {
			ev = "-"
		}
		if iv == "" {
			iv = "-"
		}
		kL := widget.NewLabelWithStyle(k, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		eL := widget.NewLabel(ev)
		iL := widget.NewLabel(iv)
		sL := widget.NewLabel(status)
		kL.Wrapping = fyne.TextWrapWord
		eL.Wrapping = fyne.TextWrapWord
		iL.Wrapping = fyne.TextWrapWord
		rows = append(rows, container.NewGridWithColumns(4, kL, eL, iL, sL))
	}
	return rows
}

func formatSymSummaryDB(s *db.Symbol) string {
	if s == nil {
		return "(none)"
	}
	return fmt.Sprintf("%s (%d pins)", s.Name, s.PinCount)
}

func formatSymSummaryCand(c *importer.CandidatePart) string {
	if c == nil || (c.SymbolSExpr == "" && c.SymbolSourcePath == "") {
		return "(none)"
	}
	return fmt.Sprintf("%s (%d pins)", c.SymbolName, c.PinCount)
}

func formatFPSummaryDB(fp *db.Footprint) string {
	if fp == nil {
		return "(none)"
	}
	return fmt.Sprintf("%s (%d pads)", fp.Name, fp.PadCount)
}

func formatFPSummaryCand(c *importer.CandidatePart) string {
	if c == nil || (c.FootprintName == "" && c.FootprintSExpr == "") {
		return "(none)"
	}
	return fmt.Sprintf("%s (%d pads)", c.FootprintName, c.PadCount)
}

func formatModelsDB(models []db.Model3D) string {
	if len(models) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, fmt.Sprintf("%s (%s)", m.FileName, strings.ToUpper(m.Format)))
	}
	return strings.Join(names, ", ")
}

func formatModelsCand(models []importer.CandidateModel3D) string {
	if len(models) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, fmt.Sprintf("%s (%s)", m.FileName, strings.ToUpper(m.Format)))
	}
	return strings.Join(names, ", ")
}

func loadSymbolIntoPreview(pw *InteractivePreviewWidget, rawSExpr, filePath, symName string) {
	if pw == nil {
		return
	}
	var d2 *kicad.Drawing2D
	var err error
	if filePath != "" && strings.Contains(rawSExpr, "(extends ") {
		d2, err = kicad.ParseSymbolDrawingFromFile(filePath, symName)
	}
	if d2 == nil && rawSExpr != "" {
		d2, err = kicad.ParseSymbolDrawing(rawSExpr)
	}
	if d2 == nil && filePath != "" {
		d2, err = kicad.ParseSymbolDrawingFromFile(filePath, symName)
	}
	if err == nil && d2 != nil {
		pw.SetDrawing2D(d2)
	} else {
		pw.SetEmpty("No symbol geometry")
	}
}

func loadFootprintIntoPreview(pw *InteractivePreviewWidget, rawSExpr, filePath string) {
	if pw == nil {
		return
	}
	raw := strings.TrimSpace(rawSExpr)
	if raw == "" && filePath != "" {
		if b, err := os.ReadFile(filePath); err == nil {
			raw = string(b)
		}
	}
	if raw == "" {
		pw.SetEmpty("No footprint geometry")
		return
	}
	d2, err := kicad.ParseFootprintDrawing(raw)
	if err == nil && d2 != nil {
		pw.SetDrawing2D(d2)
	} else {
		pw.SetEmpty("Could not parse footprint geometry")
	}
}

func loadModel3DAsync(pw *InteractivePreviewWidget, path, label string) {
	if pw == nil {
		return
	}
	resolved := kicad.Resolve3DModelPath(path)
	if resolved == "" {
		pw.SetEmpty("3D model path not set")
		return
	}
	if _, err := os.Stat(resolved); err != nil {
		pw.SetEmpty("3D model not found on disk:\n" + label)
		return
	}
	pw.SetEmpty("Loading 3D model " + filepath.Base(resolved) + "...")
	go func() {
		mesh, err := kicad.LoadMesh3D(resolved)
		fyne.Do(func() {
			if err != nil {
				pw.SetEmpty(fmt.Sprintf("Could not render %s:\n%v", filepath.Base(resolved), err))
				return
			}
			pw.SetMesh3D(mesh)
		})
	}()
}
