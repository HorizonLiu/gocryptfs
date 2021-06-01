package gocryptfs

// 对外提供的gocrypt的API
func GoCryptAPI(cmd []string, password string) {
	doMain(cmd, password)
}
