# FlashTransfer

Copy files between a Mac and an Android phone over USB.

## Installation

You need a Mac with macOS 13 or later (Apple Silicon or Intel), an Android phone, and a USB
cable that carries data (some cables only charge).

1. Download **FlashTransfer.dmg** from the
   [latest release](https://github.com/AshminJayson/FlashTransfer/releases/latest).
2. Open the downloaded file and drag **FlashTransfer** into **Applications**.
3. Open **FlashTransfer** from Applications.
4. The first time, macOS says it can't verify the app, because it is not yet signed with an
   Apple developer certificate. Click **Done**, open **System Settings > Privacy & Security**,
   scroll down and click **Open Anyway** next to FlashTransfer, then confirm. This is only
   needed once.

### Connect your phone

1. Plug the phone into the Mac and unlock it.
2. On the phone, open the **USB** notification and choose **File transfer**. If you don't see
   these options, unplug the cable and plug it back in.
3. If FlashTransfer says **macOS Image Capture is using the phone**, click
   **Stop Image Capture and Connect**. FlashTransfer gives the phone back to Photos and Image
   Capture when you disconnect.
4. If the phone asks **Allow access to phone data?**, tap **Allow**.

Copy with **Copy to Mac** and **Copy to Phone**, or drag files from Finder onto the window.
Click **Disconnect** before unplugging. If something goes wrong, the screen says what and how
to fix it; the codes are explained under [Troubleshooting](#troubleshooting).

## Building from source

Requires macOS, Go 1.26+, Node.js, `brew install libusb pkgconf`, and the Wails CLI:
`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0`.

```
wails3 task dmg                               # release: universal bin/FlashTransfer.dmg
wails3 task package                           # desktop app: bin/FlashTransfer.app
wails3 dev                                    # desktop app with live reload
go build -o flashtransfer ./cmd/flashtransfer  # command-line tool
```

The app bundles its own universal libusb (built by `build/darwin/build-libusb.sh` on first build,
linked dynamically, LGPL-2.1), so users need neither Homebrew nor libusb. Minimum macOS is 13.
Third-party licenses ship inside the app (`Contents/Resources/licenses`) and are listed in
`THIRD_PARTY_NOTICES.md`.

The desktop app has the same features as the command line below: browse, copy both ways
(buttons or drag and drop from Finder), delete, and the same troubleshooting codes.

## Website

`docs/` is the user-facing site. To publish it: repository Settings > Pages > Deploy from a branch,
branch `main`, folder `/docs`. Its download and help links point to this repository automatically.

## Command-line tool

Plug the phone in, unlock it, and choose **File transfer** from the USB notification.

```
flashtransfer info                              # phone model and storages
flashtransfer ls /DCIM/Camera                   # list a folder
flashtransfer pull /DCIM/Camera ~/Desktop       # copy from phone to Mac
flashtransfer push ~/Documents/a.pdf /Download  # copy from Mac to phone
flashtransfer rm /Download/a.pdf                # delete a file
flashtransfer rm -r /Download/old-folder        # delete a folder and its contents
flashtransfer -s 1 ls /                         # use the second storage (SD card)
```

Existing files are never replaced unless you add `-force` (`pull -force`, `push -force`).
A replaced file is only removed after its new copy has fully arrived.

## Troubleshooting

Every problem prints a code. Find it below.

### MTP-001

**No phone found.** The Mac sees no phone in File transfer mode.

1. Use a data cable; some cables only charge.
2. Unlock the phone, open the USB notification, choose **File transfer**. If you don't see
   these options, unplug the cable and plug it back in.
3. Try another USB port or cable.

### MTP-002

**macOS Image Capture is using the phone.** When a phone is plugged in, macOS starts a
background service, `ptpcamerad`, so Photos and Image Capture can import pictures. It locks
the phone's file-transfer connection, so no other app can use it.

flashtransfer explains this and asks before doing anything. If you answer `y`, it stops `ptpcamerad`
while connected and restarts it as soon as it lets go of the phone (disconnect, quit, unplug,
or when a command finishes). Nothing is uninstalled or disabled.
To do it yourself instead: `killall ptpcamerad`, then run flashtransfer again.

### MTP-003

**Another app is using the phone.** Only one app can use the phone's file-transfer connection
at a time. Quit the app named in the message (for example OpenMTP or Android File Transfer).

### MTP-004

**Could not open a connection to the phone.** The phone did not answer.

1. Unlock the phone and keep the screen on.
2. Unplug, wait 5 seconds, plug back in, choose **File transfer** again.
3. Restart the phone if it keeps happening.

### MTP-005

**Phone storage is not visible.** Android hides files until the phone is unlocked and access
is allowed. Unlock the phone and tap **Allow** if it asks "Allow access to phone data?".

### MTP-006

**File or folder not found on the phone.** Names are case-sensitive. List the parent folder
with `flashtransfer ls` to check the exact name.

### MTP-007

**Files skipped because they already exist.** Nothing was overwritten. Run the same copy with
`-force` to replace them.

### MTP-008

**Phone storage is full.** Free up space on the phone and copy again.

### MTP-009

**The transfer stopped.** The phone stopped responding, for example because the cable moved, the
USB mode changed, or the phone locked. Unlock the phone, check the cable, and copy again.
Finished files are kept; the interrupted file is not left half-written.

### MTP-010

**macOS Image Capture could not be restarted.** The app stopped Image Capture to reach the
phone and could not start it again, so Photos and Image Capture may not see phones. Unplug
the phone and plug it back in. If Photos still cannot see it, run
`launchctl kickstart gui/$(id -u)/com.apple.ptpcamerad` in Terminal, or restart the Mac.

### More detail

Add `-debug` before the command (`flashtransfer -debug info`) to print raw USB/MTP traffic.
