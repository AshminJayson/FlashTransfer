package phone

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/ganeshrvel/usb"
)

const (
	usbClassStillImage = 6
	mtpCloseSession    = 0x1003
)

// clearStaleSession unblocks a phone that a killed ptpcamerad left mid-operation.
// The phone may still be trying to send ptpcamerad a reply nobody reads, and it
// treats the next command as the end of the old operation, so a fresh OpenSession
// times out; the only other cure is a USB reset, which re-attaches the phone and
// lets ptpcamerad grab it again. Discard pending replies, then send one
// throwaway CloseSession. Returns how many stale bytes were discarded.
func clearStaleSession(uctx *usb.Context) (int, error) {
	h, iface, in, out, err := openMTPInterface(uctx)
	if err != nil {
		return 0, err
	}
	defer h.Close()

	// ptpcamerad may take a moment to let go after being killed
	start := time.Now()
	for {
		if err = h.ClaimInterface(iface); err == nil {
			break
		}
		if time.Since(start) > 3*time.Second {
			return 0, fmt.Errorf("claim MTP interface: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer h.ReleaseInterface(iface)

	drained := drain(h, in)
	req := make([]byte, 12) // container: length, type=command, code, transaction id
	binary.LittleEndian.PutUint32(req[0:], uint32(len(req)))
	binary.LittleEndian.PutUint16(req[4:], 1)
	binary.LittleEndian.PutUint16(req[6:], mtpCloseSession)
	if _, err := h.BulkTransfer(out, req, 1000); err != nil {
		// a timed-out write can leave the endpoint halted; clear it and try once more
		_ = h.ClearHalt(out)
		drained += drain(h, in)
		if _, err := h.BulkTransfer(out, req, 1000); err != nil {
			return drained, fmt.Errorf("send CloseSession: %w", err)
		}
	}
	// any reply means the pipe is free again; its code does not matter
	if _, err := h.BulkTransfer(in, make([]byte, 512), 1000); err != nil {
		return drained, fmt.Errorf("read CloseSession reply: %w", err)
	}
	return drained + drain(h, in), nil
}

// drain reads and discards whatever the phone has queued on its bulk IN endpoint.
func drain(h *usb.DeviceHandle, in byte) int {
	total := 0
	buf := make([]byte, 16*1024)
	for i := 0; i < 64; i++ {
		n, err := h.BulkTransfer(in, buf, 200)
		total += n
		if err != nil || n == 0 {
			break
		}
	}
	return total
}

// openMTPInterface opens the first device with an MTP (still image) interface
// and returns its interface number and bulk endpoints.
func openMTPInterface(uctx *usb.Context) (h *usb.DeviceHandle, iface, in, out byte, err error) {
	list, err := uctx.GetDeviceList()
	if err != nil {
		return nil, 0, 0, 0, err
	}
	if len(list) > 0 {
		defer list.Done()
	}
	for _, d := range list {
		cd, err := d.GetActiveConfigDescriptor()
		if err != nil {
			continue
		}
		for _, it := range cd.Interfaces {
			for _, a := range it.AltSetting {
				if a.InterfaceClass != usbClassStillImage {
					continue
				}
				in, out = 0, 0
				for _, e := range a.EndPoints {
					if e.TransferType() != usb.TRANSFER_TYPE_BULK {
						continue
					}
					if e.Direction() == usb.ENDPOINT_IN {
						in = e.EndpointAddress
					} else {
						out = e.EndpointAddress
					}
				}
				if in == 0 || out == 0 {
					continue
				}
				// the handle keeps its own reference, so freeing the list is safe
				h, err := d.Open()
				if err != nil {
					return nil, 0, 0, 0, err
				}
				return h, a.InterfaceNumber, in, out, nil
			}
		}
	}
	return nil, 0, 0, 0, errors.New("no MTP interface found")
}
