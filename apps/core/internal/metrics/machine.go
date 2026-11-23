package metrics

import (
	"bytes"
	"path/filepath"
	"strings"
)

// placeholders are what firmware writes where the maker set no name.
var placeholders = []string{"to be filled by o.e.m.", "system product name", "system manufacturer", "default string", "not applicable", "none", "o.e.m.", "oem"}

// machineModel is the machine's maker and model from DMI, or its
// motherboard's where the maker left the model unset; "" where the firmware
// says neither.
func machineModel(dmi string) string {
	read := func(name string) string {
		b, _ := readFile(filepath.Join(dmi, name))
		v := strings.Join(strings.Fields(string(b)), " ")
		for _, p := range placeholders {
			if strings.EqualFold(v, p) {
				return ""
			}
		}
		return v
	}
	if m := maker(read("sys_vendor"), read("product_name")); m != "" {
		return m
	}
	return maker(read("board_vendor"), read("board_name"))
}

// maker joins a vendor and a model, without the vendor twice.
func maker(vendor, model string) string {
	switch {
	case model == "":
		return ""
	case vendor == "" || strings.HasPrefix(strings.ToLower(model), strings.ToLower(vendor)):
		return model
	}
	return vendor + " " + model
}

// cpuName is the first processor's model name in /proc/cpuinfo ("Model" on
// some ARM boards); "" when it has none.
func cpuName(proc string) string {
	b, _ := readFile(filepath.Join(proc, "cpuinfo"))
	for line := range bytes.Lines(b) {
		k, v, ok := strings.Cut(string(line), ":")
		if k = strings.TrimSpace(k); ok && (k == "model name" || k == "Model") {
			return strings.Join(strings.Fields(v), " ")
		}
	}
	return ""
}
