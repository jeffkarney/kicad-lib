package config

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// Config holds user preferences, storage locations, KiCad integration paths, and API credentials.
type Config struct {
	StorageRoot           string `json:"storage_root"`
	KiCadConfigDir        string `json:"kicad_config_dir"`
	KiCadCLIPath          string `json:"kicad_cli_path"`
	KiCadIPCSocket        string `json:"kicad_ipc_socket"`
	AutoRegisterLibTables bool   `json:"auto_register_lib_tables"`

	DigiKeyClientID     string `json:"digikey_client_id,omitempty"`
	DigiKeyClientSecret string `json:"digikey_client_secret,omitempty"`
	MouserAPIKey        string `json:"mouser_api_key,omitempty"`
	NexarClientID       string `json:"nexar_client_id,omitempty"`
	NexarClientSecret   string `json:"nexar_client_secret,omitempty"`

	configPath string
}

// DefaultConfigPath returns ~/.config/kicad-lib/config.json.
func DefaultConfigPath() (string, error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", err
		}
		cfgDir = filepath.Join(home, ".config")
	}
	return filepath.Join(cfgDir, "kicad-lib", "config.json"), nil
}

// Default returns a Config initialized with sensible OS-specific defaults.
func Default() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}

	storageRoot := filepath.Join(home, ".local", "share", "kicad-lib")
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		storageRoot = filepath.Join(xdgData, "kicad-lib")
	}

	ipcSocket := os.Getenv("KICAD_API_SOCKET")
	if ipcSocket == "" {
		ipcSocket = filepath.Join(os.TempDir(), "kicad", "api.sock")
	}

	cliPath, _ := exec.LookPath("kicad-cli")
	cfgPath, _ := DefaultConfigPath()

	return &Config{
		StorageRoot:           storageRoot,
		KiCadConfigDir:        detectKiCadConfigDir(home),
		KiCadCLIPath:          cliPath,
		KiCadIPCSocket:        ipcSocket,
		AutoRegisterLibTables: true,
		configPath:            cfgPath,
	}, nil
}

func detectKiCadConfigDir(home string) string {
	baseCfg := filepath.Join(home, ".config", "kicad")
	if xdgCfg := os.Getenv("XDG_CONFIG_HOME"); xdgCfg != "" {
		baseCfg = filepath.Join(xdgCfg, "kicad")
	}

	for _, ver := range []string{"10.0", "9.0", "8.0", "7.0"} {
		candidate := filepath.Join(baseCfg, ver)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return filepath.Join(baseCfg, "10.0")
}

// Load reads configuration from the default config path or creates defaults if missing.
func Load() (*Config, error) {
	p, err := DefaultConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadFrom(p)
}

// LoadFrom reads configuration from a specific path, returning defaults if the file does not exist.
func LoadFrom(path string) (*Config, error) {
	cfg, err := Default()
	if err != nil {
		return nil, err
	}
	cfg.configPath = path

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	// Fill any empty runtime defaults
	def, _ := Default()
	if cfg.StorageRoot == "" && def != nil {
		cfg.StorageRoot = def.StorageRoot
	}
	if cfg.KiCadConfigDir == "" && def != nil {
		cfg.KiCadConfigDir = def.KiCadConfigDir
	}
	if cfg.KiCadCLIPath == "" && def != nil {
		cfg.KiCadCLIPath = def.KiCadCLIPath
	}
	if cfg.KiCadIPCSocket == "" && def != nil {
		cfg.KiCadIPCSocket = def.KiCadIPCSocket
	}
	cfg.configPath = path
	return cfg, nil
}

// Save writes configuration to its loaded path (or DefaultConfigPath).
func (c *Config) Save() error {
	target := c.configPath
	if target == "" {
		var err error
		target, err = DefaultConfigPath()
		if err != nil {
			return err
		}
		c.configPath = target
	}
	return c.SaveTo(target)
}

// SaveTo persists configuration as formatted JSON to the specified path.
func (c *Config) SaveTo(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// DBPath returns the SQLite database path inside StorageRoot.
func (c *Config) DBPath() string {
	return filepath.Join(c.StorageRoot, "kicad-lib.db")
}

// VaultDir returns the canonical immutable asset storage root.
func (c *Config) VaultDir() string {
	return filepath.Join(c.StorageRoot, "vault")
}

// LibrariesDir returns the synced KiCad libraries directory root.
func (c *Config) LibrariesDir() string {
	return filepath.Join(c.StorageRoot, "libraries")
}

// SymbolsDir returns the generated .kicad_sym libraries directory.
func (c *Config) SymbolsDir() string {
	return filepath.Join(c.LibrariesDir(), "symbols")
}

// FootprintsDir returns the generated .pretty directories root.
func (c *Config) FootprintsDir() string {
	return filepath.Join(c.LibrariesDir(), "footprints")
}

// Models3DDir returns the generated .3dshapes directories root.
func (c *Config) Models3DDir() string {
	return filepath.Join(c.LibrariesDir(), "3dmodels")
}

// GitCacheDir returns the directory where cloned Git repositories are cached.
func (c *Config) GitCacheDir() string {
	return filepath.Join(c.StorageRoot, "cache", "git")
}

// PreviewCacheDir returns the directory for rendered SVG preview caches.
func (c *Config) PreviewCacheDir() string {
	return filepath.Join(c.StorageRoot, "cache", "previews")
}

// EnsureDirs creates all required local storage subdirectories.
func (c *Config) EnsureDirs() error {
	dirs := []string{
		c.StorageRoot,
		filepath.Join(c.VaultDir(), "parts"),
		c.SymbolsDir(),
		c.FootprintsDir(),
		c.Models3DDir(),
		c.GitCacheDir(),
		c.PreviewCacheDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
