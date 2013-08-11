package conn_reader

import (
  "net"
  "math/rand"
  "time"
  "io"
  "sync/atomic"
)

const BUFFER_SIZE = 1280

func init() {
  rand.Seed(time.Now().UnixNano())
}

type ConnReader struct {
  Events chan Event
  Count int32
}

const (
  DATA = iota
  EOF
  ERROR
)

type Event struct {
  Type int
  Data []byte
  Obj interface{}
}

func New() *ConnReader {
  self := new(ConnReader)
  self.Events = make(chan Event, 4096)
  return self
}

func (self *ConnReader) Add(tcpConn *net.TCPConn, obj interface{}) {
  atomic.AddInt32(&self.Count, int32(1))
  go func() {
    defer atomic.AddInt32(&self.Count, int32(-1))
    for {
      buf := make([]byte, BUFFER_SIZE)
      n, err := tcpConn.Read(buf)
      if n > 0 {
        self.Events <- Event{DATA, buf[:n], obj}
      }
      if err != nil {
        if err == io.EOF { //EOF
          self.Events <- Event{EOF, nil, obj}
        } else { //ERROR
          self.Events <- Event{ERROR, []byte(err.Error()), obj}
        }
        return
      }
    }
  }()
}
