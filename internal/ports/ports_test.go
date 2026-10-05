package ports

import (
	"net"
	"testing"
)

func TestIsFreeLoopbackListener(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if IsFree(port) {
		t.Fatalf("port %d reported free while listening", port)
	}
	l.Close()
	if !IsFree(port) {
		t.Fatalf("port %d reported busy after close", port)
	}
}

func TestIsFreeWildcardListener(t *testing.T) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if IsFree(l.Addr().(*net.TCPAddr).Port) {
		t.Fatal("wildcard listener not detected")
	}
}
