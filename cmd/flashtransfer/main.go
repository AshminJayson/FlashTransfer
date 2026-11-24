// flashtransfer: copy files between a Mac and an Android phone over USB.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"flashtransfer/phone"
)

const usage = `usage: flashtransfer [-s N] [-debug] <command>

commands:
  info                                  phone model and storages
  ls [phone-dir]                        list a folder (default /)
  pull [-force] <phone-path>... <mac-dir>   copy from phone to Mac
  push [-force] <mac-path>... <phone-dir>   copy from Mac to phone
  rm [-r] <phone-path>...               delete from phone (-r for folders)

Existing files are skipped unless -force is given.
Problems print a code (e.g. MTP-002); see README "Troubleshooting".
`

func main() {
	storageIdx := flag.Int("s", 0, "storage number shown by info")
	debug := flag.Bool("debug", false, "print raw USB/MTP traffic")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage); flag.PrintDefaults() }
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	err := run(flag.Arg(0), flag.Args()[1:], *storageIdx, *debug)
	var ue usageError
	switch {
	case err == nil:
	case errors.Is(err, phone.ErrStopDeclined):
		fmt.Fprintln(os.Stderr, "Not connected:", err)
		fmt.Fprintln(os.Stderr, "Run again and answer y, or run `killall ptpcamerad` yourself first.")
		os.Exit(1)
	case errors.As(err, &ue):
		fmt.Fprintln(os.Stderr, "error:", err)
		fmt.Fprintln(os.Stderr, "run `flashtransfer -h` for usage")
		os.Exit(2)
	default:
		printIssue(phone.Explain(err))
		os.Exit(1)
	}
}

type usageError string

func (u usageError) Error() string { return string(u) }

func run(cmd string, args []string, storageIdx int, debug bool) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	force := fs.Bool("force", false, "replace existing files")
	recursive := fs.Bool("r", false, "delete folders and their contents")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	args = fs.Args()

	switch cmd {
	case "info", "ls":
	case "pull", "push":
		if len(args) < 2 {
			return usageError(cmd + " needs at least one source and a destination")
		}
	case "rm":
		if len(args) < 1 {
			return usageError("rm needs at least one phone path")
		}
	default:
		return usageError(fmt.Sprintf("unknown command %q", cmd))
	}
	if cmd == "pull" {
		if st, err := os.Stat(args[len(args)-1]); err != nil || !st.IsDir() {
			return usageError(fmt.Sprintf("Mac destination %q must be an existing folder", args[len(args)-1]))
		}
	}
	if cmd == "push" {
		for _, s := range args[:len(args)-1] {
			if _, err := os.Stat(s); err != nil {
				return usageError(fmt.Sprintf("cannot read %q on this Mac: %v", s, err))
			}
		}
	}

	logf("Connecting to phone...")
	p, err := phone.Connect(confirmStop, func(_ phone.Step, msg string) { logf("  %s", msg) }, debug)
	if err != nil {
		return err
	}
	defer func() {
		if err := p.Close(); err != nil {
			printIssue(phone.Explain(err))
		}
	}()
	if storageIdx < 0 || storageIdx >= len(p.Storages) {
		return usageError(fmt.Sprintf("storage %d does not exist (phone has 0-%d, see `flashtransfer info`)", storageIdx, len(p.Storages)-1))
	}
	st := p.Storages[storageIdx]
	logf("Connected: %s, %s", p.Model, st.Name)

	switch cmd {
	case "info":
		fmt.Println(p.Model)
		for i, s := range p.Storages {
			fmt.Printf("  [%d] %-20s %s free of %s\n", i, s.Name, human(s.Free), human(s.Total))
		}
		return nil

	case "ls":
		dir := "/"
		if len(args) > 0 {
			dir = args[0]
		}
		e, err := p.Lookup(st.ID, dir)
		if err != nil {
			return err
		}
		if !e.IsDir {
			return usageError(fmt.Sprintf("%q is a file, not a folder", e.Path))
		}
		items, err := p.List(e)
		if err != nil {
			return err
		}
		for _, it := range items {
			size, name := human(it.Size), it.Name
			if it.IsDir {
				size, name = "-", name+"/"
			}
			fmt.Printf("%10s  %s  %s\n", size, it.ModTime.Format("2006-01-02 15:04"), name)
		}
		return nil

	case "pull":
		var res phone.Result
		for _, src := range args[:len(args)-1] {
			e, err := p.Lookup(st.ID, src)
			if err != nil {
				return err
			}
			if err := p.Download(e, args[len(args)-1], *force, progress, &res); err != nil {
				return err
			}
		}
		return summary(res)

	case "push":
		dst, err := p.Lookup(st.ID, args[len(args)-1])
		if err != nil {
			return err
		}
		if !dst.IsDir {
			return usageError(fmt.Sprintf("phone destination %q is a file, not a folder", dst.Path))
		}
		var res phone.Result
		for _, src := range args[:len(args)-1] {
			if err := p.Upload(src, dst, *force, progress, &res); err != nil {
				return err
			}
		}
		return summary(res)

	default: // rm
		for _, target := range args {
			e, err := p.Lookup(st.ID, target)
			if err != nil {
				return err
			}
			if e.IsDir && !*recursive {
				return usageError(fmt.Sprintf("%q is a folder; use rm -r to delete it and everything in it", e.Path))
			}
			if err := p.Delete(e); err != nil {
				return err
			}
			logf("Deleted %s", e.Path)
		}
		return nil
	}
}

// confirmStop asks the user before stopping macOS Image Capture.
func confirmStop(is *phone.Issue) bool {
	printIssue(is)
	if st, err := os.Stdin.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintln(os.Stderr, "Not asking: input is not a terminal. Run `killall ptpcamerad` and try again.")
		return false
	}
	fmt.Fprint(os.Stderr, "\nStop ptpcamerad now and connect? [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	ok := strings.EqualFold(strings.TrimSpace(line), "y")
	if ok {
		logf("Stopping ptpcamerad...")
	}
	return ok
}

func printIssue(is *phone.Issue) {
	fmt.Fprintf(os.Stderr, "\n[%s] %s\n", is.Code, is.Title)
	fmt.Fprintf(os.Stderr, "  Why: %s\n", is.Why)
	fmt.Fprintln(os.Stderr, "  What to do:")
	for i, f := range is.Fix {
		fmt.Fprintf(os.Stderr, "    %d. %s\n", i+1, f)
	}
	if is.Err != nil {
		fmt.Fprintf(os.Stderr, "  Details: %v\n", is.Err)
	}
	fmt.Fprintf(os.Stderr, "  More help: README.md, Troubleshooting > %s\n", is.Code)
}

func progress(name string, sent, total int64) {
	pct := 100.0
	if total > 0 {
		pct = float64(sent) * 100 / float64(total)
	}
	fmt.Fprintf(os.Stderr, "\r  %-40.40s %3.0f%%  %s / %s   ", name, pct, human(sent), human(total))
	if sent >= total {
		fmt.Fprintln(os.Stderr)
	}
}

func summary(res phone.Result) error {
	logf("Copied %d file(s), %s", res.Files, human(res.Bytes))
	if len(res.Skipped) > 0 {
		is := phone.Skipped(res.Skipped)
		printIssue(is)
		for _, s := range res.Skipped {
			fmt.Fprintf(os.Stderr, "    skipped: %s\n", s)
		}
	}
	return nil
}

func logf(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

// human formats bytes with binary units.
func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
