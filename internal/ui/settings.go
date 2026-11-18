package ui

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jeffk/kicad-lib/internal/storage"
)

// ShowSettingsDialog lets the user configure local storage path, KiCad config/CLI/IPC paths, and distributor API credentials.
func ShowSettingsDialog(parent fyne.Window, mgr *storage.Manager, onSaved func()) {
	cfgCopy := *mgr.Config()

	storageEntry := widget.NewEntry()
	storageEntry.SetText(cfgCopy.StorageRoot)
	browseStorage := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.ShowFolderOpen(func(lu fyne.ListableURI, err error) {
			if err == nil && lu != nil {
				storageEntry.SetText(lu.Path())
			}
		}, parent)
	})

	kicadCfgEntry := widget.NewEntry()
	kicadCfgEntry.SetText(cfgCopy.KiCadConfigDir)
	browseKiCadCfg := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.ShowFolderOpen(func(lu fyne.ListableURI, err error) {
			if err == nil && lu != nil {
				kicadCfgEntry.SetText(lu.Path())
			}
		}, parent)
	})

	cliEntry := widget.NewEntry()
	cliEntry.SetText(cfgCopy.KiCadCLIPath)
	ipcEntry := widget.NewEntry()
	ipcEntry.SetText(cfgCopy.KiCadIPCSocket)

	autoRegCheck := widget.NewCheck("Automatically register generated libraries in KiCad sym-lib-table / fp-lib-table", nil)
	autoRegCheck.SetChecked(cfgCopy.AutoRegisterLibTables)

	mouserEntry := widget.NewPasswordEntry()
	mouserEntry.SetText(cfgCopy.MouserAPIKey)
	dkIDEntry := widget.NewEntry()
	dkIDEntry.SetText(cfgCopy.DigiKeyClientID)
	dkSecretEntry := widget.NewPasswordEntry()
	dkSecretEntry.SetText(cfgCopy.DigiKeyClientSecret)
	nexarIDEntry := widget.NewEntry()
	nexarIDEntry.SetText(cfgCopy.NexarClientID)
	nexarSecretEntry := widget.NewPasswordEntry()
	nexarSecretEntry.SetText(cfgCopy.NexarClientSecret)

	form := widget.NewForm(
		widget.NewFormItem("Local Storage Root", container.NewBorder(nil, nil, nil, browseStorage, storageEntry)),
		widget.NewFormItem("KiCad Config Dir", container.NewBorder(nil, nil, nil, browseKiCadCfg, kicadCfgEntry)),
		widget.NewFormItem("kicad-cli Path", cliEntry),
		widget.NewFormItem("KiCad IPC Socket", ipcEntry),
		widget.NewFormItem("Library Tables", autoRegCheck),
		widget.NewFormItem("Mouser API Key", mouserEntry),
		widget.NewFormItem("DigiKey Client ID", dkIDEntry),
		widget.NewFormItem("DigiKey Secret", dkSecretEntry),
		widget.NewFormItem("Nexar Client ID", nexarIDEntry),
		widget.NewFormItem("Nexar Secret", nexarSecretEntry),
	)

	var dlg dialog.Dialog
	saveBtn := widget.NewButtonWithIcon("Save Settings", theme.DocumentSaveIcon(), func() {
		cfgCopy.StorageRoot = strings.TrimSpace(storageEntry.Text)
		cfgCopy.KiCadConfigDir = strings.TrimSpace(kicadCfgEntry.Text)
		cfgCopy.KiCadCLIPath = strings.TrimSpace(cliEntry.Text)
		cfgCopy.KiCadIPCSocket = strings.TrimSpace(ipcEntry.Text)
		cfgCopy.AutoRegisterLibTables = autoRegCheck.Checked
		cfgCopy.MouserAPIKey = strings.TrimSpace(mouserEntry.Text)
		cfgCopy.DigiKeyClientID = strings.TrimSpace(dkIDEntry.Text)
		cfgCopy.DigiKeyClientSecret = strings.TrimSpace(dkSecretEntry.Text)
		cfgCopy.NexarClientID = strings.TrimSpace(nexarIDEntry.Text)
		cfgCopy.NexarClientSecret = strings.TrimSpace(nexarSecretEntry.Text)

		if err := mgr.UpdateConfig(&cfgCopy); err != nil {
			dialog.ShowError(err, parent)
			return
		}
		if err := cfgCopy.Save(); err != nil {
			dialog.ShowError(err, parent)
			return
		}
		dlg.Hide()
		if onSaved != nil {
			onSaved()
		}
	})
	saveBtn.Importance = widget.HighImportance
	cancelBtn := widget.NewButton("Cancel", func() { dlg.Hide() })

	body := container.NewBorder(
		nil,
		container.NewHBox(layout.NewSpacer(), cancelBtn, saveBtn),
		nil, nil,
		container.NewVScroll(form),
	)
	dlg = dialog.NewCustomWithoutButtons("Settings", body, parent)
	dlg.Resize(fyne.NewSize(640, 520))
	dlg.Show()
}
