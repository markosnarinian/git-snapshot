package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

var Version = "dev"

type App struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

func New() *App {
	return &App{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}
}

type commonFlags struct {
	repo               string
	ref                string
	namespace          string
	base               string
	includeUntracked   bool
	includeIgnored     bool
	createReflog       bool
	messageTemplate    string
	sign               bool
	signingKey         string
	outputFormat       string
	color              string
	yes                bool
	restoreDestination string
	retention          int
	lockTimeout        time.Duration
	configFile         string
	json               bool
	quiet              bool
	verbose            bool
}

func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) > 0 && (args[0] == "--version" || args[0] == "version") {
		fmt.Fprintf(a.Out, "git-snapshot %s\n", Version)
		return nil
	}
	command := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	if command == "help" || command == "--help" || command == "-h" {
		a.writeUsage()
		return nil
	}
	if command == "" {
		cfg, err := a.loadConfig(ctx, args)
		if err != nil {
			return err
		}
		if cfg.DefaultCommand == "help" {
			a.writeUsage()
			return nil
		}
		command = cfg.DefaultCommand
	}
	switch command {
	case "create":
		return a.runCreate(ctx, args)
	case "list":
		return a.runList(ctx, args)
	case "show":
		return a.runShow(ctx, args)
	case "diff":
		return a.runDiff(ctx, args)
	case "verify":
		return a.runVerify(ctx, args)
	case "restore":
		return a.runRestore(ctx, args)
	case "drop":
		return a.runDrop(ctx, args)
	case "delete":
		return a.runDelete(ctx, args)
	case "config":
		return a.runConfig(ctx, args)
	default:
		return fail(ExitUsage, fmt.Sprintf("unknown command %q", command), "Run git snapshot help for available commands.", nil)
	}
}

func (a *App) loadConfig(ctx context.Context, args []string) (Config, error) {
	repo, file := bootstrapPaths(args)
	if file == "" {
		file = os.Getenv("GIT_SNAPSHOT_CONFIG_FILE")
	}
	return LoadConfig(ctx, repo, file)
}

func bootstrapPaths(args []string) (repo, configFile string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case strings.HasPrefix(arg, "--repo="):
			repo = strings.TrimPrefix(arg, "--repo=")
		case arg == "--repo" && i+1 < len(args):
			i++
			repo = args[i]
		case strings.HasPrefix(arg, "--config-file="):
			configFile = strings.TrimPrefix(arg, "--config-file=")
		case arg == "--config-file" && i+1 < len(args):
			i++
			configFile = args[i]
		}
	}
	return repo, configFile
}

func (a *App) flagSet(command string, cfg Config) (*flag.FlagSet, *commonFlags) {
	fs := flag.NewFlagSet("git snapshot "+command, flag.ContinueOnError)
	fs.SetOutput(a.Err)
	c := &commonFlags{
		repo:               cfg.RepoPath,
		ref:                cfg.Ref,
		namespace:          cfg.Namespace,
		base:               cfg.Base,
		includeUntracked:   cfg.IncludeUntracked,
		includeIgnored:     cfg.IncludeIgnored,
		createReflog:       cfg.CreateReflog,
		messageTemplate:    cfg.MessageTemplate,
		sign:               cfg.Sign,
		signingKey:         cfg.SigningKey,
		outputFormat:       cfg.OutputFormat,
		color:              cfg.Color,
		yes:                cfg.Yes,
		restoreDestination: cfg.RestoreDestination,
		retention:          cfg.Retention,
		lockTimeout:        cfg.LockTimeout,
		configFile:         cfg.ConfigFile,
		json:               cfg.OutputFormat == "json",
	}
	fs.StringVar(&c.repo, "repo", c.repo, "repository path")
	fs.StringVar(&c.ref, "ref", c.ref, "full snapshot ref")
	fs.StringVar(&c.namespace, "namespace", c.namespace, "permitted snapshot ref prefix")
	fs.StringVar(&c.base, "base", c.base, "base revision for a new stream")
	fs.BoolVar(&c.includeUntracked, "include-untracked", c.includeUntracked, "include non-ignored untracked files")
	fs.BoolFunc("no-include-untracked", "exclude untracked files", func(string) error { c.includeUntracked = false; return nil })
	fs.BoolVar(&c.includeIgnored, "include-ignored", c.includeIgnored, "include ignored files (may expose secrets)")
	fs.BoolFunc("no-include-ignored", "exclude ignored files", func(string) error { c.includeIgnored = false; return nil })
	fs.BoolVar(&c.createReflog, "create-reflog", c.createReflog, "create a reflog for a new snapshot ref")
	fs.BoolFunc("no-create-reflog", "do not create a reflog", func(string) error { c.createReflog = false; return nil })
	fs.StringVar(&c.messageTemplate, "message-template", c.messageTemplate, "default commit-message template")
	fs.BoolVar(&c.sign, "sign", c.sign, "sign snapshot commits")
	fs.BoolFunc("no-sign", "do not sign snapshot commits", func(string) error { c.sign = false; return nil })
	fs.StringVar(&c.signingKey, "signing-key", c.signingKey, "commit signing key")
	fs.StringVar(&c.outputFormat, "output-format", c.outputFormat, "default output format: human or json")
	fs.StringVar(&c.color, "color", c.color, "color behavior: auto, always, or never")
	fs.BoolVar(&c.yes, "yes", c.yes, "answer yes to required confirmations")
	fs.StringVar(&c.restoreDestination, "restore-destination", c.restoreDestination, "default restore destination")
	fs.IntVar(&c.retention, "retention", c.retention, "configured retention count (0 means unlimited)")
	fs.DurationVar(&c.lockTimeout, "lock-timeout", c.lockTimeout, "ref-lock retry timeout")
	fs.StringVar(&c.configFile, "config-file", c.configFile, "explicit configuration file (lowest-precedence config layer)")
	fs.BoolVar(&c.json, "json", c.json, "emit stable JSON")
	fs.BoolVar(&c.quiet, "quiet", false, "suppress normal human output")
	fs.BoolVar(&c.verbose, "verbose", false, "show Git commands on stderr")
	return fs, c
}

func (a *App) prepare(ctx context.Context, c *commonFlags) (*Repository, Git, error) {
	if c.outputFormat != "human" && c.outputFormat != "json" {
		return nil, Git{}, fail(ExitUsage, "invalid --output-format", "Use human or json.", nil)
	}
	if c.color != "auto" && c.color != "always" && c.color != "never" {
		return nil, Git{}, fail(ExitUsage, "invalid --color value", "Use auto, always, or never.", nil)
	}
	if c.retention < 0 {
		return nil, Git{}, fail(ExitUsage, "invalid --retention value", "Use a non-negative integer.", nil)
	}
	if c.lockTimeout < 0 {
		return nil, Git{}, fail(ExitUsage, "invalid --lock-timeout value", "Use a non-negative duration such as 5s.", nil)
	}
	repo, err := DiscoverRepository(ctx, c.repo, c.verbose, a.Err)
	if err != nil {
		return nil, Git{}, err
	}
	git := repo.Git(c.verbose, a.Err)
	if err := ValidateSnapshotRef(ctx, git, c.ref, c.namespace); err != nil {
		return nil, Git{}, err
	}
	return repo, git, nil
}

func (a *App) runCreate(ctx context.Context, args []string) error {
	cfg, err := a.loadConfig(ctx, args)
	if err != nil {
		return err
	}
	fs, common := a.flagSet("create", cfg)
	var message, messageFile string
	var allowInProgress, allowDirtySubmodules, dryRun bool
	fs.StringVar(&message, "message", "", "snapshot commit message")
	fs.StringVar(&messageFile, "message-file", "", "read snapshot message from a file")
	fs.BoolVar(&allowInProgress, "allow-in-progress", false, "allow conflicts or an in-progress Git operation")
	fs.BoolVar(&allowDirtySubmodules, "allow-dirty-submodules", false, "record only gitlinks for dirty submodules")
	fs.BoolVar(&dryRun, "dry-run", false, "capture a tree but do not create a commit or update the ref")
	if err := parseFlags(fs, args); err != nil {
		return usageError(err)
	}
	if fs.NArg() != 0 {
		return fail(ExitUsage, "create does not accept positional arguments", "Pass the message with --message.", nil)
	}
	if common.includeIgnored {
		fmt.Fprintln(a.Err, "WARNING: --include-ignored can store secrets, credentials, dependencies, and build output permanently in Git objects.")
		if !common.yes {
			if err := a.confirm("Continue and include every ignored file?"); err != nil {
				return err
			}
		}
	}
	if allowInProgress {
		fmt.Fprintln(a.Err, "WARNING: --allow-in-progress captures conflict markers and transient operation state only as ordinary file content; Git operation metadata is not snapshotted.")
	}
	if allowDirtySubmodules {
		fmt.Fprintln(a.Err, "WARNING: --allow-dirty-submodules records only each submodule gitlink commit, not modified or untracked submodule files.")
	}
	repo, git, err := a.prepare(ctx, common)
	if err != nil {
		return err
	}
	result, err := Create(ctx, repo, git, CreateOptions{
		Ref: common.ref, Namespace: common.namespace, Base: common.base,
		Message: message, MessageFile: messageFile, MessageTemplate: common.messageTemplate,
		IncludeUntracked: common.includeUntracked, IncludeIgnored: common.includeIgnored,
		AllowInProgress: allowInProgress, AllowDirtySubmodules: allowDirtySubmodules,
		Sign: common.sign, SigningKey: common.signingKey, CreateReflog: common.createReflog,
		DryRun: dryRun, Retention: common.retention, LockTimeout: common.lockTimeout,
	})
	if err != nil {
		return err
	}
	if common.json || common.outputFormat == "json" {
		return writeJSON(a.Out, result)
	}
	if !common.quiet {
		if result.DryRun {
			fmt.Fprintf(a.Out, "Would create snapshot tree %s on %s\n", result.Tree, result.Ref)
		} else {
			fmt.Fprintf(a.Out, "Created snapshot %s on %s\n", result.OID, result.Ref)
		}
	}
	return nil
}

func (a *App) runList(ctx context.Context, args []string) error {
	cfg, err := a.loadConfig(ctx, args)
	if err != nil {
		return err
	}
	fs, common := a.flagSet("list", cfg)
	var limit int
	var reverse, showRef, showBase, showSize bool
	var format string
	fs.IntVar(&limit, "limit", 0, "maximum snapshots to list")
	fs.BoolVar(&reverse, "reverse", false, "show oldest first")
	fs.StringVar(&format, "format", "", "format using %d %H %h %cI %s %r %b")
	fs.BoolVar(&showRef, "show-ref", false, "show the selected ref")
	fs.BoolVar(&showBase, "show-base", false, "show the original base")
	fs.BoolVar(&showSize, "show-size", false, "show commit object size")
	if err := parseFlags(fs, args); err != nil {
		return usageError(err)
	}
	if fs.NArg() != 0 || limit < 0 {
		return fail(ExitUsage, "invalid list arguments", "Use --limit with a non-negative number.", nil)
	}
	repo, git, err := a.prepare(ctx, common)
	if err != nil {
		return err
	}
	stream, err := List(ctx, repo, git, common.ref, ListOptions{Limit: limit, Reverse: reverse, ShowSize: showSize})
	if err != nil {
		return err
	}
	if common.json || common.outputFormat == "json" {
		return writeJSON(a.Out, stream)
	}
	if showRef {
		fmt.Fprintf(a.Out, "ref %s\n", stream.Ref)
	}
	if showBase {
		fmt.Fprintf(a.Out, "base %s\n", stream.Base)
	}
	for _, snapshot := range stream.Snapshots {
		line := FormatSnapshot(format, snapshot, snapshot.Distance)
		if showSize {
			line += fmt.Sprintf(" %d bytes", snapshot.Size)
		}
		fmt.Fprintln(a.Out, line)
	}
	return nil
}

func (a *App) runShow(ctx context.Context, args []string) error {
	cfg, err := a.loadConfig(ctx, args)
	if err != nil {
		return err
	}
	fs, common := a.flagSet("show", cfg)
	var stat, nameOnly, patch, allowUnreachable bool
	var format string
	fs.BoolVar(&stat, "stat", false, "show diffstat")
	fs.BoolVar(&nameOnly, "name-only", false, "show changed path names")
	fs.BoolVar(&patch, "patch", false, "show patch")
	fs.StringVar(&format, "format", "", "metadata format")
	fs.BoolVar(&allowUnreachable, "allow-unreachable", false, "allow a full CLI-owned object ID outside the current stream")
	if err := parseFlags(fs, args); err != nil {
		return usageError(err)
	}
	if fs.NArg() > 1 || countTrue(stat, nameOnly, patch) > 1 {
		return fail(ExitUsage, "invalid show arguments", "Choose at most one of --stat, --name-only, or --patch.", nil)
	}
	selector := "latest"
	if fs.NArg() == 1 {
		selector = fs.Arg(0)
	}
	repo, git, err := a.prepare(ctx, common)
	if err != nil {
		return err
	}
	snapshot, _, err := ResolveSelector(ctx, repo, git, common.ref, selector, allowUnreachable)
	if err != nil {
		return err
	}
	diff := ""
	if stat || nameOnly || patch {
		diff, err = SnapshotDiff(ctx, git, snapshot, DiffOptions{Stat: stat, NameOnly: nameOnly, Patch: patch})
		if err != nil {
			return err
		}
	}
	if common.json || common.outputFormat == "json" {
		return writeJSON(a.Out, map[string]any{"snapshot": snapshot, "diff": diff})
	}
	if format != "" {
		fmt.Fprintln(a.Out, FormatSnapshot(format, snapshot, snapshot.Distance))
	} else {
		parent := "none"
		if len(snapshot.Parents) > 0 {
			parent = snapshot.Parents[0]
		}
		fmt.Fprintf(a.Out, "snapshot %s\nversion %s\nref %s\nbase %s\ntree %s\nparent %s\ncreated %s\nsubject %s\n", snapshot.OID, snapshot.Version, snapshot.Ref, snapshot.Base, snapshot.Tree, parent, snapshot.CreatedAt.Format(time.RFC3339), snapshot.Subject)
	}
	if diff != "" {
		fmt.Fprint(a.Out, diff)
	}
	return nil
}

func (a *App) runDiff(ctx context.Context, args []string) error {
	cfg, err := a.loadConfig(ctx, args)
	if err != nil {
		return err
	}
	fs, common := a.flagSet("diff", cfg)
	var stat, nameOnly, patch, allowUnreachable bool
	fs.BoolVar(&stat, "stat", false, "show diffstat")
	fs.BoolVar(&nameOnly, "name-only", false, "show changed path names")
	fs.BoolVar(&patch, "patch", false, "show patch (default)")
	fs.BoolVar(&allowUnreachable, "allow-unreachable", false, "allow full CLI-owned IDs outside the current stream")
	if err := parseFlags(fs, args); err != nil {
		return usageError(err)
	}
	if fs.NArg() > 2 || countTrue(stat, nameOnly, patch) > 1 {
		return fail(ExitUsage, "invalid diff arguments", "Pass zero, one, or two endpoints and one output mode.", nil)
	}
	from, to := "", ""
	if fs.NArg() > 0 {
		from = fs.Arg(0)
	}
	if fs.NArg() > 1 {
		to = fs.Arg(1)
	}
	repo, git, err := a.prepare(ctx, common)
	if err != nil {
		return err
	}
	output, err := Diff(ctx, repo, git, common.ref, from, to, allowUnreachable, DiffOptions{Stat: stat, NameOnly: nameOnly, Patch: patch})
	if err != nil {
		return err
	}
	if common.json || common.outputFormat == "json" {
		return writeJSON(a.Out, map[string]any{"from": from, "to": to, "output": output})
	}
	fmt.Fprint(a.Out, output)
	return nil
}

func (a *App) runVerify(ctx context.Context, args []string) error {
	cfg, err := a.loadConfig(ctx, args)
	if err != nil {
		return err
	}
	fs, common := a.flagSet("verify", cfg)
	var allowUnreachable bool
	fs.BoolVar(&allowUnreachable, "allow-unreachable", false, "verify a full CLI-owned object ID outside the current stream")
	if err := parseFlags(fs, args); err != nil {
		return usageError(err)
	}
	if fs.NArg() > 1 {
		return fail(ExitUsage, "verify accepts at most one selector", "Use latest, a distance, or a full object ID.", nil)
	}
	repo, git, err := a.prepare(ctx, common)
	if err != nil {
		return err
	}
	if fs.NArg() == 1 {
		snapshot, stream, err := ResolveSelector(ctx, repo, git, common.ref, fs.Arg(0), allowUnreachable)
		if err != nil {
			return err
		}
		if common.json || common.outputFormat == "json" {
			return writeJSON(a.Out, map[string]any{"ok": true, "snapshot": snapshot, "stream": stream.Ref})
		}
		fmt.Fprintf(a.Out, "Verified snapshot %s for %s\n", snapshot.OID, stream.Ref)
		return nil
	}
	stream, err := VerifyStream(ctx, repo, git, common.ref)
	if err != nil {
		return err
	}
	if common.json || common.outputFormat == "json" {
		return writeJSON(a.Out, map[string]any{"ok": true, "stream": stream})
	}
	fmt.Fprintf(a.Out, "Verified %d snapshots on %s (base %s)\n", len(stream.Snapshots), stream.Ref, stream.Base)
	return nil
}

func (a *App) runRestore(ctx context.Context, args []string) error {
	cfg, err := a.loadConfig(ctx, args)
	if err != nil {
		return err
	}
	fs, common := a.flagSet("restore", cfg)
	var destination string = common.restoreDestination
	var worktree, overwrite, force, staged, dryRun, allowUnreachable bool
	fs.StringVar(&destination, "destination", destination, "separate restore destination")
	fs.BoolVar(&worktree, "worktree", false, "restore into the current working tree")
	fs.BoolVar(&overwrite, "overwrite", false, "allow a nonempty destination")
	fs.BoolVar(&force, "force", false, "permit destructive working-tree restoration")
	fs.BoolVar(&staged, "staged", false, "also replace the real index (requires --worktree)")
	fs.BoolVar(&dryRun, "dry-run", false, "preview without writing files")
	fs.BoolVar(&allowUnreachable, "allow-unreachable", false, "restore a full CLI-owned ID outside the current stream")
	if err := parseFlags(fs, args); err != nil {
		return usageError(err)
	}
	if fs.NArg() > 1 {
		return fail(ExitUsage, "restore accepts at most one selector", "Place flags before or after a single selector.", nil)
	}
	selector := "latest"
	if fs.NArg() == 1 {
		selector = fs.Arg(0)
	}
	repo, git, err := a.prepare(ctx, common)
	if err != nil {
		return err
	}
	snapshot, _, err := ResolveSelector(ctx, repo, git, common.ref, selector, allowUnreachable)
	if err != nil {
		return err
	}
	opts := RestoreOptions{Destination: destination, Worktree: worktree, Overwrite: overwrite, Force: force, Staged: staged, DryRun: dryRun}
	if worktree {
		previewOpts := opts
		previewOpts.DryRun = true
		previewOpts.Force = true
		preview, previewErr := Restore(ctx, repo, git, snapshot, previewOpts)
		if previewErr != nil {
			return previewErr
		}
		if !common.json {
			fmt.Fprintf(a.Err, "Working-tree restore preview (%d paths):\n", len(preview.Paths))
			for _, path := range preview.Paths {
				fmt.Fprintf(a.Err, "  %s\n", path)
			}
		}
		if dryRun {
			if common.json || common.outputFormat == "json" {
				return writeJSON(a.Out, preview)
			}
			return nil
		}
		if force && !common.yes {
			if err := a.confirm("Restore these paths and discard conflicting current data?"); err != nil {
				return err
			}
		}
	}
	result, err := Restore(ctx, repo, git, snapshot, opts)
	if err != nil {
		return err
	}
	if common.json || common.outputFormat == "json" {
		return writeJSON(a.Out, result)
	}
	if !common.quiet {
		fmt.Fprintf(a.Out, "Restored snapshot %s to %s\n", result.OID, result.Destination)
	}
	return nil
}

func (a *App) runDrop(ctx context.Context, args []string) error {
	cfg, err := a.loadConfig(ctx, args)
	if err != nil {
		return err
	}
	fs, common := a.flagSet("drop", cfg)
	count := 1
	fs.IntVar(&count, "count", 1, "number of newest snapshots to drop")
	if err := parseFlags(fs, args); err != nil {
		return usageError(err)
	}
	if fs.NArg() != 0 || count < 1 {
		return fail(ExitUsage, "invalid drop count", "Use --count with a positive integer.", nil)
	}
	repo, git, err := a.prepare(ctx, common)
	if err != nil {
		return err
	}
	stream, err := VerifyStream(ctx, repo, git, common.ref)
	if err != nil {
		return err
	}
	if count >= len(stream.Snapshots) {
		hint := "Use delete to remove this complete stream."
		if len(stream.Snapshots) > 1 {
			hint = fmt.Sprintf("Choose --count between 1 and %d, or use delete for the complete stream.", len(stream.Snapshots)-1)
		}
		return fail(ExitSafety, "drop would empty the snapshot stream", hint, nil)
	}
	fmt.Fprintf(a.Err, "Snapshots that may become unreachable:\n")
	for _, snapshot := range stream.Snapshots[:count] {
		fmt.Fprintf(a.Err, "  %s %s\n", snapshot.OID, snapshot.Subject)
	}
	if !common.yes {
		if err := a.confirm(fmt.Sprintf("Move %s back by %d snapshot(s)?", common.ref, count)); err != nil {
			return err
		}
	}
	result, err := Drop(ctx, repo, git, common.ref, common.namespace, stream.Tip, count, common.lockTimeout, common.createReflog)
	if err != nil {
		return err
	}
	if common.json || common.outputFormat == "json" {
		return writeJSON(a.Out, result)
	}
	fmt.Fprintf(a.Out, "Moved %s to %s\n", common.ref, result.NewTip)
	return nil
}

func (a *App) runDelete(ctx context.Context, args []string) error {
	cfg, err := a.loadConfig(ctx, args)
	if err != nil {
		return err
	}
	fs, common := a.flagSet("delete", cfg)
	if err := parseFlags(fs, args); err != nil {
		return usageError(err)
	}
	if fs.NArg() != 0 {
		return fail(ExitUsage, "delete does not accept selectors", "Delete operates only on the exact configured --ref.", nil)
	}
	repo, git, err := a.prepare(ctx, common)
	if err != nil {
		return err
	}
	stream, err := VerifyStream(ctx, repo, git, common.ref)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Err, "Snapshots that may become unreachable:\n")
	for _, snapshot := range stream.Snapshots {
		fmt.Fprintf(a.Err, "  %s %s\n", snapshot.OID, snapshot.Subject)
	}
	if !common.yes {
		if err := a.confirm(fmt.Sprintf("Delete snapshot stream %s?", common.ref)); err != nil {
			return err
		}
	}
	result, err := Delete(ctx, repo, git, common.ref, common.namespace, stream.Tip, common.lockTimeout)
	if err != nil {
		return err
	}
	if common.json || common.outputFormat == "json" {
		return writeJSON(a.Out, result)
	}
	fmt.Fprintf(a.Out, "Deleted snapshot stream %s (%d snapshots)\n", common.ref, len(result.Removed))
	return nil
}

func (a *App) confirm(question string) error {
	fmt.Fprintf(a.Err, "%s Type 'yes' to continue: ", question)
	line, err := bufio.NewReader(a.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fail(ExitSafety, "confirmation could not be read", "Retry interactively, or pass --yes only after reviewing the operation.", err)
	}
	if strings.TrimSpace(strings.ToLower(line)) != "yes" {
		return fail(ExitSafety, "operation cancelled because confirmation was not given", "Review the preview and retry when ready.", nil)
	}
	return nil
}

func parseFlags(fs *flag.FlagSet, args []string) error {
	return fs.Parse(reorderInterspersed(args))
}

var boolFlagNames = map[string]bool{
	"include-untracked": true, "no-include-untracked": true, "include-ignored": true, "no-include-ignored": true,
	"create-reflog": true, "no-create-reflog": true, "sign": true, "no-sign": true, "yes": true,
	"json": true, "quiet": true, "verbose": true, "allow-in-progress": true, "allow-dirty-submodules": true,
	"dry-run": true, "reverse": true, "show-ref": true, "show-base": true, "show-size": true,
	"stat": true, "name-only": true, "patch": true, "allow-unreachable": true, "worktree": true,
	"overwrite": true, "force": true, "staged": true, "global": true, "local": true,
	"effective": true, "show-origin": true, "help": true, "h": true,
}

func reorderInterspersed(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") || boolFlagNames[name] {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}

func usageError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return fail(ExitUsage, "invalid command arguments", "Run the command with --help for accepted flags.", err)
}

func countTrue(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func (a *App) writeUsage() {
	fmt.Fprint(a.Out, `git-snapshot safely captures Git working-tree snapshots outside branch history.

Usage:
  git snapshot <command> [options]

Commands:
  create             Capture the current working-tree state
  list               List first-parent snapshot history
  show [selector]    Show snapshot metadata or changes
  diff [from] [to]   Compare snapshots, HEAD, or the working tree
  verify [selector]  Verify ownership and stream integrity
  restore [selector] Restore to a separate directory or explicitly to the worktree
  drop               Move the snapshot ref back without rewriting commits
  delete             Delete the exact selected snapshot stream
  config get|set|unset|list

Selectors are latest, a non-negative first-parent distance, or a full object ID.
Run 'git snapshot <command> --help' for command flags.
`)
}

func (a *App) runConfig(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fail(ExitUsage, "config requires get, set, unset, or list", "Run git snapshot config list --effective.", nil)
	}
	action := args[0]
	args = args[1:]
	cfg, err := a.loadConfig(ctx, args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("git snapshot config "+action, flag.ContinueOnError)
	fs.SetOutput(a.Err)
	var repoPath, file string
	var global, local, effective, showOrigin, jsonOutput bool
	fs.StringVar(&repoPath, "repo", cfg.RepoPath, "repository path")
	fs.StringVar(&file, "file", "", "operate on an explicit Git config file")
	fs.BoolVar(&global, "global", false, "operate on global Git config")
	fs.BoolVar(&local, "local", false, "operate on repository-local Git config")
	fs.BoolVar(&effective, "effective", false, "show effective values after precedence resolution")
	fs.BoolVar(&showOrigin, "show-origin", false, "show each effective value's origin")
	fs.BoolVar(&jsonOutput, "json", false, "emit stable JSON")
	if err := parseFlags(fs, args); err != nil {
		return usageError(err)
	}
	if countTrue(global, local, file != "") > 1 {
		return fail(ExitUsage, "config scopes are mutually exclusive", "Choose one of --global, --local, or --file.", nil)
	}
	scopeArgs := []string{"config"}
	git := Git{}
	if global {
		scopeArgs = append(scopeArgs, "--global")
	} else if file != "" {
		scopeArgs = append(scopeArgs, "--file", file)
	} else {
		git = Git{Repo: repoPath}
		scopeArgs = append(scopeArgs, "--local")
	}
	switch action {
	case "get":
		if fs.NArg() != 1 {
			return fail(ExitUsage, "config get requires one key", "Example: git snapshot config get snapshot.ref", nil)
		}
		key := canonicalConfigKey(fs.Arg(0))
		if !knownConfigKey(key) {
			return fail(ExitUsage, "unknown git-snapshot configuration key", "Run git snapshot config list --effective.", nil)
		}
		if !global && !local && file == "" {
			value := cfg.Values[key]
			if jsonOutput {
				return writeJSON(a.Out, map[string]any{"key": key, "value": value.Value, "origin": value.Origin})
			}
			fmt.Fprintln(a.Out, value.Value)
			return nil
		}
		value, getErr := git.Run(ctx, append(scopeArgs, "--get", key)...)
		if getErr != nil {
			return fail(ExitNotFound, fmt.Sprintf("%s is not set in the selected scope", key), "Set it or omit the scope flag to read the effective value.", getErr)
		}
		if jsonOutput {
			return writeJSON(a.Out, map[string]string{"key": key, "value": value})
		}
		fmt.Fprintln(a.Out, value)
		return nil
	case "set":
		if fs.NArg() != 2 {
			return fail(ExitUsage, "config set requires a key and value", "Example: git snapshot config set snapshot.ref refs/snapshots/project", nil)
		}
		key, value := canonicalConfigKey(fs.Arg(0)), fs.Arg(1)
		if !knownConfigKey(key) {
			return fail(ExitUsage, "unknown git-snapshot configuration key", "Run git snapshot config list --effective.", nil)
		}
		copyCfg := cfg
		if err := copyCfg.set(key, value, "cli"); err != nil {
			return err
		}
		if err := validateConfigRefValue(ctx, key, value); err != nil {
			return err
		}
		if _, err := git.Run(ctx, append(scopeArgs, key, value)...); err != nil {
			return fail(ExitFailure, "could not write Git configuration", "Check the selected config scope and permissions.", err)
		}
		if jsonOutput {
			return writeJSON(a.Out, map[string]any{"ok": true, "key": key, "value": value})
		}
		fmt.Fprintf(a.Out, "Set %s=%s\n", key, value)
		return nil
	case "unset":
		if fs.NArg() != 1 {
			return fail(ExitUsage, "config unset requires one key", "Example: git snapshot config unset snapshot.ref", nil)
		}
		key := canonicalConfigKey(fs.Arg(0))
		if !knownConfigKey(key) {
			return fail(ExitUsage, "unknown git-snapshot configuration key", "Run git snapshot config list --effective.", nil)
		}
		if _, err := git.Run(ctx, append(scopeArgs, "--unset", key)...); err != nil {
			return fail(ExitNotFound, fmt.Sprintf("%s is not set in the selected scope", key), "No configuration was changed.", err)
		}
		if jsonOutput {
			return writeJSON(a.Out, map[string]any{"ok": true, "key": key})
		}
		fmt.Fprintf(a.Out, "Unset %s\n", key)
		return nil
	case "list":
		if fs.NArg() != 0 {
			return fail(ExitUsage, "config list accepts no positional arguments", "Use --effective and --show-origin as flags.", nil)
		}
		_ = effective
		keys := append([]string(nil), configKeys...)
		sort.Strings(keys)
		if jsonOutput {
			values := make(map[string]ConfigValue, len(keys))
			for _, key := range keys {
				values[key] = cfg.Values[key]
			}
			return writeJSON(a.Out, values)
		}
		for _, key := range keys {
			value := cfg.Values[key]
			if showOrigin {
				fmt.Fprintf(a.Out, "%s\t%s\t%s\n", value.Origin, key, value.Value)
			} else {
				fmt.Fprintf(a.Out, "%s=%s\n", key, value.Value)
			}
		}
		return nil
	default:
		return fail(ExitUsage, fmt.Sprintf("unknown config command %q", action), "Use get, set, unset, or list.", nil)
	}
}

func knownConfigKey(key string) bool {
	for _, known := range configKeys {
		if known == key {
			return true
		}
	}
	return false
}

func validateConfigRefValue(ctx context.Context, key, value string) error {
	switch key {
	case "snapshot.ref":
		if err := rejectProtectedRef(value); err != nil {
			return err
		}
		if !strings.HasPrefix(value, "refs/") {
			return fail(ExitUsage, "snapshot.ref must be a full ref", "Use refs/snapshots/name.", nil)
		}
		if _, err := (Git{}).Run(ctx, "check-ref-format", value); err != nil {
			return fail(ExitUsage, "snapshot.ref is malformed", "Use refs/snapshots/name.", err)
		}
	case "snapshot.namespace":
		return ValidateNamespace(ctx, Git{}, value)
	}
	return nil
}

func ErrorWantsJSON(args []string) bool {
	if os.Getenv("GIT_SNAPSHOT_OUTPUT_FORMAT") == "json" {
		return true
	}
	for _, arg := range args {
		if arg == "--json" || arg == "--output-format=json" {
			return true
		}
		if value, err := strconv.ParseBool(strings.TrimPrefix(arg, "--json=")); strings.HasPrefix(arg, "--json=") && err == nil && value {
			return true
		}
	}
	return false
}
