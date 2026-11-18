package ui

import (
	"context"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jeffk/kicad-lib/internal/importer"
	"github.com/jeffk/kicad-lib/internal/storage"
)

// ShowGitImportDialog prompts for a Git repository URL, clones/pulls and scans it,
// and hands discovered parts to the Import Staging Dialog.
func ShowGitImportDialog(parent fyne.Window, mgr *storage.Manager, onImported func()) {
	urlEntry := widget.NewEntry()
	urlEntry.SetPlaceHolder("https://github.com/user/kicad-parts-library.git")

	statusLbl := widget.NewLabel("Enter a Git repository URL containing KiCad symbols, footprints, or 3D models.")
	statusLbl.Wrapping = fyne.TextWrapWord
	progress := widget.NewProgressBarInfinite()
	progress.Hide()

	var dlg dialog.Dialog
	scanBtn := widget.NewButtonWithIcon("Clone & Scan", theme.DownloadIcon(), nil)
	scanBtn.Importance = widget.HighImportance
	cancelBtn := widget.NewButton("Cancel", func() { dlg.Hide() })

	scanBtn.OnTapped = func() {
		repoURL := strings.TrimSpace(urlEntry.Text)
		if repoURL == "" {
			dialog.ShowInformation("Git Import", "Please enter a Git repository URL.", parent)
			return
		}
		scanBtn.Disable()
		progress.Show()
		statusLbl.SetText("Cloning/pulling repository and scanning KiCad files...")

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			res, _, err := importer.ScanGitRepo(ctx, repoURL, mgr.Config().GitCacheDir(), mgr.Config().KiCadCLIPath)
			fyne.Do(func() {
				scanBtn.Enable()
				progress.Hide()
				if err != nil {
					statusLbl.SetText("Error: " + err.Error())
					return
				}
				res.VendorHint = "Git (" + repoURL + ")"
				dlg.Hide()
				ShowDropStagingDialog(parent, mgr, res, onImported)
			})
		}()
	}

	content := container.NewVBox(
		widget.NewForm(widget.NewFormItem("Git Repo URL", urlEntry)),
		statusLbl,
		progress,
		container.NewHBox(layout.NewSpacer(), cancelBtn, scanBtn),
	)
	dlg = dialog.NewCustomWithoutButtons("Import Parts from Git Repository", content, parent)
	dlg.Resize(fyne.NewSize(560, 220))
	dlg.Show()
}

// ShowLCSCImportDialog fetches a component (symbol, footprint, 3D STEP model, metadata) from LCSC / EasyEDA by C-number.
func ShowLCSCImportDialog(parent fyne.Window, mgr *storage.Manager, onImported func()) {
	lcscEntry := widget.NewEntry()
	lcscEntry.SetPlaceHolder("e.g. C2040, C14663, C8734")

	statusLbl := widget.NewLabel("Enter an LCSC part number (C-number) to fetch symbol, footprint, 3D STEP model, and parameters.")
	statusLbl.Wrapping = fyne.TextWrapWord
	progress := widget.NewProgressBarInfinite()
	progress.Hide()

	var dlg dialog.Dialog
	fetchBtn := widget.NewButtonWithIcon("Fetch from LCSC / EasyEDA", theme.DownloadIcon(), nil)
	fetchBtn.Importance = widget.HighImportance
	cancelBtn := widget.NewButton("Cancel", func() { dlg.Hide() })

	fetchBtn.OnTapped = func() {
		id := strings.TrimSpace(lcscEntry.Text)
		if id == "" {
			dialog.ShowInformation("LCSC Import", "Please enter an LCSC part number (e.g. C2040).", parent)
			return
		}
		fetchBtn.Disable()
		progress.Show()
		statusLbl.SetText("Fetching symbol, footprint, and 3D model from EasyEDA/LCSC...")

		go func() {
			tmpDir, err := os.MkdirTemp("", "kicad-lib-lcsc-*")
			if err != nil {
				fyne.Do(func() {
					fetchBtn.Enable()
					progress.Hide()
					statusLbl.SetText("Error: " + err.Error())
				})
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cand, err := importer.FetchLCSCPart(ctx, id, tmpDir)
			fyne.Do(func() {
				fetchBtn.Enable()
				progress.Hide()
				if err != nil {
					_ = os.RemoveAll(tmpDir)
					statusLbl.SetText("Error: " + err.Error())
					return
				}

				res := &importer.ScanResult{
					VendorHint:  "EasyEDA / LCSC",
					TempWorkDir: tmpDir,
					Candidates:  []importer.CandidatePart{*cand},
				}
				if cand.FootprintName != "" {
					res.AllFootprints = []importer.CandidateFootprint{{
						Name:     cand.FootprintName,
						RawSExpr: cand.FootprintSExpr,
						PadCount: cand.PadCount,
					}}
				}
				res.AllModels3D = cand.Models3D
				dlg.Hide()
				ShowDropStagingDialog(parent, mgr, res, onImported)
			})
		}()
	}

	content := container.NewVBox(
		widget.NewForm(widget.NewFormItem("LCSC Part #", lcscEntry)),
		statusLbl,
		progress,
		container.NewHBox(layout.NewSpacer(), cancelBtn, fetchBtn),
	)
	dlg = dialog.NewCustomWithoutButtons("Import Part from LCSC / EasyEDA", content, parent)
	dlg.Resize(fyne.NewSize(520, 220))
	dlg.Show()
}
