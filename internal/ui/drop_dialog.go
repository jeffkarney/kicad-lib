package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jeffk/kicad-lib/internal/db"
	"github.com/jeffk/kicad-lib/internal/importer"
	"github.com/jeffk/kicad-lib/internal/storage"
)

// ShowDropStagingDialog presents discovered symbols, footprints, 3D models, and metadata
// from a dropped zip/folder/KiCad project/schematic/PCB, Git repo, or LCSC lookup, allowing the user
// to pick an existing KiCad library or create a new library, inspect & resolve duplicate parts,
// and import/merge & sync.
func ShowDropStagingDialog(parent fyne.Window, mgr *storage.Manager, res *importer.ScanResult, onImported func()) {
	if res == nil || len(res.Candidates) == 0 {
		if res != nil {
			res.Cleanup()
		}
		msg := "No KiCad symbols (.kicad_sym / .lib / .kicad_sch), footprints (.kicad_mod / .kicad_pcb), or KiCad projects (.kicad_pro) were found."
		if res != nil && len(res.Warnings) > 0 {
			msg += "\n\nWarnings:\n" + strings.Join(res.Warnings, "\n")
		}
		dialog.ShowInformation("Nothing to Import", msg, parent)
		return
	}

	stageWin := fyne.CurrentApp().NewWindow("Stage & Import Parts")
	defaultWidth := float32(1280)
	defaultHeight := float32(840)
	if parent != nil {
		pSize := parent.Canvas().Size()
		if pSize.Width > 0 && pSize.Height > 0 {
			targetW := pSize.Width * 0.85
			targetH := pSize.Height * 0.85
			if targetW > defaultWidth {
				defaultWidth = targetW
			}
			if targetH > defaultHeight {
				defaultHeight = targetH
			}
		}
	}
	if defaultWidth > 1600 {
		defaultWidth = 1600
	}
	if defaultHeight > 1050 {
		defaultHeight = 1050
	}
	stageWin.Resize(fyne.NewSize(defaultWidth, defaultHeight))
	stageWin.SetFixedSize(false)

	stageWin.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) {
		if ev.Name == fyne.KeyEscape {
			stageWin.Close()
		}
	})

	libs, _ := mgr.DB().ListLibraries()
	libIDByName := make(map[string]int64, len(libs))
	hasVisibleLib := false
	for _, l := range libs {
		libIDByName[l.Name] = l.ID
		if !l.Hidden {
			hasVisibleLib = true
		}
	}

	// Target library selection vs inline creation
	modeExisting := "Add to Existing Library"
	modeNew := "Create New Library"
	libFilterEntry := widget.NewEntry()
	libFilterEntry.SetPlaceHolder("Search target libraries...")
	showHiddenCheck := widget.NewCheck("Include hidden libraries", nil)
	if !hasVisibleLib && len(libs) > 0 {
		showHiddenCheck.SetChecked(true)
	}
	existingSelect := widget.NewSelect(nil, nil)
	existingSelect.PlaceHolder = "Select target library..."
	existingLibBox := container.NewVBox(
		container.NewBorder(nil, nil, nil, showHiddenCheck, libFilterEntry),
		existingSelect,
	)

	newLibNameEntry := widget.NewEntry()
	newLibNameEntry.SetPlaceHolder("New library name (e.g. Custom_ICs)")
	newLibDescEntry := widget.NewEntry()
	newLibDescEntry.SetPlaceHolder("Optional library description")

	newLibBox := container.NewVBox(newLibNameEntry, newLibDescEntry)
	targetModeRadio := widget.NewRadioGroup([]string{modeExisting, modeNew}, nil)
	if len(libs) > 0 {
		targetModeRadio.SetSelected(modeExisting)
		newLibBox.Hide()
	} else {
		targetModeRadio.SetSelected(modeNew)
		existingLibBox.Hide()
	}

	currentTargetLibID := func() int64 {
		if targetModeRadio.Selected == modeNew {
			return libIDByName[strings.TrimSpace(newLibNameEntry.Text)]
		}
		return libIDByName[existingSelect.Selected]
	}

	// Duplicate tracking per candidate
	resolutions := make([]CandidateResolution, len(res.Candidates))

	// Duplicate notification banner & per-candidate status bar
	dupBannerLbl := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	dupBannerLbl.Wrapping = fyne.TextWrapWord
	dupBannerBg := canvas.NewRectangle(theme.Color(theme.ColorNameWarning))
	dupBannerBg.CornerRadius = 4

	var candDupBtn *widget.Button
	dupBannerCompareBtn := widget.NewButton("Compare Active Duplicate...", nil)
	dupBanner := container.NewStack(
		dupBannerBg,
		container.NewPadded(container.NewBorder(nil, nil, widget.NewIcon(theme.WarningIcon()), dupBannerCompareBtn, dupBannerLbl)),
	)
	dupBanner.Hide()

	candDupStatusLbl := widget.NewLabel("")
	candDupStatusLbl.Wrapping = fyne.TextWrapWord
	candDupBtn = widget.NewButtonWithIcon("Compare & Merge Duplicate...", theme.WarningIcon(), nil)
	candDupBar := container.NewBorder(nil, nil, nil, candDupBtn, candDupStatusLbl)
	candDupBar.Hide()

	// Candidate selection & active candidate editor
	activeIdx := 0

	nameEntry := widget.NewEntry()
	valEntry := widget.NewEntry()
	mpnEntry := widget.NewEntry()
	mfgEntry := widget.NewEntry()
	lcscEntry := widget.NewEntry()
	dsEntry := widget.NewEntry()
	descEntry := widget.NewEntry()

	symSummaryLbl := widget.NewLabel("")
	fpOptions := make([]string, 0, len(res.AllFootprints)+1)
	fpOptions = append(fpOptions, "(None)")
	for _, fp := range res.AllFootprints {
		fpOptions = append(fpOptions, fp.Name)
	}
	fpSelect := widget.NewSelect(fpOptions, nil)
	modelsLbl := widget.NewLabel("")
	modelsLbl.Wrapping = fyne.TextWrapWord

	metaPreviewLbl := widget.NewLabel("")
	metaPreviewLbl.Wrapping = fyne.TextWrapWord
	metaKeyEntry := widget.NewEntry()
	metaKeyEntry.SetPlaceHolder("Custom Metadata Key")
	metaValEntry := widget.NewEntry()
	metaValEntry.SetPlaceHolder("Value")

	var candList *widget.List
	var suppressSync bool

	refreshMetaPreview := func(c *importer.CandidatePart) {
		if len(c.Metadata) == 0 {
			metaPreviewLbl.SetText("(no custom metadata)")
			return
		}
		keys := make([]string, 0, len(c.Metadata))
		for k := range c.Metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var lines []string
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("%s = %s", k, c.Metadata[k]))
		}
		metaPreviewLbl.SetText(strings.Join(lines, "\n"))
	}

	updateActiveCandidateDupBar := func() {
		if activeIdx < 0 || activeIdx >= len(resolutions) {
			candDupBar.Hide()
			return
		}
		r := resolutions[activeIdx]
		if len(r.AllMatches) == 0 {
			candDupBar.Hide()
			return
		}
		candDupBar.Show()
		ex := r.DuplicatePart
		if ex == nil {
			ex = &r.AllMatches[0]
		}
		if r.Resolved {
			candDupStatusLbl.SetText(fmt.Sprintf("Duplicate [%s] %s — Resolved: %s", ex.LibraryName, ex.Name, strings.ToUpper(r.Options.Strategy)))
		} else {
			candDupStatusLbl.SetText(fmt.Sprintf("⚠ Matches existing part [%s] %s (%d match(es))", ex.LibraryName, ex.Name, len(r.AllMatches)))
		}
	}

	updateBannerCounts := func() {
		dupCount := 0
		unresolvedCount := 0
		for _, r := range resolutions {
			if len(r.AllMatches) > 0 {
				dupCount++
				if !r.Resolved {
					unresolvedCount++
				}
			}
		}
		if dupCount == 0 {
			dupBanner.Hide()
		} else {
			if unresolvedCount > 0 {
				dupBannerLbl.SetText(fmt.Sprintf("%d candidate part(s) match existing parts in your database (%d unresolved). Click Compare & Merge or Import to choose how to handle them.", dupCount, unresolvedCount))
			} else {
				dupBannerLbl.SetText(fmt.Sprintf("%d duplicate candidate(s) found — all resolved and ready to import/merge.", dupCount))
			}
			dupBanner.Show()
			dupBanner.Refresh()
		}
		updateActiveCandidateDupBar()
		if candList != nil {
			candList.Refresh()
		}
	}

	refreshCandidateDup := func(idx int, libID int64) {
		if idx < 0 || idx >= len(res.Candidates) {
			return
		}
		c := res.Candidates[idx]
		matches, err := mgr.DB().FindDuplicateParts(c.PartName, c.MPN, c.LCSCPart, libID)
		if err != nil {
			matches = nil
		}
		prev := resolutions[idx]
		resolutions[idx].AllMatches = matches
		if len(matches) == 0 {
			resolutions[idx].DuplicatePart = nil
			resolutions[idx].Resolved = false
			resolutions[idx].Options = storage.MergeOptions{}
			return
		}
		if prev.DuplicatePart != nil {
			stillPresent := false
			for i := range matches {
				if matches[i].ID == prev.DuplicatePart.ID {
					mCopy := matches[i]
					resolutions[idx].DuplicatePart = &mCopy
					stillPresent = true
					break
				}
			}
			if !stillPresent {
				mCopy := matches[0]
				resolutions[idx].DuplicatePart = &mCopy
				resolutions[idx].Resolved = false
			}
		} else {
			mCopy := matches[0]
			resolutions[idx].DuplicatePart = &mCopy
		}
	}

	refreshAllDuplicates := func(libID int64) {
		for i := range res.Candidates {
			refreshCandidateDup(i, libID)
		}
		updateBannerCounts()
	}

	openCompareForIdx := func(idx int, onDone func()) {
		if idx < 0 || idx >= len(res.Candidates) || len(resolutions[idx].AllMatches) == 0 {
			if onDone != nil {
				onDone()
			}
			return
		}
		ShowDuplicateCompareDialog(
			stageWin, mgr, currentTargetLibID(),
			&res.Candidates[idx], resolutions[idx].AllMatches, resolutions[idx].Options,
			func(selectedMatch db.Part, opts storage.MergeOptions) {
				resolutions[idx].DuplicatePart = &selectedMatch
				resolutions[idx].Resolved = true
				resolutions[idx].Options = opts
				updateBannerCounts()
				if onDone != nil {
					onDone()
				}
			},
		)
	}

	candDupBtn.OnTapped = func() {
		openCompareForIdx(activeIdx, nil)
	}

	// Hook banner button cleanly so count label refreshes too
	dupBannerCompareBtn.OnTapped = func() {
		idx := activeIdx
		if idx < 0 || idx >= len(resolutions) || len(resolutions[idx].AllMatches) == 0 {
			idx = -1
			for i, r := range resolutions {
				if len(r.AllMatches) > 0 && !r.Resolved {
					idx = i
					break
				}
			}
			if idx < 0 {
				for i, r := range resolutions {
					if len(r.AllMatches) > 0 {
						idx = i
						break
					}
				}
			}
		}
		if idx >= 0 {
			candList.Select(idx)
			openCompareForIdx(idx, nil)
		}
	}

	applyTargetLibFilter := func(triggerDupRefresh bool) {
		filtered := filterLibraries(libs, libFilterEntry.Text, showHiddenCheck.Checked, false)
		opts := make([]string, 0, len(filtered))
		for _, l := range filtered {
			opts = append(opts, l.Name)
		}
		prev := existingSelect.Selected
		existingSelect.Options = opts
		switch {
		case prev != "" && containsString(opts, prev):
			existingSelect.Selected = prev
		case len(opts) > 0:
			existingSelect.Selected = opts[0]
		default:
			existingSelect.Selected = ""
		}
		existingSelect.Refresh()
		if triggerDupRefresh && existingSelect.Selected != prev {
			refreshAllDuplicates(currentTargetLibID())
		}
	}
	applyTargetLibFilter(false)

	libFilterEntry.OnChanged = func(_ string) {
		applyTargetLibFilter(true)
	}
	showHiddenCheck.OnChanged = func(_ bool) {
		applyTargetLibFilter(true)
	}

	targetModeRadio.OnChanged = func(sel string) {
		if sel == modeNew {
			existingLibBox.Hide()
			newLibBox.Show()
		} else {
			existingLibBox.Show()
			newLibBox.Hide()
		}
		refreshAllDuplicates(currentTargetLibID())
	}
	existingSelect.OnChanged = func(_ string) {
		refreshAllDuplicates(currentTargetLibID())
	}
	newLibNameEntry.OnChanged = func(_ string) {
		refreshAllDuplicates(currentTargetLibID())
	}

	loadCandidateIntoForm := func(idx int) {
		if idx < 0 || idx >= len(res.Candidates) {
			return
		}
		suppressSync = true
		defer func() { suppressSync = false }()

		activeIdx = idx
		c := &res.Candidates[idx]
		nameEntry.SetText(c.PartName)
		valEntry.SetText(c.Value)
		mpnEntry.SetText(c.MPN)
		mfgEntry.SetText(c.Manufacturer)
		lcscEntry.SetText(c.LCSCPart)
		dsEntry.SetText(c.Datasheet)
		descEntry.SetText(c.Description)

		if c.SymbolSExpr != "" {
			symSummaryLbl.SetText(fmt.Sprintf("%s (%d pins)", c.SymbolName, c.PinCount))
		} else {
			symSummaryLbl.SetText("(Footprint-only part)")
		}

		if c.FootprintName != "" {
			fpSelect.SetSelected(c.FootprintName)
		} else {
			fpSelect.SetSelected("(None)")
		}

		if len(c.Models3D) > 0 {
			var names []string
			for _, m := range c.Models3D {
				names = append(names, fmt.Sprintf("%s (%s)", m.FileName, strings.ToUpper(m.Format)))
			}
			modelsLbl.SetText(strings.Join(names, ", "))
		} else {
			modelsLbl.SetText("(none attached)")
		}
		refreshMetaPreview(c)
		updateActiveCandidateDupBar()
	}

	// Bind live edits back to res.Candidates[activeIdx]
	nameEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].PartName = s
			resolutions[activeIdx].Resolved = false
			refreshCandidateDup(activeIdx, currentTargetLibID())
			updateBannerCounts()
		}
	}
	valEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].Value = s
		}
	}
	mpnEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].MPN = s
			resolutions[activeIdx].Resolved = false
			refreshCandidateDup(activeIdx, currentTargetLibID())
			updateBannerCounts()
		}
	}
	mfgEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].Manufacturer = s
		}
	}
	lcscEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].LCSCPart = s
			resolutions[activeIdx].Resolved = false
			refreshCandidateDup(activeIdx, currentTargetLibID())
			updateBannerCounts()
		}
	}
	dsEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].Datasheet = s
		}
	}
	descEntry.OnChanged = func(s string) {
		if !suppressSync {
			res.Candidates[activeIdx].Description = s
		}
	}
	fpSelect.OnChanged = func(sel string) {
		if suppressSync {
			return
		}
		c := &res.Candidates[activeIdx]
		if sel == "(None)" || sel == "" {
			c.FootprintName = ""
			c.FootprintSourcePath = ""
			c.FootprintSExpr = ""
			c.PadCount = 0
			return
		}
		for _, fp := range res.AllFootprints {
			if fp.Name == sel {
				c.FootprintName = fp.Name
				c.FootprintSourcePath = fp.SourcePath
				c.FootprintSExpr = fp.RawSExpr
				c.PadCount = fp.PadCount
				break
			}
		}
	}

	addMetaBtn := widget.NewButtonWithIcon("Add Metadata", theme.ContentAddIcon(), func() {
		k := strings.TrimSpace(metaKeyEntry.Text)
		v := strings.TrimSpace(metaValEntry.Text)
		if k == "" {
			return
		}
		c := &res.Candidates[activeIdx]
		if c.Metadata == nil {
			c.Metadata = make(map[string]string)
		}
		c.Metadata[k] = v
		metaKeyEntry.SetText("")
		metaValEntry.SetText("")
		refreshMetaPreview(c)
	})

	lookupBtn := widget.NewButtonWithIcon("Lookup Metadata", theme.SearchIcon(), func() {
		c := &res.Candidates[activeIdx]
		q := strings.TrimSpace(c.LCSCPart)
		if q == "" {
			q = strings.TrimSpace(c.MPN)
		}
		if q == "" {
			q = c.PartName
		}
		go func(idx int, query string) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			found, err := importer.LookupMetadata(ctx, mgr.Config(), query)
			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(err, stageWin)
					return
				}
				target := &res.Candidates[idx]
				if target.Metadata == nil {
					target.Metadata = make(map[string]string)
				}
				for k, v := range found.Attributes {
					if target.Metadata[k] == "" {
						target.Metadata[k] = v
					}
				}
				if target.MPN == "" && found.MPN != "" {
					target.MPN = found.MPN
				}
				if target.Manufacturer == "" && found.Manufacturer != "" {
					target.Manufacturer = found.Manufacturer
				}
				if target.LCSCPart == "" && found.LCSCPart != "" {
					target.LCSCPart = found.LCSCPart
				}
				if target.Datasheet == "" && found.Datasheet != "" {
					target.Datasheet = found.Datasheet
				}
				if target.Description == "" && found.Description != "" {
					target.Description = found.Description
				}
				refreshCandidateDup(idx, currentTargetLibID())
				updateBannerCounts()
				if activeIdx == idx {
					loadCandidateIntoForm(idx)
				}
			})
		}(activeIdx, q)
	})

	candList = widget.NewList(
		func() int { return len(res.Candidates) },
		func() fyne.CanvasObject {
			chk := widget.NewCheck("", nil)
			lbl := widget.NewLabel("Part Name")
			lbl.Truncation = fyne.TextTruncateEllipsis
			return container.NewBorder(nil, nil, chk, nil, lbl)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			c := &res.Candidates[id]
			border := obj.(*fyne.Container)
			var chk *widget.Check
			var lbl *widget.Label
			for _, child := range border.Objects {
				if w, ok := child.(*widget.Check); ok {
					chk = w
				} else if w, ok := child.(*widget.Label); ok {
					lbl = w
				}
			}
			if chk != nil {
				chk.OnChanged = nil
				chk.SetChecked(c.Selected)
				chk.OnChanged = func(b bool) {
					res.Candidates[id].Selected = b
				}
			}
			if lbl != nil {
				fpTag := ""
				if c.FootprintName != "" {
					fpTag = " [" + c.FootprintName + "]"
				}
				m3Tag := ""
				if len(c.Models3D) > 0 {
					m3Tag = " +3D"
				}
				dupTag := ""
				if id < len(resolutions) && len(resolutions[id].AllMatches) > 0 {
					if resolutions[id].Resolved {
						dupTag = " [✓ " + strings.ToUpper(resolutions[id].Options.Strategy) + "]"
					} else {
						dupTag = " [⚠ Duplicate]"
					}
				}
				lbl.SetText(c.PartName + fpTag + m3Tag + dupTag)
			}
		},
	)
	candList.OnSelected = func(id widget.ListItemID) {
		loadCandidateIntoForm(id)
	}

	selectAllBtn := widget.NewButton("Select All", func() {
		for i := range res.Candidates {
			res.Candidates[i].Selected = true
		}
		candList.Refresh()
	})
	selectNoneBtn := widget.NewButton("Select None", func() {
		for i := range res.Candidates {
			res.Candidates[i].Selected = false
		}
		candList.Refresh()
	})

	refreshAllDuplicates(currentTargetLibID())
	loadCandidateIntoForm(0)
	candList.Select(0)

	leftPane := container.NewBorder(
		widget.NewLabelWithStyle(fmt.Sprintf("Detected Parts (%d) — %s", len(res.Candidates), res.VendorHint), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(selectAllBtn, selectNoneBtn),
		nil, nil,
		candList,
	)

	rightForm := container.NewVScroll(container.NewVBox(
		candDupBar,
		widget.NewForm(
			widget.NewFormItem("Part Name", nameEntry),
			widget.NewFormItem("Value", valEntry),
			widget.NewFormItem("MPN", mpnEntry),
			widget.NewFormItem("Manufacturer", mfgEntry),
			widget.NewFormItem("LCSC ID", lcscEntry),
			widget.NewFormItem("Datasheet", dsEntry),
			widget.NewFormItem("Description", descEntry),
			widget.NewFormItem("Symbol", symSummaryLbl),
			widget.NewFormItem("Footprint", fpSelect),
			widget.NewFormItem("3D Models", modelsLbl),
		),
		widget.NewSeparator(),
		container.NewHBox(widget.NewLabelWithStyle("Metadata", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), layout.NewSpacer(), lookupBtn),
		metaPreviewLbl,
		container.NewGridWithColumns(2, metaKeyEntry, metaValEntry),
		addMetaBtn,
	))

	split := container.NewHSplit(leftPane, rightForm)
	split.Offset = 0.35

	targetCard := widget.NewCard("Target KiCad Library", "", container.NewVBox(
		targetModeRadio,
		existingLibBox,
		newLibBox,
	))

	executeImport := func(targetLib *db.Library) {
		var errs []string
		importedCount := 0
		mergedCount := 0
		skippedCount := 0

		for i, c := range res.Candidates {
			if !c.Selected {
				continue
			}
			var models []storage.ImportModel3D
			for _, m := range c.Models3D {
				models = append(models, storage.ImportModel3D{
					SourcePath: m.SourcePath,
					FileName:   m.FileName,
					Format:     m.Format,
				})
			}
			req := storage.ImportPartRequest{
				LibraryID:           targetLib.ID,
				PartName:            c.PartName,
				Value:               c.Value,
				Description:         c.Description,
				Keywords:            c.Keywords,
				Datasheet:           c.Datasheet,
				MPN:                 c.MPN,
				Manufacturer:        c.Manufacturer,
				LCSCPart:            c.LCSCPart,
				SourceType:          c.SourceType,
				SourceRef:           c.SourceRef,
				SymbolName:          c.SymbolName,
				SymbolSourcePath:    c.SymbolSourcePath,
				SymbolSExpr:         c.SymbolSExpr,
				FootprintName:       c.FootprintName,
				FootprintSourcePath: c.FootprintSourcePath,
				FootprintSExpr:      c.FootprintSExpr,
				Models3D:            models,
				Metadata:            c.Metadata,
			}

			r := resolutions[i]
			if r.DuplicatePart != nil && r.Resolved {
				if strings.EqualFold(r.Options.Strategy, "skip") {
					skippedCount++
					continue
				}
				_, err := mgr.MergePart(r.DuplicatePart.ID, req, r.Options)
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", c.PartName, err))
				} else if strings.EqualFold(r.Options.Strategy, "rename") {
					importedCount++
				} else {
					mergedCount++
				}
			} else {
				_, err := mgr.ImportPart(req)
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", c.PartName, err))
				} else {
					importedCount++
				}
			}
		}

		stageWin.Close()
		summary := fmt.Sprintf("Library %q updated: %d imported, %d merged, %d skipped.", targetLib.Name, importedCount, mergedCount, skippedCount)
		if len(errs) > 0 {
			dialog.ShowInformation("Import Completed with Warnings",
				summary+"\n\nErrors:\n"+strings.Join(errs, "\n"),
				parent)
		} else {
			dialog.ShowInformation("Import Complete",
				summary+"\n\nKiCad symbol, footprint, and 3D libraries synced.",
				parent)
		}
		if onImported != nil {
			onImported()
		}
	}

	importBtn := widget.NewButtonWithIcon("Import & Sync to KiCad", theme.ConfirmIcon(), func() {
		selectedCount := 0
		for _, c := range res.Candidates {
			if c.Selected {
				selectedCount++
			}
		}
		if selectedCount == 0 {
			dialog.ShowInformation("No Parts Selected", "Check at least one part to import.", stageWin)
			return
		}

		var targetLib *db.Library
		var err error
		if targetModeRadio.Selected == modeNew {
			libName := strings.TrimSpace(newLibNameEntry.Text)
			if libName == "" {
				dialog.ShowInformation("Library Name Required", "Enter a name for the new KiCad library.", stageWin)
				return
			}
			targetLib, err = mgr.DB().GetLibraryByName(libName)
			if err != nil {
				targetLib, err = mgr.DB().CreateLibrary(libName, strings.TrimSpace(newLibDescEntry.Text))
				if err != nil {
					dialog.ShowError(err, stageWin)
					return
				}
			}
		} else {
			libName := existingSelect.Selected
			if libName == "" {
				dialog.ShowInformation("Library Required", "Select an existing library or choose Create New Library.", stageWin)
				return
			}
			targetLib, err = mgr.DB().GetLibraryByName(libName)
			if err != nil {
				dialog.ShowError(err, stageWin)
				return
			}
		}

		refreshAllDuplicates(targetLib.ID)

		// Collect unresolved duplicates among selected candidates
		var unresolvedQueue []int
		for i, c := range res.Candidates {
			if c.Selected && len(resolutions[i].AllMatches) > 0 && !resolutions[i].Resolved {
				unresolvedQueue = append(unresolvedQueue, i)
			}
		}
		if len(unresolvedQueue) == 0 {
			executeImport(targetLib)
			return
		}

		// Step sequentially through unresolved duplicates
		var stepNext func(qPos int)
		stepNext = func(qPos int) {
			if qPos >= len(unresolvedQueue) {
				executeImport(targetLib)
				return
			}
			idx := unresolvedQueue[qPos]
			candList.Select(idx)
			openCompareForIdx(idx, func() {
				stepNext(qPos + 1)
			})
		}
		stepNext(0)
	})
	importBtn.Importance = widget.HighImportance

	cancelBtn := widget.NewButton("Cancel", func() {
		stageWin.Close()
	})

	body := container.NewBorder(
		container.NewVBox(targetCard, dupBanner),
		container.NewHBox(layout.NewSpacer(), cancelBtn, importBtn),
		nil, nil,
		split,
	)

	stageWin.SetContent(body)
	stageWin.SetOnClosed(func() {
		res.Cleanup()
	})
	stageWin.CenterOnScreen()
	stageWin.Show()
}
