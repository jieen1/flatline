// The service subcommand keeps the archive promise operational: harnesses
// delete transcripts after their retention window whether or not the daemon
// was running, so the rescuer has to survive reboots. install writes a
// systemd user unit and enables it; on hosts where the user bus is not up
// (WSL without lingering, a bare SSH session) it says exactly which one-time
// command fixes that instead of pretending it worked.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const serviceName = "flatline.service"

// serviceUnit is the unit text. Asset scope is user: a service has no project
// working directory, and defaulting the project scope would register $HOME as
// an asset root. History roots resolve themselves.
func serviceUnit(binary string) string {
	return `[Unit]
Description=Flatline local daemon (loopback agent-session archive and monitor)

[Service]
ExecStart=` + binary + ` daemon -asset-scope user
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
}

func serviceUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user", serviceName), nil
}

func runService(args []string) error {
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	action := fs.Arg(0)
	switch action {
	case "install":
		return serviceInstall()
	case "uninstall":
		return serviceUninstall()
	case "status":
		return serviceStatus()
	default:
		fmt.Fprintln(os.Stderr, `usage: flatline service <install|uninstall|status>

install    write the systemd user unit and enable it (daemon survives reboots)
uninstall  disable the unit and remove the file
status     show whether the unit is installed and running`)
		if action == "" {
			return fmt.Errorf("no action given")
		}
		return fmt.Errorf("unknown action %q", action)
	}
}

func serviceInstall() error {
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own binary path: %w", err)
	}
	if binary, err = filepath.EvalSymlinks(binary); err != nil {
		return fmt.Errorf("resolve binary symlinks: %w", err)
	}
	unitPath, err := serviceUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("create unit directory: %w", err)
	}
	if err := os.WriteFile(unitPath, []byte(serviceUnit(binary)), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	fmt.Printf("unit written: %s\n", unitPath)

	if err := userSystemctl("daemon-reload"); err != nil {
		return explainNoUserBus(err)
	}
	if err := userSystemctl("enable", "--now", serviceName); err != nil {
		return explainNoUserBus(err)
	}
	fmt.Println("service enabled and started; it now survives reboots")
	fmt.Println("note: another manually started daemon on the same port will make this one exit; stop it first if the service reports a bind failure")
	return nil
}

func serviceUninstall() error {
	unitPath, err := serviceUnitPath()
	if err != nil {
		return err
	}
	// Best-effort stop; the file removal is the uninstall either way.
	_ = userSystemctl("disable", "--now", serviceName)
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}
	fmt.Println("service uninstalled")
	return nil
}

func serviceStatus() error {
	unitPath, err := serviceUnitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(unitPath); err != nil {
		fmt.Println("unit: not installed (run: flatline service install)")
		return nil
	}
	fmt.Printf("unit: %s\n", unitPath)
	if err := userSystemctl("is-active", serviceName); err != nil {
		return explainNoUserBus(err)
	}
	return nil
}

// userSystemctl runs systemctl --user with the runtime dir filled in when the
// shell lacks it — a non-login shell on WSL commonly does.
func userSystemctl(args ...string) error {
	command := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	command.Env = os.Environ()
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		command.Env = append(command.Env, fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Getuid()))
	}
	return command.Run()
}

// explainNoUserBus turns the opaque bus failure into the one-time fix. The
// unit file is already on disk at this point, so once the bus exists the
// enable step is all that remains.
func explainNoUserBus(err error) error {
	return fmt.Errorf(`systemctl --user failed: %w

the unit file is written; what is missing is the per-user systemd instance.
on WSL or a bare SSH login, enable lingering once (needs sudo):

  sudo loginctl enable-linger %s

then finish with:

  systemctl --user daemon-reload && systemctl --user enable --now %s`,
		err, userName(), serviceName)
}

func userName() string {
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	return strings.TrimSpace(fmt.Sprintf("%d", os.Getuid()))
}
