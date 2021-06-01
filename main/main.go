package main

import "github.com/HorizonLiu/gocryptfs"

func main() {
	var clientCmd = []string{"gocryptfs", "-fg", "-zerokey", "-nosyslog", "-ro", "cipher", "plain"}
	var fsPwd = "123456"
	gocryptfs.GoCryptAPI(clientCmd, fsPwd)

}
