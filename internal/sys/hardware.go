package sys

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Hardware is a coarse fingerprint used to decide whether hardware-specific
// items (GPU drivers, Bluetooth pairings) may be restored.
type Hardware struct {
	CPUVendor   string   `json:"cpu_vendor"`
	GPUVendors  []string `json:"gpu_vendors"` // nvidia, amd, intel
	BoardVendor string   `json:"board_vendor"`
	BoardName   string   `json:"board_name"`
	ProductName string   `json:"product_name"`
	BTAdapters  []string `json:"bt_adapters"` // MAC addresses
	Arch        string   `json:"arch"`
}

// DetectHardware reads /sys and /proc; it never needs root.
func DetectHardware(ctx context.Context, e *Env) Hardware {
	h := Hardware{Arch: readTrim("/proc/sys/kernel/arch")}
	if h.Arch == "" {
		if out, err := e.Out(ctx, "uname", "-m"); err == nil {
			h.Arch = out
		}
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		s := string(b)
		switch {
		case strings.Contains(s, "AuthenticAMD"):
			h.CPUVendor = "amd"
		case strings.Contains(s, "GenuineIntel"):
			h.CPUVendor = "intel"
		}
	}
	h.BoardVendor = readTrim("/sys/class/dmi/id/board_vendor")
	h.BoardName = readTrim("/sys/class/dmi/id/board_name")
	h.ProductName = readTrim("/sys/class/dmi/id/product_name")
	h.GPUVendors = gpuVendors()
	h.BTAdapters = btAdapters(ctx, e)
	return h
}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// gpuVendors inspects PCI display-class devices.
func gpuVendors() []string {
	set := map[string]bool{}
	devs, _ := filepath.Glob("/sys/bus/pci/devices/*")
	for _, d := range devs {
		class := readTrim(filepath.Join(d, "class"))
		if !strings.HasPrefix(class, "0x03") { // display controller
			continue
		}
		switch readTrim(filepath.Join(d, "vendor")) {
		case "0x10de":
			set["nvidia"] = true
		case "0x1002":
			set["amd"] = true
		case "0x8086":
			set["intel"] = true
		}
	}
	out := []string{}
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var macRe = regexp.MustCompile(`([0-9A-F]{2}:){5}[0-9A-F]{2}`)

func btAdapters(ctx context.Context, e *Env) []string {
	out := []string{}
	ents, _ := filepath.Glob("/sys/class/bluetooth/hci*/address")
	for _, p := range ents {
		if m := strings.ToUpper(readTrim(p)); macRe.MatchString(m) {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		if s, err := e.Out(ctx, "bluetoothctl", "list"); err == nil {
			for _, m := range macRe.FindAllString(strings.ToUpper(s), -1) {
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

// HasGPU reports whether vendor is present.
func (h Hardware) HasGPU(vendor string) bool {
	for _, v := range h.GPUVendors {
		if v == vendor {
			return true
		}
	}
	return false
}

// Satisfies reports whether a hardware requirement tag ("nvidia", "amd-cpu",
// "same-board", "bt:<MAC>") is met by h. src is the fingerprint of the backup source.
func (h Hardware) Satisfies(tag string, src Hardware) bool {
	switch {
	case tag == "":
		return true
	case tag == "nvidia" || tag == "amd" || tag == "intel":
		return h.HasGPU(tag)
	case tag == "amd-cpu":
		return h.CPUVendor == "amd"
	case tag == "intel-cpu":
		return h.CPUVendor == "intel"
	case tag == "same-board":
		return h.BoardName != "" && h.BoardName == src.BoardName && h.BoardVendor == src.BoardVendor
	case strings.HasPrefix(tag, "bt:"):
		mac := strings.TrimPrefix(tag, "bt:")
		for _, a := range h.BTAdapters {
			if a == mac {
				return true
			}
		}
		return false
	}
	return false
}

// Summary is a one-line human description.
func (h Hardware) Summary() string {
	parts := []string{}
	if h.ProductName != "" || h.BoardName != "" {
		parts = append(parts, strings.TrimSpace(h.BoardVendor+" "+h.BoardName))
	}
	if h.CPUVendor != "" {
		parts = append(parts, "CPU: "+h.CPUVendor)
	}
	if len(h.GPUVendors) > 0 {
		parts = append(parts, "GPU: "+strings.Join(h.GPUVendors, "+"))
	}
	return strings.Join(parts, ", ")
}
