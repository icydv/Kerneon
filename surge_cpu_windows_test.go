//go:build windows

package main

import "testing"

func TestSurgeCPUVendorUsesRawIdentityAndBrand(t *testing.T) {
	tests := []struct{ vendor, brand, want string }{
		{"AuthenticAMD", "AMD Ryzen 7 5800X", "AMD"},
		{"GenuineIntel", "Intel(R) Core(TM)", "Intel"},
		{"SyntheticCPU", "Virtual processor", "Other x86"},
	}
	for _, test := range tests {
		if got := surgeCPUVendor(test.vendor, test.brand); got != test.want {
			t.Fatalf("surgeCPUVendor(%q, %q)=%q, want %q", test.vendor, test.brand, got, test.want)
		}
	}
}
