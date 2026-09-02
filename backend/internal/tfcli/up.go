package tfcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
	"github.com/dotneet/thinkingface/backend/internal/tfcli/local"
)

const upUsage = `usage: tf up PATH [flags]

Create a repository if needed (kind inferred from PATH's contents, name from
PATH's basename, namespace = you) and push PATH's contents to it in one
commit.

Flags:
  --to NS/NAME|NAME      destination repository (default: your namespace +
                         the directory name); a "datasets/" or "models/"
                         prefix on NS/NAME also pins the kind
  --kind dataset|model   pin the repository kind (default: inferred)
  --rev BRANCH           branch to push to (default: main)
  -m, --message MSG      commit summary
  --license L            set the license in the repository card
  --tag T                add a tag to the repository card (repeatable,
                         comma-separated values also accepted)
  --desc TEXT            set the description in the repository card
  --include GLOB          only include files matching GLOB (repeatable)
  --exclude GLOB          exclude files matching GLOB (repeatable)
  --hidden               also upload dot-files and dot-directories found
                         under PATH (default: skipped, except
                         .gitattributes and .gitignore)
  --delete               remove remote files that are not present locally
                         (the root .gitattributes and README.md are kept)
  --dry-run              show what would happen without changing anything
  --workers N            concurrent LFS transfers (default 4)
  --quiet                do not print progress to stderr
  --json                 print the final result as one JSON line on stdout
  --endpoint URL         server URL
  --token TOKEN          API token (or THINKINGFACE_API_KEY in the environment)
  --api-key KEY          alias of --token
  --verbose              print credential resolution to stderr
`

// upOptions holds the parsed flags of `tf up`.
type upOptions struct {
	to      string
	kind    string
	rev     string
	message string
	license string
	tags    sliceFlag
	desc    string
	include sliceFlag
	exclude sliceFlag
	hidden  bool
	del     bool
	dryRun  bool
	workers int
	quiet   bool
	jsonOut bool
}

// sliceFlag is a flag.Value collecting repeated string flags; when split is
// true each occurrence is additionally split on commas ("--tag a,b --tag c"
// -> [a b c]).
type sliceFlag struct {
	values []string
	split  bool
}

func (s *sliceFlag) String() string { return strings.Join(s.values, ",") }

func (s *sliceFlag) Set(v string) error {
	if !s.split {
		s.values = append(s.values, v)
		return nil
	}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			s.values = append(s.values, part)
		}
	}
	return nil
}

func runUp(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cf := addCommonFlags(fs)

	var opt upOptions
	opt.tags.split = true
	fs.StringVar(&opt.to, "to", "", "destination repository")
	fs.StringVar(&opt.kind, "kind", "", "dataset|model")
	fs.StringVar(&opt.rev, "rev", "main", "branch")
	fs.StringVar(&opt.message, "m", "", "commit summary")
	fs.StringVar(&opt.message, "message", "", "commit summary")
	fs.StringVar(&opt.license, "license", "", "license id")
	fs.Var(&opt.tags, "tag", "tag (repeatable, comma-separated)")
	fs.StringVar(&opt.desc, "desc", "", "description")
	fs.Var(&opt.include, "include", "include glob (repeatable)")
	fs.Var(&opt.exclude, "exclude", "exclude glob (repeatable)")
	fs.BoolVar(&opt.hidden, "hidden", false, "also upload dot-files and dot-directories")
	fs.BoolVar(&opt.del, "delete", false, "delete remote files missing locally")
	fs.BoolVar(&opt.dryRun, "dry-run", false, "show what would happen")
	fs.IntVar(&opt.workers, "workers", 4, "concurrent LFS transfers")
	fs.BoolVar(&opt.quiet, "quiet", false, "suppress progress output")
	fs.BoolVar(&opt.jsonOut, "json", false, "print the result as JSON")

	if hasHelpFlag(args) {
		fmt.Fprint(stdout, upUsage)
		return exitOK
	}
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprint(stderr, upUsage)
		return exitUsage
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "tf: up requires exactly one argument (PATH)")
		fmt.Fprint(stderr, upUsage)
		return exitUsage
	}
	path := positionals[0]

	if strings.HasPrefix(path, "gs://") {
		fmt.Fprintln(stderr, "tf: gs:// import is not supported yet; copy the data locally first")
		return exitError
	}

	// allPaths is every file actually on disk under path, ignoring
	// --include/--exclude; it feeds Plan.LocalPaths below so --delete only
	// ever removes remote files that are truly gone locally, not ones this
	// run simply chose not to upload. skipped covers the rest of that
	// question: paths the walk left out, and (its Dir entries) directories
	// it never looked inside, which --delete must not treat as empty. One
	// scan produces all three.
	files, allPaths, skipped, err := local.Scan(path, local.Options{
		Include: opt.include.values,
		Exclude: opt.exclude.values,
		Hidden:  opt.hidden,
	})
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", err)
		return exitError
	}
	// Warnings survive --quiet: --quiet suppresses progress, and content
	// silently left out of the upload is not progress.
	warnSkipped(stderr, skipped)
	if len(files) == 0 {
		fmt.Fprintf(stderr, "tf: no files found under %s\n", path)
		return exitError
	}

	resolved, err := resolveCreds(cf, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", err)
		return exitError
	}
	if resolved.Token == "" {
		fmt.Fprintln(stderr, "tf: "+notLoggedInHint)
		return exitError
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := hub.New(resolved.Endpoint, resolved.Token, hub.WithUserAgent(userAgent()))

	me, err := client.Whoami(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", describeHubError(err, resolved.Endpoint, ""))
		return exitError
	}

	ref, kindPinned, err := resolveUpTarget(opt.to, opt.kind, path, me.Name, files)
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", err)
		return exitError
	}

	exists, err := client.RepoExists(ctx, ref)
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", describeHubError(err, resolved.Endpoint, ref.Namespace))
		return exitError
	}
	if !exists && !kindPinned {
		otherKind := hub.KindModel
		if ref.Kind == hub.KindModel {
			otherKind = hub.KindDataset
		}
		otherRef := hub.Ref{Kind: otherKind, Namespace: ref.Namespace, Name: ref.Name}
		otherExists, oerr := client.RepoExists(ctx, otherRef)
		if oerr != nil {
			fmt.Fprintf(stderr, "tf: %s\n", describeHubError(oerr, resolved.Endpoint, ref.Namespace))
			return exitError
		}
		if otherExists {
			if !opt.quiet {
				fmt.Fprintf(stderr, "using existing %s repository %s\n", otherKind, otherRef.ID())
			}
			ref = otherRef
			exists = true
		}
	}

	if !opt.quiet {
		label := "existing repository"
		if !exists {
			label = "new repository"
		}
		fmt.Fprintf(stderr, "→ %s (%s)\n", ref.String(), label)
	}

	created := false
	if !exists {
		if opt.dryRun {
			if !opt.quiet {
				fmt.Fprintln(stderr, "  would create repository")
			}
		} else {
			created, err = client.CreateRepo(ctx, ref)
			if err != nil {
				fmt.Fprintf(stderr, "tf: %s\n", describeHubError(err, resolved.Endpoint, ref.Namespace))
				return exitError
			}
		}
	}

	uploadFiles, err := buildUploadFiles(files, local.CardOptions{
		License:     opt.license,
		Tags:        opt.tags.values,
		Description: opt.desc,
		Title:       ref.Name,
	})
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", err)
		return exitError
	}

	// A dry run against a repository that doesn't exist (and won't be
	// created) has nothing on the server to diff against: Tree, Preupload
	// and Commit all require an existing repository and would 404 for real,
	// not "unborn branch". Report what's on disk directly instead of asking
	// hub.Upload to talk to a repository that isn't there. Whether each file
	// would travel as LFS is unknown until the repository exists (that
	// routing comes from the server's preupload answer), so it is not
	// claimed here.
	if !exists && opt.dryRun {
		return reportNewRepoDryRun(stdout, stderr, client, ref, opt, uploadFiles)
	}

	var planned *hub.Result
	progress := func(ev hub.Event) {
		if ev.Kind == hub.EventPlanned {
			planned = ev.Result
		}
		if opt.quiet {
			return
		}
		switch ev.Kind {
		case hub.EventHashing:
			if ev.Mode == hub.ModeLFS {
				fmt.Fprintf(stderr, "  hashing %s (%s)\n", ev.Path, formatSize(ev.Size))
			}
		case hub.EventSkipped:
			fmt.Fprintf(stderr, "  = %s unchanged\n", ev.Path)
		case hub.EventUploadStart:
			fmt.Fprintf(stderr, "  ↑ %s %s [lfs]\n", ev.Path, formatSize(ev.Size))
		case hub.EventDeduplicated:
			fmt.Fprintf(stderr, "  · %s already on server (dedup)\n", ev.Path)
		case hub.EventCommitting:
			n := 0
			if planned != nil {
				n = len(planned.Regular) + len(planned.LFS) + len(planned.Deleted)
			}
			fmt.Fprintf(stderr, "  committing %s…\n", countFiles(n))
		}
	}

	plan := hub.Plan{
		Ref:              ref,
		Rev:              opt.rev,
		Files:            uploadFiles,
		LocalPaths:       allPaths,
		LocalUnknownDirs: local.SkippedDirs(skipped),
		DeleteMissing:    opt.del,
		Summary:          opt.message,
		Workers:          opt.workers,
		DryRun:           opt.dryRun,
	}

	result, uerr := hub.Upload(ctx, client, plan, progress)
	nothingToDo := errors.Is(uerr, hub.ErrNothingToDo)
	if uerr != nil && !nothingToDo {
		if errors.Is(uerr, context.Canceled) {
			fmt.Fprintln(stderr, "tf: interrupted")
			return exitError
		}
		fmt.Fprintf(stderr, "tf: %s\n", describeHubError(uerr, resolved.Endpoint, ref.Namespace))
		return exitError
	}
	if result == nil {
		fmt.Fprintln(stderr, "tf: internal error: no result from upload")
		return exitError
	}

	if !opt.quiet {
		printUpSummary(stderr, client, ref, opt.rev, result, nothingToDo, opt.dryRun)
	}

	if opt.jsonOut {
		if err := writeUpJSON(stdout, client, ref, opt, result, created, nothingToDo); err != nil {
			fmt.Fprintf(stderr, "tf: %s\n", err)
			return exitError
		}
	}

	return exitOK
}

// maxSkipWarnings bounds the per-path skip warnings; a tree full of sockets
// should say so once, not scroll the real output away.
const maxSkipWarnings = 10

// warnSkipped tells the user about content the scan left out of the upload.
// A symlinked directory is the case that matters: it is not followed (a link
// back up the tree would loop), so its files simply never appear -- which,
// without a word on stderr, looks exactly like an upload that had nothing to
// do. Dot-paths are reported too, on one grouped line naming the flag that
// includes them: leaving a ".env" out is the right default, but a user who
// wanted it uploaded has to be able to find out why it wasn't. The routine
// exclusions (.git, __pycache__) are not Notable and stay quiet. Nothing
// here is deleted from the remote either: hub.Plan's LocalPaths and
// LocalUnknownDirs keep --delete away from these paths.
func warnSkipped(stderr io.Writer, skipped []local.Skipped) {
	var hiddenPaths []string
	shown := 0
	unshown := 0
	for _, s := range skipped {
		if !s.Notable() {
			continue
		}
		if s.Reason == local.ReasonHidden {
			// A trailing "/" says the whole subtree went unread.
			if s.Dir {
				hiddenPaths = append(hiddenPaths, s.RepoPath+"/")
			} else {
				hiddenPaths = append(hiddenPaths, s.RepoPath)
			}
			continue
		}
		if shown >= maxSkipWarnings {
			unshown++
			continue
		}
		shown++
		if s.Dir {
			fmt.Fprintf(stderr, "tf: warning: not uploading %s or anything below it (%s)\n", s.RepoPath, s.Reason)
			continue
		}
		fmt.Fprintf(stderr, "tf: warning: not uploading %s (%s)\n", s.RepoPath, s.Reason)
	}
	if unshown > 0 {
		fmt.Fprintf(stderr, "tf: warning: %d more path(s) skipped\n", unshown)
	}
	warnHidden(stderr, hiddenPaths)
}

// warnHidden prints the one grouped line about skipped dot-paths. It is a
// single line however many there are: a project directory can easily hold a
// dozen, and ten separate warnings would push the real output off the screen
// for something that is the documented default.
func warnHidden(stderr io.Writer, paths []string) {
	if len(paths) == 0 {
		return
	}
	listed := paths
	suffix := ""
	if len(listed) > maxSkipWarnings {
		listed = listed[:maxSkipWarnings]
		suffix = fmt.Sprintf(" and %d more", len(paths)-maxSkipWarnings)
	}
	fmt.Fprintf(stderr, "tf: warning: not uploading %d hidden path(s): %s%s (pass --hidden to include them)\n",
		len(paths), strings.Join(listed, ", "), suffix)
}

// reportNewRepoDryRun prints and (with --json) writes the dry-run result for
// a repository that doesn't exist yet, without contacting the server about
// its (nonexistent) contents. See the call site in runUp for why this can't
// just go through hub.Upload like every other dry run does.
func reportNewRepoDryRun(stdout, stderr io.Writer, client *hub.Client, ref hub.Ref, opt upOptions, files []hub.LocalFile) int {
	var totalBytes int64
	for _, f := range files {
		totalBytes += f.Size
	}
	if !opt.quiet {
		fmt.Fprintf(stderr, "(dry run) would upload %s (%s)\n", countFiles(len(files)), formatSize(totalBytes))
	}
	if opt.jsonOut {
		jr := upResultJSON{
			Repo:    ref.ID(),
			Kind:    string(ref.Kind),
			Rev:     opt.rev,
			Created: false,
			URL:     client.WebURL(ref),
			Files:   len(files),
			Bytes:   totalBytes,
			DryRun:  true,
		}
		if err := json.NewEncoder(stdout).Encode(&jr); err != nil {
			fmt.Fprintf(stderr, "tf: %s\n", err)
			return exitError
		}
	}
	return exitOK
}

// resolveUpTarget applies the --to / --kind / path-inference priority order:
// --kind (or a "datasets/"/"models/" prefix on --to) beats local.InferKind;
// --to's namespace beats the caller's own namespace; --to's name beats
// local.RepoNameFromPath. kindPinned reports whether the kind was pinned by
// a flag (as opposed to inferred), which callers use to decide whether the
// "try the other kind" existence fallback applies.
func resolveUpTarget(to, kindFlag, path, selfName string, files []local.File) (ref hub.Ref, kindPinned bool, err error) {
	var toKindPrefix, ns, name string
	if to != "" {
		prefixKind, toNs, toName, perr := local.ParseTarget(to)
		if perr != nil {
			return hub.Ref{}, false, perr
		}
		toKindPrefix = prefixKind
		ns = toNs
		name = toName
		if ns == "" {
			ns = selfName
		}
	} else {
		ns = selfName
		n, nerr := local.RepoNameFromPath(path)
		if nerr != nil {
			return hub.Ref{}, false, nerr
		}
		name = n
	}

	kindStr := kindFlag
	if kindStr == "" {
		kindStr = toKindPrefix
	}
	if kindStr != "" {
		kindPinned = true
	} else {
		kindStr, _ = local.InferKind(files)
	}

	kind, kerr := hub.ParseKind(kindStr)
	if kerr != nil {
		return hub.Ref{}, false, kerr
	}

	return hub.Ref{Kind: kind, Namespace: ns, Name: name}, kindPinned, nil
}

// buildUploadFiles converts the scanned local files into hub.LocalFile,
// generating or merging README.md's front matter from cardOpts when it is
// non-empty (see docs/dev/tf-cli.md, "repository card").
func buildUploadFiles(files []local.File, cardOpts local.CardOptions) ([]hub.LocalFile, error) {
	var readme *local.File
	for i := range files {
		if files[i].RepoPath == "README.md" {
			readme = &files[i]
			break
		}
	}

	out := make([]hub.LocalFile, 0, len(files)+1)
	for _, f := range files {
		if readme != nil && f.RepoPath == "README.md" && !cardOpts.Empty() {
			continue // replaced below
		}
		out = append(out, hub.LocalFile{RepoPath: f.RepoPath, Size: f.Size, Open: f.Open})
	}

	if cardOpts.Empty() {
		return out, nil
	}

	if readme == nil {
		content := local.BuildReadme(cardOpts)
		out = append(out, generatedFile("README.md", content))
		return out, nil
	}

	data, rerr := os.ReadFile(readme.AbsPath)
	if rerr != nil {
		return nil, fmt.Errorf("reading %s: %w", readme.AbsPath, rerr)
	}

	var content []byte
	if len(bytes.TrimSpace(data)) == 0 {
		content = local.BuildReadme(cardOpts)
	} else {
		merged, merr := local.MergeReadme(data, cardOpts)
		if merr != nil {
			return nil, merr
		}
		content = merged
	}
	out = append(out, generatedFile("README.md", content))
	return out, nil
}

// generatedFile wraps in-memory content (a generated or merged README) as a
// hub.LocalFile.
func generatedFile(repoPath string, content []byte) hub.LocalFile {
	return hub.LocalFile{
		RepoPath: repoPath,
		Size:     int64(len(content)),
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(content)), nil
		},
	}
}

// printUpSummary writes the final progress line(s) for `tf up` to stderr.
func printUpSummary(stderr io.Writer, client *hub.Client, ref hub.Ref, rev string, result *hub.Result, nothingToDo, dryRun bool) {
	switch {
	case nothingToDo:
		fmt.Fprintf(stderr, "✓ %s@%s up to date (%s unchanged)\n", ref.String(), rev, countFiles(len(result.Unchanged)))
	case dryRun:
		total := len(result.Regular) + len(result.LFS)
		fmt.Fprintf(stderr, "(dry run) would upload %s (%s; %d via LFS)\n", countFiles(total), formatSize(result.Bytes), len(result.LFS))
		if len(result.Deleted) > 0 {
			fmt.Fprintf(stderr, "(dry run) would delete %s\n", countFiles(len(result.Deleted)))
		}
	default:
		total := len(result.Regular) + len(result.LFS)
		oid := ""
		if result.Commit != nil {
			oid = shortOID(result.Commit.OID)
		}
		var extra strings.Builder
		if len(result.LFS) > 0 {
			fmt.Fprintf(&extra, "; %d via LFS, %s uploaded", len(result.LFS), formatSize(result.UploadedBytes))
		}
		if len(result.Unchanged) > 0 {
			fmt.Fprintf(&extra, ", %d unchanged", len(result.Unchanged))
		}
		if len(result.Deleted) > 0 {
			fmt.Fprintf(&extra, ", %s deleted", countFiles(len(result.Deleted)))
		}
		fmt.Fprintf(stderr, "✓ %s@%s %s — %s (%s%s)\n", ref.String(), rev, oid, countFiles(total), formatSize(result.Bytes), extra.String())
		if result.Commit != nil {
			fmt.Fprintf(stderr, "  %s\n", client.WebURL(ref))
		}
	}
}

// countFiles renders "1 file" / "N files".
func countFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// upResultJSON is the shape written to stdout by `tf up --json`.
type upResultJSON struct {
	Repo          string `json:"repo"`
	Kind          string `json:"kind"`
	Rev           string `json:"rev"`
	Created       bool   `json:"created"`
	Commit        string `json:"commit"`
	URL           string `json:"url"`
	CommitURL     string `json:"commit_url"`
	Files         int    `json:"files"`
	LFSFiles      int    `json:"lfs_files"`
	Unchanged     int    `json:"unchanged"`
	Deleted       int    `json:"deleted"`
	Bytes         int64  `json:"bytes"`
	UploadedBytes int64  `json:"uploaded_bytes"`
	DryRun        bool   `json:"dry_run"`
	NothingToDo   bool   `json:"nothing_to_do"`
}

func writeUpJSON(stdout io.Writer, client *hub.Client, ref hub.Ref, opt upOptions, result *hub.Result, created, nothingToDo bool) error {
	jr := upResultJSON{
		Repo:          ref.ID(),
		Kind:          string(ref.Kind),
		Rev:           opt.rev,
		Created:       created,
		URL:           client.WebURL(ref),
		Files:         len(result.Regular) + len(result.LFS),
		LFSFiles:      len(result.LFS),
		Unchanged:     len(result.Unchanged),
		Deleted:       len(result.Deleted),
		Bytes:         result.Bytes,
		UploadedBytes: result.UploadedBytes,
		DryRun:        opt.dryRun,
		NothingToDo:   nothingToDo,
	}
	if result.Commit != nil {
		jr.Commit = result.Commit.OID
		jr.CommitURL = result.Commit.URL
	}
	enc := json.NewEncoder(stdout)
	return enc.Encode(&jr)
}
