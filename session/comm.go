package session

import (
  "net"
  "math/rand"
  "time"
  "encoding/binary"
  "io"
  "fmt"
  "sync/atomic"
  "bytes"
)

func init() {
  rand.Seed(time.Now().UnixNano())
}

const (
  SESSION = iota
  DATA
  SIGNAL
  ERROR
)

type Event struct {
  Type int
  Session *Session
  Data []byte
}

type Comm struct {
  conn *net.TCPConn
  sessions map[int64]*Session
  sendQueue chan []byte
  Events chan Event
  serial uint64
  maxReceivedSerial uint64
  maxAckSerial uint64
}

func NewComm(conn *net.TCPConn) (*Comm) {
  c := &Comm{
    conn: conn,
    sessions: make(map[int64]*Session),
    sendQueue: make(chan []byte, 65536),
    Events: make(chan Event, 65536),
  }
  go c.startSender()
  go c.startReader()
  go c.startAck()
  return c
}

func (self *Comm) nextSerial() uint64 {
  return atomic.AddUint64(&(self.serial), uint64(1))
}

func (self *Comm) startSender() {
  for {
    self.conn.Write(<-self.sendQueue)
  }
}

func (self *Comm) startReader() {
  var id int64
  var t uint8
  var dataLen uint32
  var serial uint64
  loop: for {
    binary.Read(self.conn, binary.LittleEndian, &serial)
    binary.Read(self.conn, binary.LittleEndian, &id)
    binary.Read(self.conn, binary.LittleEndian, &t)
    if t == typeAck { // ack packet
      self.maxAckSerial = serial
      continue loop
    }
    binary.Read(self.conn, binary.LittleEndian, &dataLen)
    data := make([]byte, dataLen)
    n, err := io.ReadFull(self.conn, data)
    if err != nil || uint32(n) != dataLen {
      self.emit(Event{Type: ERROR, Data: []byte("error occurred when reading data")})
      return
    }
    self.maxReceivedSerial = serial
    switch t {
    case typeConnect:
      session := self.NewSession(id, nil, nil)
      self.emit(Event{Type: SESSION, Session: session, Data: data})
    case typeData:
      session, ok := self.sessions[id]
      if !ok {
        self.emit(Event{Type: ERROR, Session: &Session{Id: id}, Data: []byte("unregistered session id")})
        return
      }
      self.emit(Event{Type: DATA, Session: session, Data: data})
    case typeSignal:
      session, ok := self.sessions[id]
      if !ok {
        self.emit(Event{Type: ERROR, Session: &Session{Id: id}, Data: []byte("unregistered session id")})
        return
      }
      self.emit(Event{Type: SIGNAL, Session: session, Data: data})
    default:
      self.emit(Event{Type: ERROR, Data: []byte(fmt.Sprintf("unrecognized packet type %s", t))})
      return
    }
  }
}

func (self *Comm) startAck() {
  for {
    buf := new(bytes.Buffer)
    binary.Write(buf, binary.LittleEndian, self.maxReceivedSerial)
    binary.Write(buf, binary.LittleEndian, rand.Int63())
    binary.Write(buf, binary.LittleEndian, typeAck)
    self.sendQueue <- buf.Bytes()
    <-time.After(time.Millisecond * 200)
  }
}

func (self *Comm) emit(ev Event) {
  self.Events <- ev
}

func (self *Comm) NewSession(id int64, data []byte, obj interface{}) (*Session) {
  isNew := false
  if id <= int64(0) {
    isNew = true
    id = rand.Int63()
  }
  session := &Session{
    Id: id,
    comm: self,
    Obj: obj,
  }
  if isNew {
    self.sendQueue <- session.constructPacket(typeConnect, data)
  }
  self.sessions[id] = session
  return session
}
