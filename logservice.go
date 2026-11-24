package main

import (
	"log"
	"os"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const maxLogLines = 500

// LogService keeps recent log lines for the footer log. Everything written with
// the standard log package (ours, slog's default handler, and go-mtpfs) lands
// here and on stderr.
type LogService struct {
	mu    sync.Mutex
	lines []string
}

// Lines returns the kept log lines, oldest first.
func (l *LogService) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// UI records something from the window (an error or a user action) in the log.
func (l *LogService) UI(msg string) {
	log.Printf("ui: %s", msg)
}

// writer returns the io.Writer to install with log.SetOutput.
func (l *LogService) writer() logWriter { return logWriter{l} }

// logWriter is separate so Write is not exposed to the UI as a bound method.
type logWriter struct{ l *LogService }

func (w logWriter) Write(p []byte) (int, error) {
	if _, err := os.Stderr.Write(p); err != nil {
		return 0, err
	}
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		w.l.mu.Lock()
		w.l.lines = append(w.l.lines, line)
		if len(w.l.lines) > maxLogLines {
			w.l.lines = w.l.lines[len(w.l.lines)-maxLogLines:]
		}
		w.l.mu.Unlock()
		// before app.Run there is no app to emit to; Lines() covers those
		if app := application.Get(); app != nil {
			app.Event.Emit("log", line)
		}
	}
	return len(p), nil
}
