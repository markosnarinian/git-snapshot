package app

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultRef              = "refs/snapshots/default"
	DefaultNamespace        = "refs/snapshots/"
	DefaultBase             = "HEAD"
	DefaultMessageTemplate  = "git-snapshot: {createdAt}"
	DefaultOutputFormat     = "human"
	DefaultColor            = "auto"
	DefaultLockTimeout      = 5 * time.Second
	DefaultCommand          = "create"
	DefaultIncludeUntracked = true
	DefaultCreateReflog     = true
)

type ConfigValue struct {
	Value  string `json:"value"`
	Origin string `json:"origin"`
}

type Config struct {
	RepoPath           string
	Ref                string
	Namespace          string
	Base               string
	IncludeUntracked   bool
	IncludeIgnored     bool
	CreateReflog       bool
	MessageTemplate    string
	Sign               bool
	SigningKey         string
	OutputFormat       string
	Color              string
	Yes                bool
	RestoreDestination string
	Retention          int
	LockTimeout        time.Duration
	DefaultCommand     string
	ConfigFile         string
	Values             map[string]ConfigValue
}

var configKeys = []string{
	"snapshot.repo",
	"snapshot.ref",
	"snapshot.namespace",
	"snapshot.base",
	"snapshot.includeUntracked",
	"snapshot.includeIgnored",
	"snapshot.createReflog",
	"snapshot.messageTemplate",
	"snapshot.sign",
	"snapshot.signingKey",
	"snapshot.outputFormat",
	"snapshot.color",
	"snapshot.yes",
	"snapshot.restoreDestination",
	"snapshot.retention",
	"snapshot.lockTimeout",
	"snapshot.defaultCommand",
}

func Defaults() Config {
	cfg := Config{
		RepoPath:         ".",
		Ref:              DefaultRef,
		Namespace:        DefaultNamespace,
		Base:             DefaultBase,
		IncludeUntracked: DefaultIncludeUntracked,
		CreateReflog:     DefaultCreateReflog,
		MessageTemplate:  DefaultMessageTemplate,
		OutputFormat:     DefaultOutputFormat,
		Color:            DefaultColor,
		LockTimeout:      DefaultLockTimeout,
		DefaultCommand:   DefaultCommand,
		Values:           make(map[string]ConfigValue),
	}
	cfg.recordDefaults()
	return cfg
}

func (c *Config) recordDefaults() {
	defaults := map[string]string{
		"snapshot.repo":               c.RepoPath,
		"snapshot.ref":                c.Ref,
		"snapshot.namespace":          c.Namespace,
		"snapshot.base":               c.Base,
		"snapshot.includeUntracked":   strconv.FormatBool(c.IncludeUntracked),
		"snapshot.includeIgnored":     strconv.FormatBool(c.IncludeIgnored),
		"snapshot.createReflog":       strconv.FormatBool(c.CreateReflog),
		"snapshot.messageTemplate":    c.MessageTemplate,
		"snapshot.sign":               strconv.FormatBool(c.Sign),
		"snapshot.signingKey":         c.SigningKey,
		"snapshot.outputFormat":       c.OutputFormat,
		"snapshot.color":              c.Color,
		"snapshot.yes":                strconv.FormatBool(c.Yes),
		"snapshot.restoreDestination": c.RestoreDestination,
		"snapshot.retention":          strconv.Itoa(c.Retention),
		"snapshot.lockTimeout":        c.LockTimeout.String(),
		"snapshot.defaultCommand":     c.DefaultCommand,
	}
	for key, value := range defaults {
		c.Values[key] = ConfigValue{Value: value, Origin: "default"}
	}
}

// LoadConfig applies explicit-file values as a defaults layer, followed by
// global config, repository-local config, and environment variables.
func LoadConfig(ctx context.Context, repoPath, explicitFile string) (Config, error) {
	cfg := Defaults()
	if explicitFile != "" {
		values, err := readGitConfig(ctx, Git{}, []string{"config", "--file", explicitFile, "--null", "--list"})
		if err != nil {
			return cfg, fail(ExitUsage, "could not read explicit configuration file", "Check --config-file and its permissions.", err)
		}
		if err := cfg.apply(values, "file:"+explicitFile); err != nil {
			return cfg, err
		}
		cfg.ConfigFile = explicitFile
	}
	global, err := readGitConfig(ctx, Git{}, []string{"config", "--global", "--null", "--list"})
	if err != nil && !isGitExit(err, 1) {
		return cfg, fail(ExitUsage, "could not read global Git configuration", "Repair the global Git configuration and retry.", err)
	}
	if err := cfg.apply(global, "global"); err != nil {
		return cfg, err
	}
	if repoPath == "" {
		repoPath = cfg.RepoPath
	}
	if repoPath != "" {
		local, localErr := readGitConfig(ctx, Git{Repo: repoPath}, []string{"config", "--local", "--null", "--list"})
		if localErr == nil {
			if err := cfg.apply(local, "local"); err != nil {
				return cfg, err
			}
		}
	}
	if err := cfg.applyEnvironment(); err != nil {
		return cfg, err
	}
	if repoPath != "" && os.Getenv("GIT_SNAPSHOT_REPO") == "" {
		cfg.RepoPath = repoPath
		cfg.Values["snapshot.repo"] = ConfigValue{Value: repoPath, Origin: "cli-bootstrap"}
	}
	return cfg, nil
}

func readGitConfig(ctx context.Context, git Git, args []string) (map[string]string, error) {
	data, err := git.RunBytes(ctx, args...)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for _, record := range strings.Split(string(data), "\x00") {
		if record == "" {
			continue
		}
		key, value, found := strings.Cut(record, "\n")
		if found && strings.HasPrefix(strings.ToLower(key), "snapshot.") {
			values[strings.ToLower(key)] = value
		}
	}
	return values, nil
}

func (c *Config) apply(values map[string]string, origin string) error {
	for key, value := range values {
		if err := c.set(strings.ToLower(key), value, origin); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) applyEnvironment() error {
	env := map[string]string{
		"snapshot.repo":               "GIT_SNAPSHOT_REPO",
		"snapshot.ref":                "GIT_SNAPSHOT_REF",
		"snapshot.namespace":          "GIT_SNAPSHOT_NAMESPACE",
		"snapshot.base":               "GIT_SNAPSHOT_BASE",
		"snapshot.includeuntracked":   "GIT_SNAPSHOT_INCLUDE_UNTRACKED",
		"snapshot.includeignored":     "GIT_SNAPSHOT_INCLUDE_IGNORED",
		"snapshot.createreflog":       "GIT_SNAPSHOT_CREATE_REFLOG",
		"snapshot.messagetemplate":    "GIT_SNAPSHOT_MESSAGE_TEMPLATE",
		"snapshot.sign":               "GIT_SNAPSHOT_SIGN",
		"snapshot.signingkey":         "GIT_SNAPSHOT_SIGNING_KEY",
		"snapshot.outputformat":       "GIT_SNAPSHOT_OUTPUT_FORMAT",
		"snapshot.color":              "GIT_SNAPSHOT_COLOR",
		"snapshot.yes":                "GIT_SNAPSHOT_YES",
		"snapshot.restoredestination": "GIT_SNAPSHOT_RESTORE_DESTINATION",
		"snapshot.retention":          "GIT_SNAPSHOT_RETENTION",
		"snapshot.locktimeout":        "GIT_SNAPSHOT_LOCK_TIMEOUT",
		"snapshot.defaultcommand":     "GIT_SNAPSHOT_DEFAULT_COMMAND",
	}
	for key, name := range env {
		if value, ok := os.LookupEnv(name); ok {
			if err := c.set(key, value, "env:"+name); err != nil {
				return err
			}
		}
	}
	if value, ok := os.LookupEnv("GIT_SNAPSHOT_CONFIG_FILE"); ok {
		c.ConfigFile = value
	}
	return nil
}

func (c *Config) set(key, value, origin string) error {
	canonical := canonicalConfigKey(key)
	bad := func(reason string) error {
		return fail(ExitUsage, fmt.Sprintf("invalid value for %s from %s", canonical, origin), reason, nil)
	}
	parseBool := func() (bool, error) {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false, bad("Use true or false.")
		}
		return parsed, nil
	}
	switch canonical {
	case "snapshot.repo":
		c.RepoPath = value
	case "snapshot.ref":
		c.Ref = value
	case "snapshot.namespace":
		c.Namespace = value
	case "snapshot.base":
		c.Base = value
	case "snapshot.includeUntracked":
		v, err := parseBool()
		if err != nil {
			return err
		}
		c.IncludeUntracked = v
	case "snapshot.includeIgnored":
		v, err := parseBool()
		if err != nil {
			return err
		}
		c.IncludeIgnored = v
	case "snapshot.createReflog":
		v, err := parseBool()
		if err != nil {
			return err
		}
		c.CreateReflog = v
	case "snapshot.messageTemplate":
		c.MessageTemplate = value
	case "snapshot.sign":
		v, err := parseBool()
		if err != nil {
			return err
		}
		c.Sign = v
	case "snapshot.signingKey":
		c.SigningKey = value
	case "snapshot.outputFormat":
		if value != "human" && value != "json" {
			return bad("Use human or json.")
		}
		c.OutputFormat = value
	case "snapshot.color":
		if value != "auto" && value != "always" && value != "never" {
			return bad("Use auto, always, or never.")
		}
		c.Color = value
	case "snapshot.yes":
		v, err := parseBool()
		if err != nil {
			return err
		}
		c.Yes = v
	case "snapshot.restoreDestination":
		c.RestoreDestination = value
	case "snapshot.retention":
		v, err := strconv.Atoi(value)
		if err != nil || v < 0 {
			return bad("Use a non-negative integer.")
		}
		c.Retention = v
	case "snapshot.lockTimeout":
		v, err := time.ParseDuration(value)
		if err != nil || v < 0 {
			return bad("Use a non-negative Go duration such as 5s.")
		}
		c.LockTimeout = v
	case "snapshot.defaultCommand":
		if value != "create" && value != "help" {
			return bad("Use create or help.")
		}
		c.DefaultCommand = value
	default:
		return nil
	}
	c.Values[canonical] = ConfigValue{Value: value, Origin: origin}
	return nil
}

func canonicalConfigKey(key string) string {
	lower := strings.ToLower(key)
	for _, known := range configKeys {
		if strings.ToLower(known) == lower {
			return known
		}
	}
	return key
}
