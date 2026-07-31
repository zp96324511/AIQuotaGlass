//go:build !windows

package notify

var sysProcAttrHidden = &procAttrHidden{}

type procAttrHidden struct{}

func (p *procAttrHidden) String() string { return "" }
