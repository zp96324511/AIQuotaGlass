package notify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// windowsNotifier raises a Windows toast via a cached PowerShell script.
type windowsNotifier struct{}

func newWindowsNotifier() Notifier {
	if os.PathSeparator == '\\' {
		return &windowsNotifier{}
	}
	return &noopNotifier{}
}

type noopNotifier struct{}

func (n *noopNotifier) Show(_ context.Context, _, _ string) error { return nil }

var script = `$ErrorActionPreference = 'Stop'
$title = $args[0]
$message = $args[1]
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$xml = "<toast><visual><binding template='ToastGeneric'><text>$title</text><text>$message</text></binding></visual></toast>"
$doc = New-Object Windows.Data.Xml.Dom.XmlDocument
$doc.LoadXml($xml)
$toast = [Windows.UI.Notifications.ToastNotification]::new($doc)
$notifier = [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier("AIQuotaGlass")
$notifier.Show($toast)
`

// Show renders a Windows toast using PowerShell's WinRT bridge.
func (n *windowsNotifier) Show(_ context.Context, title, message string) error {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	scriptPath := filepath.Join(dir, "AIQuotaGlass", "toast.ps1")
	_ = os.MkdirAll(filepath.Dir(scriptPath), 0o755)
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		return err
	}
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden",
		"-ExecutionPolicy", "Bypass", "-File", scriptPath, title, message)
	cmd.SysProcAttr = sysProcAttrHidden
	return cmd.Run()
}
