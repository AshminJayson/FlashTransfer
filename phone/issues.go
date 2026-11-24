package phone

import (
	"errors"
	"fmt"

	"github.com/ganeshrvel/go-mtpfs/mtp"
)

// Issue is a user-facing problem: a stable code (README troubleshooting anchor),
// a plain-language explanation, and steps the user can take.
type Issue struct {
	Code  string   // "MTP-001"...; never renumber, users search for these
	Title string   // one line, what went wrong
	Why   string   // why it happens, for a non-technical reader
	Fix   []string // ordered steps the user can take
	Err   error    // underlying cause, may be nil
	Step  Step     // connection stage this blocks; 0 for problems after connecting
}

func (i *Issue) Error() string {
	if i.Err == nil {
		return i.Code + ": " + i.Title
	}
	return fmt.Sprintf("%s: %s: %v", i.Code, i.Title, i.Err)
}

func (i *Issue) Unwrap() error { return i.Err }

func NoPhone(err error) *Issue {
	return &Issue{
		Code:  "MTP-001",
		Step:  StepPlugged,
		Title: "No phone found",
		Why:   "No Android phone in File transfer mode is connected over USB.",
		Fix: []string{
			"Plug the phone in with a data cable (some cables only charge).",
			"Unlock the phone, pull down the notification shade, tap the USB notification and choose \"File transfer\". If you don't see these options, unplug the cable and plug it back in.",
			"Try another USB port or cable.",
		},
		Err: err,
	}
}

func ImageCaptureHolding() *Issue {
	return &Issue{
		Code:  "MTP-002",
		Step:  StepAvailable,
		Title: "macOS Image Capture is using the phone",
		Why: "When a phone is plugged in, macOS starts a background service (ptpcamerad) so Photos and Image Capture can import pictures. " +
			"It locks the phone's file-transfer connection, so no other app can use it.",
		Fix: []string{
			"Allow this app to stop ptpcamerad. It is restarted as soon as this app lets go of the phone, so Photos and Image Capture keep working.",
			"Or stop it yourself in Terminal: killall ptpcamerad",
		},
	}
}

func OtherAppHolding(app string) *Issue {
	return &Issue{
		Code:  "MTP-003",
		Step:  StepAvailable,
		Title: fmt.Sprintf("Another app (%s) is using the phone", app),
		Why:   "Only one app at a time can use the phone's file-transfer connection.",
		Fix: []string{
			fmt.Sprintf("Quit %s (or any other phone file-transfer app), then try again.", app),
		},
	}
}

func SessionFailed(err error) *Issue {
	return &Issue{
		Code:  "MTP-004",
		Step:  StepSession,
		Title: "Could not open a connection to the phone",
		Why:   "The phone did not answer. Usually it was just reconnected, is locked, or another app left it in a bad state.",
		Fix: []string{
			"Unlock the phone and keep the screen on.",
			"Unplug the phone, wait 5 seconds, plug it back in and choose \"File transfer\" again.",
			"Restart the phone if it keeps happening.",
		},
		Err: err,
	}
}

func Locked(err error) *Issue {
	return &Issue{
		Code:  "MTP-005",
		Step:  StepFiles,
		Title: "Phone storage is not visible",
		Why:   "Android hides its files until the phone is unlocked and you allow access.",
		Fix: []string{
			"Unlock the phone.",
			"If the phone asks \"Allow access to phone data?\", tap Allow.",
		},
		Err: err,
	}
}

func NotFound(phonePath string) *Issue {
	return &Issue{
		Code:  "MTP-006",
		Title: fmt.Sprintf("%q does not exist on the phone", phonePath),
		Why:   "Names on the phone are case-sensitive and must match exactly.",
		Fix:   []string{"List the parent folder to check the exact name."},
	}
}

func Skipped(paths []string) *Issue {
	return &Issue{
		Code:  "MTP-007",
		Title: fmt.Sprintf("%d file(s) skipped because they already exist", len(paths)),
		Why:   "Existing files are never replaced unless you ask, so nothing is lost by accident.",
		Fix:   []string{"Copy again with the replace option (-force) to overwrite them."},
	}
}

func StorageFull(err error) *Issue {
	return &Issue{
		Code:  "MTP-008",
		Title: "Phone storage is full",
		Why:   "There is not enough free space on the phone for this file.",
		Fix:   []string{"Free up space on the phone, then copy again."},
		Err:   err,
	}
}

func TransferFailed(err error) *Issue {
	return &Issue{
		Code:  "MTP-009",
		Title: "The transfer stopped",
		Why:   "The phone stopped responding mid-transfer. The cable may have moved, the phone may have switched USB mode, or it may have locked.",
		Fix: []string{
			"Unlock the phone.",
			"Check the cable is firmly connected, then copy again. Finished files are kept; the interrupted one is not left half-written.",
		},
		Err: err,
	}
}

// Explain turns any error from this package into an Issue.
func Explain(err error) *Issue {
	var is *Issue
	if errors.As(err, &is) {
		return is
	}
	var rc mtp.RCError
	if errors.As(err, &rc) && (rc == mtp.RC_StoreFull) {
		return StorageFull(err)
	}
	return TransferFailed(err)
}

func ImageCaptureNotRestored(err error) *Issue {
	return &Issue{
		Code:  "MTP-010",
		Step:  StepAvailable,
		Title: "macOS Image Capture could not be restarted",
		Why:   "This app stopped Image Capture to reach the phone, and restarting it failed. Photos and Image Capture may not see phones until it runs again.",
		Fix: []string{
			"Unplug the phone and plug it back in.",
			"If Photos still cannot see the phone, run in Terminal: launchctl kickstart gui/$(id -u)/com.apple.ptpcamerad",
			"Or restart the Mac.",
		},
		Err: err,
	}
}
