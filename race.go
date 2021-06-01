// +build race

package gocryptfs

func init() {
	// adds " -race" to the output of "gocryptfs -version"
	raceDetector = true
}
