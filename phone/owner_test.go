package phone

import "testing"

func TestParseInterfaceOwner(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"image capture holds mtp", `+-o IOUSBHostInterface@0
  |   "bInterfaceClass" = 6
  |   "UsbExclusiveOwner" = "pid 50747, ptpcamerad"
+-o IOUSBHostInterface@1
  |   "bInterfaceClass" = 2
  |   "UsbExclusiveOwner" = "AppleUSBACMControl"`, "ptpcamerad"},
		{"truncated app name", `+-o IOUSBHostInterface@0
  |   "bInterfaceClass" = 6
  |   "UsbExclusiveOwner" = "pid 50701, OpenMTP Helper ("`, "OpenMTP Helper"},
		{"mtp free, other interface held", `+-o IOUSBHostInterface@0
  |   "bInterfaceClass" = 6
+-o IOUSBHostInterface@1
  |   "bInterfaceClass" = 2
  |   "UsbExclusiveOwner" = "pid 1, something"`, ""},
		{"class 60 is not mtp", `+-o X
  |   "bInterfaceClass" = 60
  |   "UsbExclusiveOwner" = "pid 1, something"`, ""},
	}
	for _, c := range cases {
		if got := parseInterfaceOwner(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
