package main

import (
	"fmt"
	"os"

	"github.com/jeffk/kicad-lib/internal/config"
	"github.com/jeffk/kicad-lib/internal/db"
	"github.com/jeffk/kicad-lib/internal/kicad"
	"github.com/jeffk/kicad-lib/internal/storage"
	"github.com/jeffk/kicad-lib/internal/ui"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.EnsureDirs(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize storage directories: %v\n", err)
		os.Exit(1)
	}

	database, err := db.Open(cfg.DBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open sqlite database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	ipcClient := kicad.NewIPCClient(cfg.KiCadIPCSocket)
	mgr, err := storage.NewManager(cfg, database, ipcClient)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize storage manager: %v\n", err)
		os.Exit(1)
	}

	desktopApp := ui.NewApp(mgr)
	desktopApp.Run()
}
