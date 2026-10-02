package codexclient

import "golang.org/x/sys/unix"

func hostOS() (string, string) {
	version, err := unix.Sysctl("kern.osproductversion")
	if err != nil || version == "" {
		return "Mac OS", "unknown"
	}
	return "Mac OS", version
}
