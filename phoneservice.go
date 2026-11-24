package main

import (
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"flashtransfer/phone"
)

// PhoneService is the UI's access to the phone. MTP allows one operation at a
// time, so every phone call holds mu.
type PhoneService struct {
	mu        sync.Mutex
	p         *phone.Phone // nil when not connected
	lastIssue string       // last logged connect problem; polling repeats it every few seconds
	// after Disconnect: don't reconnect on our own until Image Capture has the
	// phone back or it is unplugged, or the user asks
	handingBack bool
}

// IssueView is a phone.Issue shaped for the UI.
type IssueView struct {
	Code                string
	Title               string
	Why                 string
	Fix                 []string
	Details             string
	CanStopImageCapture bool // show the "Stop Image Capture and connect" button
	Step                int  // connection stage this blocks (phone.Step); 0 after connecting
}

type ConnectResult struct {
	Present     bool // a phone in file-transfer mode is on USB
	HandingBack bool // just disconnected; waiting for Image Capture to take the phone back
	Model       string
	Storages    []phone.Storage
	Issue       *IssueView
}

type ListResult struct {
	Entries      []phone.Entry
	Issue        *IssueView
	Disconnected bool
}

// TransferResult reports a copy. Folder/Paths echo what was used so the UI can
// repeat the same copy with overwrite after files were skipped.
type TransferResult struct {
	Canceled     bool // user closed the file picker
	Folder       string
	Paths        []string
	Files        int
	Bytes        int64
	Skipped      []string
	Issue        *IssueView
	Disconnected bool
}

type DeleteResult struct {
	Canceled     bool
	Deleted      int
	Issue        *IssueView
	Disconnected bool
}

// ProgressEvent is emitted as "progress" while a file is copied.
type ProgressEvent struct {
	Name    string
	Sent    int64 // bytes of the current file
	Total   int64
	Index   int   // 1-based number of the file being copied
	OpSent  int64 // bytes copied in the whole operation so far
	OpTotal int64 // bytes in the whole operation; 0 if unknown (phone folders)
}

// Connect opens the phone if one is plugged in. stopImageCapture is true only
// after the user pressed the button that explains and allows it.
func (s *PhoneService) Connect(stopImageCapture bool) ConnectResult {
	if stopImageCapture {
		slog.Info("connect requested", "stopImageCapture", true)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		if !phone.Present() {
			s.handingBack = false
			return ConnectResult{}
		}
		if s.handingBack && !stopImageCapture && !phone.HeldByImageCapture() {
			return ConnectResult{Present: true, HandingBack: true}
		}
		s.handingBack = false
		p, err := phone.Connect(func(*phone.Issue) bool { return stopImageCapture }, connectStatus, false)
		if err != nil {
			iv := issueView(err)
			if iv.Code != s.lastIssue || stopImageCapture {
				slog.Warn("connect failed", "code", iv.Code, "stopImageCapture", stopImageCapture, "err", err)
				s.lastIssue = iv.Code
			}
			return ConnectResult{Present: true, Issue: iv}
		}
		s.p, s.lastIssue = p, ""
		slog.Info("connected", "model", p.Model, "stopImageCapture", stopImageCapture)
	}
	return ConnectResult{Present: true, Model: s.p.Model, Storages: s.p.Storages}
}

// WatchResult is polled while browsing.
type WatchResult struct {
	Connected bool
	Issue     *IssueView // set if releasing the phone went wrong
}

// StillConnected is polled by the UI; it drops the connection once the phone
// is unplugged or leaves file-transfer mode.
func (s *PhoneService) StillConnected() WatchResult {
	if phone.Present() {
		return WatchResult{Connected: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return WatchResult{Issue: s.closeLocked("phone unplugged or left file-transfer mode")}
}

// Disconnect releases the phone and hands it back to Photos and Image Capture.
func (s *PhoneService) Disconnect() *IssueView {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handingBack = true
	return s.closeLocked("user pressed Disconnect")
}

func (s *PhoneService) ServiceShutdown() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return nil
	}
	slog.Info("releasing phone", "reason", "app quitting")
	err := s.p.Close()
	s.p = nil
	return err
}

// Root is the top folder of a storage.
func (s *PhoneService) Root(storageID uint32) phone.Entry {
	return phone.Root(storageID)
}

func (s *PhoneService) List(dir phone.Entry) ListResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return ListResult{Disconnected: true}
	}
	entries, err := s.p.List(dir)
	if err != nil {
		iv, gone := s.failLocked(err)
		return ListResult{Issue: iv, Disconnected: gone}
	}
	return ListResult{Entries: entries}
}

// Download copies phone items into a Mac folder; folder "" asks the user.
func (s *PhoneService) Download(items []phone.Entry, folder string, overwrite bool) TransferResult {
	if folder == "" {
		f, err := application.Get().Dialog.OpenFile().
			SetTitle("Choose where to save").
			SetButtonText("Save Here").
			CanChooseFiles(false).
			CanChooseDirectories(true).
			CanCreateDirectories(true).
			AttachToWindow(application.Get().Window.Current()).
			PromptForSingleSelection()
		if err != nil {
			return TransferResult{Issue: pickerIssue(err)}
		}
		if f == "" {
			return TransferResult{Canceled: true}
		}
		folder = f
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	out := TransferResult{Folder: folder}
	if s.p == nil {
		out.Disconnected = true
		return out
	}
	var res phone.Result
	prog := progress(&res, phoneBytes(items))
	for _, it := range items {
		if err := s.p.Download(it, folder, overwrite, prog, &res); err != nil {
			out.Issue, out.Disconnected = s.failLocked(err)
			break
		}
	}
	out.Files, out.Bytes, out.Skipped = res.Files, res.Bytes, res.Skipped
	slog.Info("copied to Mac", "folder", folder, "files", res.Files, "bytes", res.Bytes, "skipped", len(res.Skipped), "replace", overwrite)
	return out
}

// Upload copies Mac files or folders into a phone folder; no paths asks the user.
func (s *PhoneService) Upload(paths []string, dst phone.Entry, overwrite bool) TransferResult {
	if len(paths) == 0 {
		picked, err := application.Get().Dialog.OpenFile().
			SetTitle("Choose files or folders to copy to the phone").
			SetButtonText("Copy to Phone").
			CanChooseFiles(true).
			CanChooseDirectories(true).
			AttachToWindow(application.Get().Window.Current()).
			PromptForMultipleSelection()
		if err != nil {
			return TransferResult{Issue: pickerIssue(err)}
		}
		if len(picked) == 0 {
			return TransferResult{Canceled: true}
		}
		paths = picked
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	out := TransferResult{Paths: paths}
	if s.p == nil {
		out.Disconnected = true
		return out
	}
	var res phone.Result
	prog := progress(&res, localBytes(paths))
	for _, p := range paths {
		if err := s.p.Upload(p, dst, overwrite, prog, &res); err != nil {
			out.Issue, out.Disconnected = s.failLocked(err)
			break
		}
	}
	out.Files, out.Bytes, out.Skipped = res.Files, res.Bytes, res.Skipped
	slog.Info("copied to phone", "folder", dst.Path, "files", res.Files, "bytes", res.Bytes, "skipped", len(res.Skipped), "replace", overwrite)
	return out
}

// Delete removes phone items after the user confirms in a native dialog.
func (s *PhoneService) Delete(items []phone.Entry) DeleteResult {
	if len(items) == 0 || !confirmDelete(items) {
		return DeleteResult{Canceled: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return DeleteResult{Disconnected: true}
	}
	var out DeleteResult
	for _, it := range items {
		if err := s.p.Delete(it); err != nil {
			out.Issue, out.Disconnected = s.failLocked(err)
			break
		}
		slog.Info("deleted from phone", "path", it.Path)
		out.Deleted++
	}
	return out
}

func confirmDelete(items []phone.Entry) bool {
	what := "\"" + items[0].Name + "\""
	if len(items) > 1 {
		what = strconv.Itoa(len(items)) + " items"
	}
	msg := "This permanently deletes " + what + " from the phone. It cannot be undone."
	for _, it := range items {
		if it.IsDir {
			msg = "This permanently deletes " + what + " from the phone, including everything inside folders. It cannot be undone."
			break
		}
	}

	answer := make(chan bool, 1)
	d := application.Get().Dialog.Warning().
		SetTitle("Delete from phone?").
		SetMessage(msg).
		AttachToWindow(application.Get().Window.Current())
	d.AddButton("Delete").OnClick(func() { answer <- true })
	cancel := d.AddButton("Cancel").OnClick(func() { answer <- false })
	d.SetDefaultButton(cancel)
	d.SetCancelButton(cancel)
	d.Show()
	return <-answer
}

// failLocked explains err and drops the connection when the phone stopped
// answering; the UI then goes back to the connect screen.
func (s *PhoneService) failLocked(err error) (*IssueView, bool) {
	is := phone.Explain(err)
	gone := is.Code == phone.TransferFailed(nil).Code
	slog.Warn("phone operation failed", "code", is.Code, "disconnecting", gone, "err", err)
	if gone {
		if ci := s.closeLocked("phone stopped answering"); ci != nil {
			ci.Details += "\nThis happened after: " + is.Error()
			return ci, true
		}
	}
	return toView(is), gone
}

// closeLocked releases the phone; returns an issue if Image Capture could not be restored.
func (s *PhoneService) closeLocked(reason string) *IssueView {
	if s.p == nil {
		return nil
	}
	slog.Info("releasing phone", "reason", reason)
	err := s.p.Close()
	s.p = nil
	if err != nil {
		slog.Error("release failed", "err", err)
		return toView(phone.Explain(err))
	}
	return nil
}

// ConnectStatusEvent is emitted as "connect-status" while connecting.
type ConnectStatusEvent struct {
	Step int // phone.Step being worked on
	Text string
}

// connectStatus drives the connection flow on the connect screen.
func connectStatus(step phone.Step, msg string) {
	slog.Info(msg)
	application.Get().Event.Emit("connect-status", ConnectStatusEvent{Step: int(step), Text: msg})
}

// progress emits throttled "progress" events for the UI.
func progress(res *phone.Result, opTotal int64) phone.Progress {
	var last time.Time
	return func(name string, sent, total int64) {
		if sent < total && time.Since(last) < 100*time.Millisecond {
			return
		}
		last = time.Now()
		application.Get().Event.Emit("progress", ProgressEvent{Name: name, Sent: sent, Total: total, Index: res.Files + 1,
			OpSent: res.Bytes + sent, OpTotal: opTotal})
	}
}

func issueView(err error) *IssueView {
	if errors.Is(err, phone.ErrStopDeclined) {
		v := toView(phone.ImageCaptureHolding())
		v.CanStopImageCapture = true
		return v
	}
	return toView(phone.Explain(err))
}

func toView(is *phone.Issue) *IssueView {
	v := &IssueView{Code: is.Code, Title: is.Title, Why: is.Why, Fix: is.Fix, Step: int(is.Step)}
	if is.Err != nil {
		v.Details = is.Err.Error()
	}
	return v
}

func pickerIssue(err error) *IssueView {
	return &IssueView{Title: "Could not open the file picker", Why: "macOS did not show the file chooser.",
		Fix: []string{"Try again."}, Details: err.Error()}
}

// phoneBytes totals the selected phone files; 0 (unknown) if a folder is
// selected, since its size needs a full listing.
func phoneBytes(items []phone.Entry) int64 {
	var n int64
	for _, it := range items {
		if it.IsDir {
			return 0
		}
		n += it.Size
	}
	return n
}

// localBytes totals regular files under the given Mac paths; 0 if unreadable,
// in which case the UI falls back to per-file time left.
func localBytes(paths []string) int64 {
	var n int64
	for _, p := range paths {
		err := filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				info, err := d.Info()
				if err != nil {
					return err
				}
				n += info.Size()
			}
			return nil
		})
		if err != nil {
			return 0
		}
	}
	return n
}
