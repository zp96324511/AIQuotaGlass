//go:build windows

package notify

import (
	"syscall"
)

var sysProcAttrHidden = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
