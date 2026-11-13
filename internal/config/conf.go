package config

// confFile mirrors conf/loglens.conf (see conf/loglens.conf.example in the
// repository root for the documented shape). Every field is a pointer: nil
// means the key is absent from the file, which is exactly the information the
// merge needs so that a file that sets only some keys does not clobber the
// environment-provided values for the rest.
type confFile struct {
	Dirs   *confDirs   `toml:"dirs"`
	Limits *confLimits `toml:"limits"`
	Serve  *confServe  `toml:"serve"`
	Log    *confLog    `toml:"log"`
}

type confDirs struct {
	// Allow lists the allowed log directories. Relative entries resolve
	// against the workspace root when one is in use.
	Allow *[]string `toml:"allow"`
}

type confLimits struct {
	MaxLines  *int   `toml:"max_lines"`
	MaxBytes  *int64 `toml:"max_bytes"`
	MaxFiles  *int   `toml:"max_files"`
	TimeoutMs *int   `toml:"timeout_ms"`
}

type confServe struct {
	// Addr and Token set the defaults for `loglens serve`; explicit CLI flags
	// override them.
	Addr  *string `toml:"addr"`
	Token *string `toml:"token"`
}

type confLog struct {
	// File is the LogLens runtime log path (relative entries resolve against
	// the workspace root). Empty means stderr, which is the default.
	File *string `toml:"file"`
	// MaxSizeMB is the rotation threshold; a new file replaces the old one
	// beyond this size (3 rotated copies are kept).
	MaxSizeMB *int `toml:"max_size_mb"`
}