package conn_reader

import (
  "testing"
  "net"
  "fmt"
)

type objT struct {
  magic string
  conn *net.TCPConn
}

func TestConnReader(t *testing.T) {
  reader := New()
  addr, _ := net.ResolveTCPAddr("tcp", "localhost:54322")
  ready := make(chan struct{})
  go func() {
    ln, err := net.ListenTCP("tcp", addr)
    if err != nil { t.Fatal(err) }
    close(ready)
    for {
      conn, err := ln.AcceptTCP()
      if err != nil { t.Fatal(err) }
      reader.Add(conn, objT{"hello", conn})
    }
  }()
  <-ready
  n := 50
  for i := 0; i < n; i++ {
    conn, err := net.DialTCP("tcp", nil, addr)
    if err != nil { t.Fatal(err) }
    conn.Write([]byte(fmt.Sprintf("%d", i)))
    conn.Close()
  }
  received := 0
  for {
    msg := (<-reader.Messages.Out).(Message)
    obj := msg.Obj.(objT)
    if obj.magic != "hello" { t.Fatal("magic not match") }
    switch msg.Type {
    case DATA:
      fmt.Printf("%s %s\n", obj.magic, msg.Data)
    case EOF:
      obj.conn.Close()
      received += 1
    case ERROR:
      t.Fatal(msg.Data)
    }
    if received == n {
      break
    }
  }
}
