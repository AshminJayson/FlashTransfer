// Package phone is a small MTP client for Android phones over USB, built on
// the BSD-licensed go-mtpfs protocol layer.
package phone

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ganeshrvel/go-mtpfs/mtp"
	"github.com/ganeshrvel/usb"
)

// ErrStopDeclined: user chose not to stop Image Capture, already shown the explanation.
var ErrStopDeclined = errors.New("Image Capture is still using the phone; nothing was changed")

// unlockWait is how long Connect waits for the user to unlock the phone.
const unlockWait = 2 * time.Minute

// partSuffix marks an incomplete copy; renamed to the real name only on success.
const partSuffix = ".flashtransfer-part"

type Storage struct {
	ID          uint32
	Name        string
	Free, Total int64
}

// Entry is a file or folder on the phone.
type Entry struct {
	StorageID uint32
	Handle    uint32
	Name      string
	Path      string // slash-separated, from storage root
	IsDir     bool
	Size      int64
	ModTime   time.Time
}

// Step is a stage of connecting, shown to the user as a flow.
type Step int

const (
	StepPlugged   Step = iota + 1 // on USB in file-transfer mode
	StepAvailable                 // not held by Image Capture or another app
	StepSession                   // MTP session opened
	StepFiles                     // unlocked and sharing its storage
)

// Status reports the stage being worked on and what is happening, for the user.
type Status func(step Step, msg string)

// Progress reports bytes sent so far for the file currently being copied.
type Progress func(name string, sent, total int64)

// Result accumulates the outcome of a copy across files and folders.
type Result struct {
	Files   int
	Bytes   int64
	Skipped []string // destination paths left untouched because they already exist
}

type Phone struct {
	usb                 *usb.Context
	dev                 *mtp.Device
	stoppedImageCapture bool   // restart ptpcamerad on Close
	Model               string // e.g. "samsung SM-S911B"
	Storages            []Storage
}

// Connect opens the first MTP phone on USB.
//
// If macOS Image Capture (ptpcamerad) holds the phone, confirmStop is called
// with the explanation; ptpcamerad is stopped only if it returns true.
//
// status receives the stage and a short message for the user while connecting.
func Connect(confirmStop func(*Issue) bool, status Status, debug bool) (*Phone, error) {
	holdOff := false
	switch owner := interfaceOwner(); {
	case owner == imageCaptureProcess:
		if !confirmStop(ImageCaptureHolding()) {
			return nil, ErrStopDeclined
		}
		holdOff = true
	case owner != "":
		return nil, OtherAppHolding(owner)
	}

	p, err := open(holdOff, debug, status)
	// ptpcamerad can also grab the phone after the USB reset inside open
	if err != nil && !holdOff && interfaceOwner() == imageCaptureProcess {
		if !confirmStop(ImageCaptureHolding()) {
			return nil, ErrStopDeclined
		}
		p, err = open(true, debug, status)
	}
	return p, err
}

func open(holdOff, debug bool, status Status) (p *Phone, err error) {
	if holdOff {
		// connection failed: give Photos/Image Capture the phone back right away
		defer func() {
			if err == nil {
				return
			}
			if rerr := restoreImageCapture(); rerr != nil {
				err = ImageCaptureNotRestored(errors.Join(rerr, err))
			}
		}()
		status(StepAvailable, "Stopping Image Capture...")
		stop := holdOffImageCapture()
		defer stop()
	}

	// A stopped ptpcamerad leaves its session open on the phone, so the first
	// OpenSession can fail. The USB reset that clears it makes macOS re-attach
	// the phone as a new device, which invalidates the handle. Find the phone
	// again and retry rather than asking the user to replug.
	for attempt := 1; ; attempt++ {
		var retry bool
		p, retry, err = openOnce(holdOff, debug, status)
		if err == nil || !retry || attempt == 3 {
			return p, err
		}
		status(StepSession, "Resetting the phone's USB connection...")
		waitForPhone(8 * time.Second)
	}
}

// openOnce makes one connection attempt; retry reports whether a fresh attempt
// after the phone re-attaches can help.
func openOnce(holdOff, debug bool, status Status) (p *Phone, retry bool, err error) {
	status(StepSession, "Opening the phone...")
	// own the libusb context: mtp.SelectDevice creates one per call and never frees it
	uctx := usb.NewContext()
	if holdOff {
		drained, err := clearStaleSession(uctx)
		if err != nil {
			// not fatal: Configure still recovers with a USB reset, just slower
			log.Printf("clearing Image Capture's unfinished session failed (discarded %d bytes): %v", drained, err)
		} else {
			log.Printf("cleared Image Capture's unfinished session (discarded %d bytes)", drained)
		}
	}
	dev, err := firstDevice(uctx, debug)
	if err != nil {
		uctx.Exit()
		return nil, false, NoPhone(err)
	}
	p = &Phone{usb: uctx, dev: dev, stoppedImageCapture: holdOff}

	// ms; a stale session makes OpenSession hang, so fail fast and let Configure reset
	dev.Timeout = 3000
	if err := dev.Configure(); err != nil {
		p.release()
		return nil, true, SessionFailed(err)
	}
	dev.Timeout = 15000 // ms; listing large folders is slow

	status(StepSession, "Reading phone details...")
	var di mtp.DeviceInfo
	if err := dev.GetDeviceInfo(&di); err != nil {
		p.release()
		return nil, true, SessionFailed(err)
	}
	p.Model = strings.TrimSpace(di.Manufacturer + " " + di.Model)

	// A locked phone shares no storage until it is unlocked and access is allowed,
	// and one just reset needs a moment. Wait for the user rather than failing.
	waiting := false
	for start := time.Now(); ; time.Sleep(time.Second) {
		if p.Storages, err = p.readStorages(); err != nil || len(p.Storages) > 0 {
			break
		}
		if !Present() {
			p.release()
			return nil, false, NoPhone(errors.New("phone unplugged or left file-transfer mode while waiting for it to unlock"))
		}
		if time.Since(start) > unlockWait {
			p.release()
			return nil, false, Locked(fmt.Errorf("phone shared no storage within %v", unlockWait))
		}
		// first seconds are normal after a reset; only then ask the user
		if !waiting && time.Since(start) > 2*time.Second {
			waiting = true
			status(StepFiles, "Waiting for the phone to share its files: unlock it, set its USB mode to File transfer (not Charging only), and tap Allow if asked.")
		}
	}
	if err != nil {
		p.release()
		return nil, true, SessionFailed(err)
	}
	return p, false, nil
}

// waitForPhone waits for the phone to re-attach after a USB reset.
func waitForPhone(timeout time.Duration) {
	time.Sleep(time.Second) // let the old device detach first
	for end := time.Now().Add(timeout); time.Now().Before(end) && !Present(); {
		time.Sleep(250 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond) // interfaces finish attaching after the device appears
}

// firstDevice opens the first MTP-capable USB device and releases the others.
func firstDevice(uctx *usb.Context, debug bool) (*mtp.Device, error) {
	cands, err := mtp.FindDevices(uctx)
	if err != nil {
		return nil, err
	}
	var found *mtp.Device
	for _, c := range cands {
		c.MTPDebug, c.DataDebug, c.USBDebug = debug, debug, debug
		if found == nil && c.Open() == nil {
			found = c
			continue
		}
		c.Done()
	}
	if found == nil {
		return nil, errors.New("no MTP devices found")
	}
	return found, nil
}

func (p *Phone) readStorages() ([]Storage, error) {
	var ids mtp.Uint32Array
	if err := p.dev.GetStorageIDs(&ids); err != nil {
		return nil, err
	}
	out := make([]Storage, 0, len(ids.Values))
	for _, id := range ids.Values {
		var si mtp.StorageInfo
		if err := p.dev.GetStorageInfo(id, &si); err != nil {
			return nil, err
		}
		out = append(out, Storage{ID: id, Name: si.StorageDescription,
			Free: int64(si.FreeSpaceInBytes), Total: int64(si.MaxCapability)})
	}
	return out, nil
}

// Close ends the session, frees USB, and restarts Image Capture if Connect
// stopped it, so Photos and Image Capture can use the phone again.
func (p *Phone) Close() error {
	p.release()
	if p.stoppedImageCapture {
		if err := restoreImageCapture(); err != nil {
			return ImageCaptureNotRestored(err)
		}
	}
	return nil
}

func (p *Phone) release() {
	p.dev.Close()
	p.dev.Done()
	p.usb.Exit()
}

// Root is the top folder of a storage.
func Root(storageID uint32) Entry {
	return Entry{StorageID: storageID, Handle: mtp.GOH_ROOT_PARENT, Name: "/", Path: "/", IsDir: true}
}

// List returns a folder's contents, folders first, then by name.
func (p *Phone) List(dir Entry) ([]Entry, error) {
	var handles mtp.Uint32Array
	if err := p.dev.GetObjectHandles(dir.StorageID, mtp.GOH_ALL_FORMATS, dir.Handle, &handles); err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(handles.Values))
	for _, h := range handles.Values {
		e, err := p.entry(h, dir.Path)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (p *Phone) entry(handle uint32, parentPath string) (Entry, error) {
	var oi mtp.ObjectInfo
	if err := p.dev.GetObjectInfo(handle, &oi); err != nil {
		return Entry{}, err
	}
	size := int64(oi.CompressedSize)
	if oi.CompressedSize == 0xFFFFFFFF {
		// files >= 4 GiB: real size only via the 64-bit ObjectSize property
		var v mtp.Uint64Value
		if err := p.dev.GetObjectPropValue(handle, mtp.OPC_ObjectSize, &v); err != nil {
			return Entry{}, err
		}
		size = int64(v.Value)
	}
	return Entry{
		StorageID: oi.StorageID,
		Handle:    handle,
		Name:      oi.Filename,
		Path:      path.Join(parentPath, oi.Filename),
		IsDir:     oi.ObjectFormat == mtp.OFC_Association,
		Size:      size,
		ModTime:   oi.ModificationDate,
	}, nil
}

// Lookup resolves a slash path such as "/DCIM/Camera" on a storage.
func (p *Phone) Lookup(storageID uint32, phonePath string) (Entry, error) {
	cur := Root(storageID)
	clean := path.Clean("/" + phonePath)
	for _, name := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		if name == "" {
			continue
		}
		// shortcut: lists every sibling per path segment; slow in folders with
		// thousands of files, cache listings if the UI needs it
		children, err := p.List(cur)
		if err != nil {
			return Entry{}, err
		}
		found := false
		for _, c := range children {
			if c.Name == name {
				cur, found = c, true
				break
			}
		}
		if !found {
			return Entry{}, NotFound(clean)
		}
	}
	return cur, nil
}

// Download copies a phone file or folder into localDir. Existing local files are
// skipped unless overwrite is set.
func (p *Phone) Download(src Entry, localDir string, overwrite bool, prog Progress, res *Result) error {
	dst := filepath.Join(localDir, src.Name)
	if src.IsDir {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		children, err := p.List(src)
		if err != nil {
			return err
		}
		for _, c := range children {
			if err := p.Download(c, dst, overwrite, prog, res); err != nil {
				return err
			}
		}
		return nil
	}

	if _, err := os.Lstat(dst); err == nil && !overwrite {
		res.Skipped = append(res.Skipped, dst)
		return nil
	}

	tmp := dst + partSuffix
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	err = p.dev.GetObject(src.Handle, f, func(sent int64) error {
		prog(src.Name, sent, src.Size)
		return nil
	})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chtimes(tmp, src.ModTime, src.ModTime)
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		if rerr := os.Remove(tmp); rerr != nil && !os.IsNotExist(rerr) {
			err = errors.Join(err, rerr)
		}
		return err
	}
	res.Files++
	res.Bytes += src.Size
	return nil
}

// Upload copies a local file or folder into the phone folder dst. Existing phone
// files are skipped unless overwrite is set; existing folders are merged into.
func (p *Phone) Upload(localPath string, dst Entry, overwrite bool, prog Progress, res *Result) error {
	st, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	name := filepath.Base(localPath)
	target := path.Join(dst.Path, name)

	children, err := p.List(dst)
	if err != nil {
		return err
	}
	var existing *Entry
	for i := range children {
		if children[i].Name == name {
			existing = &children[i]
			break
		}
	}

	if st.IsDir() {
		var dir Entry
		switch {
		case existing == nil:
			if dir, err = p.mkdir(dst, name); err != nil {
				return err
			}
		case existing.IsDir:
			dir = *existing
		default:
			// a file already has this folder's name; never replace a file with a folder
			res.Skipped = append(res.Skipped, target)
			return nil
		}
		items, err := os.ReadDir(localPath)
		if err != nil {
			return err
		}
		for _, it := range items {
			if err := p.Upload(filepath.Join(localPath, it.Name()), dir, overwrite, prog, res); err != nil {
				return err
			}
		}
		return nil
	}

	if existing != nil && (!overwrite || existing.IsDir) {
		res.Skipped = append(res.Skipped, target)
		return nil
	}

	// replacing: upload under a temporary name first so a failed copy never loses the old file
	sendName := name
	if existing != nil {
		sendName = name + partSuffix
	}
	handle, err := p.sendFile(localPath, st, dst, sendName, prog)
	if err != nil {
		return err
	}
	if existing != nil {
		if err := p.dev.DeleteObject(existing.Handle); err != nil {
			return fmt.Errorf("new copy saved as %q but old file could not be removed: %w", sendName, err)
		}
		if err := p.dev.SetObjectPropValue(handle, mtp.OPC_ObjectFileName, &mtp.StringValue{Value: name}); err != nil {
			return fmt.Errorf("new copy saved as %q but could not be renamed: %w", sendName, err)
		}
	}
	res.Files++
	res.Bytes += st.Size()
	return nil
}

func (p *Phone) sendFile(localPath string, st os.FileInfo, dst Entry, name string, prog Progress) (uint32, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	size := st.Size()
	info := mtp.ObjectInfo{
		StorageID:        dst.StorageID,
		ObjectFormat:     mtp.OFC_Undefined,
		ParentObject:     dst.Handle,
		Filename:         name,
		CompressedSize:   uint32(min(size, 0xFFFFFFFF)), // 0xFFFFFFFF means ">= 4 GiB"
		ModificationDate: st.ModTime(),
	}
	_, _, handle, err := p.dev.SendObjectInfo(dst.StorageID, dst.Handle, &info)
	if err != nil {
		return 0, err
	}
	err = p.dev.SendObject(f, size, func(sent int64) error {
		prog(filepath.Base(localPath), sent, size)
		return nil
	})
	if err != nil {
		// don't leave a truncated file on the phone
		if derr := p.dev.DeleteObject(handle); derr != nil {
			err = errors.Join(err, fmt.Errorf("partial file %q left on phone: %w", name, derr))
		}
		return 0, err
	}
	return handle, nil
}

func (p *Phone) mkdir(parent Entry, name string) (Entry, error) {
	info := mtp.ObjectInfo{
		StorageID:       parent.StorageID,
		ObjectFormat:    mtp.OFC_Association,
		AssociationType: mtp.AT_GenericFolder,
		ParentObject:    parent.Handle,
		Filename:        name,
	}
	_, _, h, err := p.dev.SendObjectInfo(parent.StorageID, parent.Handle, &info)
	if err != nil {
		return Entry{}, err
	}
	return Entry{StorageID: parent.StorageID, Handle: h, Name: name,
		Path: path.Join(parent.Path, name), IsDir: true}, nil
}

// Delete removes a phone file, or a folder and everything in it.
func (p *Phone) Delete(e Entry) error {
	if e.Handle == mtp.GOH_ROOT_PARENT {
		return errors.New("refusing to delete the storage root")
	}
	return p.dev.DeleteObject(e.Handle)
}
