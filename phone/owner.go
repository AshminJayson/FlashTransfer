package phone

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const imageCaptureProcess = "ptpcamerad"

var (
	mtpInterfaceRe = regexp.MustCompile(`"bInterfaceClass" = 6\b`)
	ownerRe        = regexp.MustCompile(`"UsbExclusiveOwner" = "pid \d+, ([^"]*)"`)
)

// interfaceOwner returns the process name holding a phone's MTP USB interface,
// or "" if none is held or the platform is not macOS.
func interfaceOwner() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("ioreg", "-r", "-c", "IOUSBHostInterface", "-l", "-w0").Output()
	if err != nil {
		// detection is advisory; connecting still reports the real failure
		return ""
	}
	return parseInterfaceOwner(string(out))
}

// parseInterfaceOwner finds the exclusive owner of the first still-image/MTP
// interface (class 6) in `ioreg -r -c IOUSBHostInterface -l` output.
func parseInterfaceOwner(ioreg string) string {
	for _, block := range strings.Split(ioreg, "+-o ") {
		if !mtpInterfaceRe.MatchString(block) {
			continue
		}
		if m := ownerRe.FindStringSubmatch(block); m != nil {
			// ioreg truncates names, e.g. "OpenMTP Helper ("
			return strings.TrimRight(m[1], " (")
		}
	}
	return ""
}

// HeldByImageCapture reports whether macOS Image Capture currently has the phone.
func HeldByImageCapture() bool {
	return interfaceOwner() == imageCaptureProcess
}

// holdOffImageCapture keeps ptpcamerad stopped until stop is called; launchd
// respawns it within seconds and it re-grabs the phone after a USB reset.
func holdOffImageCapture() (stop func()) {
	// opening while ptpcamerad still holds the interface fails; wait for the release
	for i := 0; i < 15 && interfaceOwner() == imageCaptureProcess; i++ {
		killImageCapture()
		time.Sleep(200 * time.Millisecond)
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			killImageCapture()
			select {
			case <-done:
				return
			case <-t.C:
			}
		}
	}()
	return func() { close(done) }
}

func killImageCapture() {
	// non-zero exit just means it is not running right now
	_ = exec.Command("killall", "-9", imageCaptureProcess).Run()
}

// Present reports whether a phone in file-transfer mode is on USB, without
// opening it; cheap enough to poll. Always true off macOS.
func Present() bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	out, err := exec.Command("ioreg", "-r", "-c", "IOUSBHostInterface", "-l", "-w0").Output()
	if err != nil {
		// can't tell; let Connect try and report the real problem
		return true
	}
	return mtpInterfaceRe.Match(out)
}

// restoreImageCapture restarts ptpcamerad so it picks the phone up again for
// Photos and Image Capture. It does not wait: right after ptpcamerad was killed,
// launchd's restart throttle makes kickstart block for up to ~10 s, and launchd
// usually brings it back on its own anyway. A failed start is logged.
func restoreImageCapture() error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	target := fmt.Sprintf("gui/%d/com.apple.%s", os.Getuid(), imageCaptureProcess)
	var out bytes.Buffer
	cmd := exec.Command("launchctl", "kickstart", target)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launchctl kickstart %s: %w", target, err)
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("restarting Image Capture failed (MTP-010): launchctl kickstart %s: %v: %s",
				target, err, strings.TrimSpace(out.String()))
		}
	}()
	return nil
}
