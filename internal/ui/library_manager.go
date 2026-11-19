package ui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jeffk/kicad-lib/internal/db"
	"github.com/jeffk/kicad-lib/internal/storage"
)

const (
	libFilterAll           = "All Libraries"
	libFilterShown         = "Shown (Un-hidden)"
	libFilterHidden        = "Hidden"
	libFilterKiCadEnabled  = "Enabled in KiCad"
	libFilterKiCadDisabled = "Disabled in KiCad"
)

// filterLibraries filters libs by case-insensitive substring query (Name, Description, and optionally URIs),
// and optionally excludes hidden libraries.
func filterLibraries(libs []db.Library, query string, includeHidden bool, matchURI bool) []db.Library {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]db.Library, 0, len(libs))
	for _, l := range libs {
		if !includeHidden && l.Hidden {
			continue
		}
		if q != "" && !libraryMatchesQuery(l, q, matchURI) {
			continue
		}
		out = append(out, l)
	}
	return out
}

func libraryMatchesQuery(l db.Library, qLower string, matchURI bool) bool {
	if strings.Contains(strings.ToLower(l.Name), qLower) ||
		strings.Contains(strings.ToLower(l.Description), qLower) {
		return true
	}
	if matchURI {
		if strings.Contains(strings.ToLower(l.SourceURI), qLower) ||
			strings.Contains(strings.ToLower(l.FPSourceURI), qLower) {
			return true
		}
	}
	return false
}

type libraryManagerView struct {
	win       fyne.Window
	mgr       *storage.Manager
	onChanged func()

	allLibs      []db.Library
	filteredLibs []db.Library

	searchEntry  *widget.Entry
	statusSelect *widget.Select
	summaryLbl   *widget.Label
	list         *widget.List
	root         fyne.CanvasObject
}

func newLibraryManagerView(win fyne.Window, mgr *storage.Manager, onChanged func()) *libraryManagerView {
	v := &libraryManagerView{
		win:       win,
		mgr:       mgr,
		onChanged: onChanged,
	}

	v.searchEntry = widget.NewEntry()
	v.searchEntry.SetPlaceHolder("Search libraries by name, description, or URI...")
	v.searchEntry.OnChanged = func(_ string) {
		v.applyFilter()
	}

	v.statusSelect = widget.NewSelect([]string{
		libFilterAll,
		libFilterShown,
		libFilterHidden,
		libFilterKiCadEnabled,
		libFilterKiCadDisabled,
	}, nil)
	v.statusSelect.SetSelected(libFilterAll)
	v.statusSelect.OnChanged = func(_ string) {
		v.applyFilter()
	}

	v.summaryLbl = widget.NewLabel("")
	v.summaryLbl.Wrapping = fyne.TextWrapWord

	unhideFilteredBtn := widget.NewButtonWithIcon("Un-hide Filtered", theme.VisibilityIcon(), func() {
		v.setFilteredHidden(false)
	})
	hideFilteredBtn := widget.NewButtonWithIcon("Hide Filtered", theme.VisibilityOffIcon(), func() {
		v.setFilteredHidden(true)
	})
	enableFilteredBtn := widget.NewButtonWithIcon("Enable Filtered in KiCad", theme.ConfirmIcon(), func() {
		v.setFilteredKiCadEnabled(true)
	})
	disableFilteredBtn := widget.NewButtonWithIcon("Disable Filtered in KiCad", theme.CancelIcon(), func() {
		v.setFilteredKiCadEnabled(false)
	})

	v.list = widget.NewList(
		func() int { return len(v.filteredLibs) },
		func() fyne.CanvasObject {
			shownChk := widget.NewCheck("Shown", nil)
			kicadChk := widget.NewCheck("In KiCad", nil)
			nameLbl := widget.NewLabelWithStyle("Library Name", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			nameLbl.Truncation = fyne.TextTruncateEllipsis
			infoLbl := widget.NewLabel("Source & description")
			infoLbl.Truncation = fyne.TextTruncateEllipsis
			countLbl := widget.NewLabel("0 parts")
			return container.NewBorder(
				nil, nil,
				container.NewHBox(shownChk, kicadChk),
				countLbl,
				container.NewVBox(nameLbl, infoLbl),
			)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < 0 || id >= len(v.filteredLibs) {
				return
			}
			lib := v.filteredLibs[id]
			border := obj.(*fyne.Container)

			var leftBox *fyne.Container
			var centerBox *fyne.Container
			var countLbl *widget.Label
			for _, child := range border.Objects {
				switch c := child.(type) {
				case *fyne.Container:
					if len(c.Objects) == 2 {
						if _, ok := c.Objects[0].(*widget.Check); ok {
							leftBox = c
						} else {
							centerBox = c
						}
					}
				case *widget.Label:
					countLbl = c
				}
			}

			if leftBox != nil && len(leftBox.Objects) == 2 {
				shownChk := leftBox.Objects[0].(*widget.Check)
				kicadChk := leftBox.Objects[1].(*widget.Check)

				shownChk.OnChanged = nil
				shownChk.SetChecked(!lib.Hidden)
				libID := lib.ID
				shownChk.OnChanged = func(checked bool) {
					if err := v.mgr.DB().SetLibraryHidden(libID, !checked); err != nil {
						dialog.ShowError(err, v.win)
						v.reload()
						return
					}
					v.reload()
					if v.onChanged != nil {
						v.onChanged()
					}
				}

				kicadChk.OnChanged = nil
				kicadChk.SetChecked(lib.KiCadEnabled)
				kicadChk.OnChanged = func(checked bool) {
					if err := v.mgr.SetLibraryKiCadEnabled(libID, checked); err != nil {
						dialog.ShowError(err, v.win)
						v.reload()
						return
					}
					v.reload()
					if v.onChanged != nil {
						v.onChanged()
					}
				}
			}

			if centerBox != nil && len(centerBox.Objects) == 2 {
				nameLbl := centerBox.Objects[0].(*widget.Label)
				infoLbl := centerBox.Objects[1].(*widget.Label)

				nameLbl.SetText(lib.Name)

				origin := "[Managed]"
				if lib.SourceURI != "" || lib.FPSourceURI != "" {
					origin = "[KiCad Sync]"
				}
				detail := strings.TrimSpace(lib.Description)
				if detail == "" {
					if lib.SourceURI != "" {
						detail = lib.SourceURI
					} else if lib.FPSourceURI != "" {
						detail = lib.FPSourceURI
					} else {
						detail = "Local managed library"
					}
				}
				infoLbl.SetText(origin + " " + detail)
			}

			if countLbl != nil {
				unit := "parts"
				if lib.PartCount == 1 {
					unit = "part"
				}
				countLbl.SetText(fmt.Sprintf("%d %s", lib.PartCount, unit))
			}
		},
	)

	closeBtn := widget.NewButton("Close", func() {
		if v.win != nil {
			v.win.Close()
		}
	})

	filterBar := container.NewBorder(nil, nil, nil, v.statusSelect, v.searchEntry)
	bulkBar := container.NewGridWithColumns(4, unhideFilteredBtn, hideFilteredBtn, enableFilteredBtn, disableFilteredBtn)
	topControls := container.NewVBox(
		filterBar,
		bulkBar,
		v.summaryLbl,
		widget.NewSeparator(),
	)

	bottomBar := container.NewVBox(
		widget.NewSeparator(),
		container.NewHBox(
			widget.NewLabel("Tip: New libraries are hidden by default. Toggle 'Shown' to show in main browser, or 'In KiCad' to enable/disable in KiCad tables."),
			layout.NewSpacer(),
			closeBtn,
		),
	)

	v.root = container.NewBorder(topControls, bottomBar, nil, nil, v.list)
	v.reload()
	return v
}

func (v *libraryManagerView) reload() {
	libs, err := v.mgr.DB().ListLibraries()
	if err != nil {
		if v.win != nil {
			dialog.ShowError(err, v.win)
		}
		return
	}
	v.allLibs = libs
	v.applyFilter()
}

func (v *libraryManagerView) applyFilter() {
	if v.list == nil || v.summaryLbl == nil {
		return
	}
	base := filterLibraries(v.allLibs, v.searchEntry.Text, true, true)
	status := v.statusSelect.Selected
	filtered := make([]db.Library, 0, len(base))
	for _, l := range base {
		switch status {
		case libFilterShown:
			if l.Hidden {
				continue
			}
		case libFilterHidden:
			if !l.Hidden {
				continue
			}
		case libFilterKiCadEnabled:
			if !l.KiCadEnabled {
				continue
			}
		case libFilterKiCadDisabled:
			if l.KiCadEnabled {
				continue
			}
		}
		filtered = append(filtered, l)
	}
	v.filteredLibs = filtered
	v.list.Refresh()

	shownCount := 0
	kicadEnabledCount := 0
	for _, l := range v.allLibs {
		if !l.Hidden {
			shownCount++
		}
		if l.KiCadEnabled {
			kicadEnabledCount++
		}
	}
	v.summaryLbl.SetText(fmt.Sprintf(
		"Showing %d of %d libraries | %d Shown | %d Enabled in KiCad",
		len(v.filteredLibs), len(v.allLibs), shownCount, kicadEnabledCount,
	))
}

func (v *libraryManagerView) filteredIDs() []int64 {
	ids := make([]int64, 0, len(v.filteredLibs))
	for _, l := range v.filteredLibs {
		ids = append(ids, l.ID)
	}
	return ids
}

func (v *libraryManagerView) setFilteredHidden(hidden bool) {
	ids := v.filteredIDs()
	if len(ids) == 0 {
		return
	}
	if err := v.mgr.DB().SetLibrariesHidden(ids, hidden); err != nil {
		dialog.ShowError(err, v.win)
		return
	}
	v.reload()
	if v.onChanged != nil {
		v.onChanged()
	}
}

func (v *libraryManagerView) setFilteredKiCadEnabled(enabled bool) {
	ids := v.filteredIDs()
	if len(ids) == 0 {
		return
	}
	if err := v.mgr.SetLibrariesKiCadEnabled(ids, enabled); err != nil {
		dialog.ShowError(err, v.win)
		return
	}
	v.reload()
	if v.onChanged != nil {
		v.onChanged()
	}
}

// ShowManageLibrariesWindow opens a resizable window to search, un-hide/hide,
// and enable/disable libraries in KiCad.
func ShowManageLibrariesWindow(parent fyne.Window, mgr *storage.Manager, onChanged func()) {
	win := fyne.CurrentApp().NewWindow("Manage Libraries")
	defaultWidth := float32(960)
	defaultHeight := float32(680)
	if parent != nil {
		pSize := parent.Canvas().Size()
		if pSize.Width > 0 && pSize.Height > 0 {
			if tw := pSize.Width * 0.80; tw > defaultWidth {
				defaultWidth = tw
			}
			if th := pSize.Height * 0.80; th > defaultHeight {
				defaultHeight = th
			}
		}
	}
	if defaultWidth > 1400 {
		defaultWidth = 1400
	}
	if defaultHeight > 950 {
		defaultHeight = 950
	}
	win.Resize(fyne.NewSize(defaultWidth, defaultHeight))
	win.SetFixedSize(false)
	win.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) {
		if ev.Name == fyne.KeyEscape {
			win.Close()
		}
	})

	view := newLibraryManagerView(win, mgr, onChanged)
	win.SetContent(view.root)
	win.CenterOnScreen()
	win.Show()
}
