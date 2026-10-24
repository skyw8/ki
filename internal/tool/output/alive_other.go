//go:build !unix

package output

func processAlive(int) (alive bool, known bool) {
	return false, false
}
