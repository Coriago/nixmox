package installerui

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
)

// Read a single line without buffering ahead: nmcli must receive password input
// from the same terminal after the user selects a network.
func readChoice(in io.Reader) (string, error) {
	var line strings.Builder
	var one [1]byte
	for {
		_, err := io.ReadFull(in, one[:])
		if err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return strings.TrimSpace(line.String()), nil
		}
		if line.Len() >= 128 {
			return "", fmt.Errorf("menu input is too long")
		}
		line.WriteByte(one[0])
	}
}

func display(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

func Run(ctx context.Context, backend Backend, in io.Reader, out io.Writer, auto bool, status func(context.Context) ([]byte, error)) error {
	if auto {
		backend.Wait(ctx)
	}
	devices, err := backend.Devices(ctx)
	if err != nil {
		return err
	}
	if auto {
		wifi := false
		for _, d := range devices {
			wifi = wifi || d.Kind == "wifi"
			if backend.Ready(d) {
				kind := "Wired"
				if d.Kind == "wifi" {
					kind = "Wi-Fi"
				}
				fmt.Fprintf(out, "%s connection ready; nixmox discovery is running.\n", kind)
				return nil
			}
		}
		if !wifi {
			fmt.Fprintln(out, "No Wi-Fi adapter found. Connect Ethernet or check the adapter.\nRun nixmox installer to open network setup again.")
			return nil
		}
	}
	for ctx.Err() == nil {
		fmt.Fprintln(out, "\nNixmox installer — network setup")
		fmt.Fprintln(out, "Connect this machine, then select it in the cluster browser to configure and install it.")
		networks, scanErr := backend.Scan(ctx)
		if scanErr != nil {
			fmt.Fprintln(out, scanErr)
		}
		if len(networks) == 0 {
			fmt.Fprintln(out, "No Wi-Fi networks found. Enable Wi-Fi or rescan.")
		}
		for i, n := range networks {
			active := ""
			if n.Active == "*" {
				active = " [connected]"
			}
			fmt.Fprintf(out, "%d) %s · %s%% · %s · %s%s\n", i+1, display(n.SSID), display(n.Signal), display(n.Security), display(n.Device), active)
		}
		fmt.Fprint(out, "r) Rescan   e) Enable Wi-Fi   s) Nixmox status   q) Return to console\nSelect: ")
		choice, err := readChoice(in)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch strings.ToLower(choice) {
		case "q":
			return nil
		case "r":
			continue
		case "e":
			if err := backend.EnableWiFi(ctx); err != nil {
				fmt.Fprintln(out, err)
			}
		case "s":
			data, err := status(ctx)
			if err != nil {
				fmt.Fprintln(out, err)
			} else {
				fmt.Fprintln(out, string(data))
			}
		default:
			n, err := strconv.Atoi(choice)
			if err != nil || n < 1 || n > len(networks) {
				fmt.Fprintln(out, "Select a listed network number or menu action.")
				continue
			}
			if err := backend.Connect(ctx, networks[n-1], in, out); err != nil {
				fmt.Fprintln(out, "Connection failed or cancelled. Select a network to retry.")
			} else {
				fmt.Fprintln(out, "Connected. Discovery updates automatically; use the cluster browser to continue setup.")
			}
		}
	}
	return ctx.Err()
}
