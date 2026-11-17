package selfupdate

import "net"

func netListen() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }

func dial(addr string) (net.Conn, error) { return net.Dial("tcp", addr) }
