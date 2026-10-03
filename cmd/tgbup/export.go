package main

import (
	"archive/zip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/render"
	"gitlab.com/joeunderwood/tgbup/internal/store"
)

var monthRE = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// usageError marks problems with what was asked for (exit 2), as opposed to failures (exit 1).
type usageError struct{ error }

func usageErr(format string, a ...any) error { return usageError{fmt.Errorf(format, a...)} }

// cmdExport packs one chat (or a topic, or a period of it) with its media into
// a zip that opens in any browser without tgbup.
func cmdExport(ctx context.Context, args []string, env Env) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	topic := fs.String("topic", "", "forum topic: title or #id")
	from := fs.String("from", "", "first month, YYYY-MM")
	to := fs.String("to", "", "last month, YYYY-MM")
	noMedia := fs.Bool("no-media", false, "leave media files out")
	outPath := fs.String("o", "", "zip file to write")
	// The chat comes first, before the flags: flag stops at the first non-flag.
	// Group and channel ids are negative, so a number is a chat, not a flag.
	var chat string
	if len(args) > 0 && (!strings.HasPrefix(args[0], "-") || isNumber(args[0])) {
		chat, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if chat == "" && fs.NArg() > 0 {
		chat = fs.Arg(0)
	}
	usage := func(format string, a ...any) int {
		fmt.Fprintf(env.Stderr, "tgbup export: "+format+"\n", a...)
		return 2
	}
	switch {
	case chat == "":
		return usage("which chat? tgbup export <chat> [--topic T] [--from YYYY-MM] [--to YYYY-MM] [--no-media] -o file.zip")
	case *outPath == "":
		return usage("-o file.zip is required")
	case *from != "" && !monthRE.MatchString(*from), *to != "" && !monthRE.MatchString(*to):
		return usage("--from and --to take YYYY-MM")
	}

	log := newLogger(env.Stderr)
	st, err := loadLocalSetup(env, true)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup export:", err)
		return 1
	}
	db, err := store.Open(filepath.Join(st.cfg.ArchiveDir, "archive.sqlite"))
	if err != nil {
		log.Error("open archive", "err", err)
		return 1
	}
	defer db.Close()

	opts := render.Options{Full: true, From: *from, To: *to, Loc: time.Local}
	opts.ChatIDs, opts.Topics, err = resolveExport(ctx, db, chat, *topic)
	if err == nil {
		err = export(ctx, db, st.cfg.ArchiveDir, opts, !*noMedia, *outPath, env.Stdout)
	}
	var ue usageError
	switch {
	case errors.As(err, &ue):
		return usage("%v", ue)
	case err != nil:
		log.Error("export failed", "err", err.Error())
		return 1
	}
	return 0
}

// resolveExport finds the chat by id, @username or title, and the topic by title or #id.
func resolveExport(ctx context.Context, db *store.Store, chat, topic string) ([]int64, []int, error) {
	chats, err := db.RenderChats(ctx)
	if err != nil {
		return nil, nil, err
	}
	var found []store.RenderChat
	id, isID := strconv.ParseInt(chat, 10, 64)
	for _, c := range chats {
		switch {
		case isID == nil && c.ID == id,
			strings.HasPrefix(chat, "@") && c.Username != "" && strings.EqualFold(c.Username, chat[1:]),
			strings.EqualFold(c.Title, chat):
			found = append(found, c)
		}
	}
	if len(found) == 0 && !strings.HasPrefix(chat, "@") && isID != nil {
		for _, c := range chats {
			if strings.Contains(strings.ToLower(c.Title), strings.ToLower(chat)) {
				found = append(found, c)
			}
		}
	}
	// A forum and the group it was upgraded from usually share a title; the
	// group's history comes with the forum anyway.
	found = slices.DeleteFunc(found, func(c store.RenderChat) bool {
		return slices.ContainsFunc(found, func(o store.RenderChat) bool { return o.MigratedFromID == c.ID })
	})
	switch len(found) {
	case 0:
		return nil, nil, usageErr("no archived chat matches %q (use its title, @username or id)", chat)
	case 1:
	default:
		names := make([]string, len(found))
		for i, c := range found {
			names[i] = fmt.Sprintf("%s (%d)", c.Title, c.ID)
		}
		return nil, nil, usageErr("%q matches several chats, pass the full title or the id: %s", chat, strings.Join(names, ", "))
	}
	c := found[0]
	ids := []int64{c.ID}
	if c.MigratedFromID != 0 && topic == "" {
		ids = append(ids, c.MigratedFromID) // history from before the upgrade
	}
	if topic == "" {
		return ids, nil, nil
	}
	if c.Type != store.ChatForum {
		return nil, nil, usageErr("%q is not a forum, --topic does not apply", c.Title)
	}
	want := strings.TrimPrefix(topic, "#")
	for tid, title := range c.Topics {
		if strings.EqualFold(title, topic) || strconv.Itoa(tid) == want {
			return ids, []int{tid}, nil
		}
	}
	return nil, nil, usageErr("no topic %q in %q", topic, c.Title)
}

func export(ctx context.Context, db *store.Store, archiveDir string, opts render.Options, withMedia bool, outPath string, w io.Writer) error {
	tmp, err := os.MkdirTemp("", "tgbup-export-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	opts.OutDir = tmp
	res, err := render.Render(ctx, db, opts)
	if err != nil {
		return err
	}
	if len(res.Errors) > 0 {
		return errors.Join(res.Errors...)
	}
	if res.Pages == 0 {
		return usageErr("nothing to export: no messages in that chat, topic or period")
	}
	var media []string
	var size int64
	missing := 0
	if withMedia {
		for _, p := range slices.Compact(slices.Sorted(slices.Values(res.Media))) {
			st, err := os.Stat(filepath.Join(archiveDir, p))
			if err != nil {
				missing++
				continue
			}
			media = append(media, p)
			size += st.Size()
		}
	}
	redirect := `<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="0; url=html/index.html"><a href="html/index.html">Open the archive</a>` + "\n"
	if err := os.WriteFile(filepath.Join(tmp, "index.html"), []byte(redirect), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s, %s, ~%s\n", plural(res.Messages, "message"), plural(len(media), "file"), mib(size))
	if missing > 0 {
		fmt.Fprintf(w, "warning: %d files missing on disk, left out\n", missing)
	}
	root := strings.TrimSuffix(filepath.Base(outPath), filepath.Ext(outPath))
	if err := writeZip(ctx, tmp, archiveDir, media, root, outPath); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s\n", outPath)
	return nil
}

func mib(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }

// writeZip packs the rendered pages in dir and the media files straight from
// the archive under a top folder named root. Media are stored, not deflated:
// they are compressed already. A temp file and a rename mean a failed export
// leaves no partial zip.
func writeZip(ctx context.Context, dir, archiveDir string, media []string, root, outPath string) error {
	f, err := os.CreateTemp(filepath.Dir(outPath), ".export-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	zw := zip.NewWriter(f)
	add := func(src, name string, method uint16) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		zf, err := zw.CreateHeader(&zip.FileHeader{Name: root + "/" + name, Method: method, Modified: time.Now()})
		if err != nil {
			return err
		}
		_, err = io.Copy(zf, in)
		return err
	}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		return add(p, filepath.ToSlash(rel), zip.Deflate)
	})
	for _, m := range media {
		if err != nil {
			break
		}
		err = add(filepath.Join(archiveDir, m), filepath.ToSlash(m), zip.Store)
	}
	if err == nil {
		err = zw.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), outPath)
}
