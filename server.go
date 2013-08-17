package main

import (
  box "github.com/nsf/termbox-go"
  "net"
  "log"
  cr "./conn_reader"
  "./session"
  "time"
  "io"
  "os"
  _ "net/http/pprof"
  "net/http"
  "encoding/binary"
  "bytes"
  "sync"
  "runtime"
  "reflect"
  "fmt"
  "./utils"
)

// configuration
var defaultConfig = map[string]string{
  "listen": "0.0.0.0:34567",
  "key": "foo bar baz foo bar baz ",
}
var globalConfig = loadConfig(defaultConfig)
func checkConfig(key string) {
  if value, ok := globalConfig[key]; !ok || value == "" {
    globalConfig[key] = defaultConfig[key]
    saveConfig(globalConfig)
    globalConfig = loadConfig(defaultConfig)
  }
}
func init() {
  checkConfig("listen")
  checkConfig("key")
  go func() {
    http.ListenAndServe("0.0.0.0:55555", nil)
  }()
}

func main() {
  // log stack
  defer func() {
    if r := recover(); r != nil {
      buf := make([]byte, 1024 * 1024 * 8)
      n := runtime.Stack(buf, true)
      errFile, err := os.Create("err.server")
      if err != nil { return }
      errFile.WriteString(fmt.Sprintf("error: %v\n\n", r))
      errFile.Write(buf[:n])
      errFile.Close()
    }
  }()
  // termbox
  err := box.Init()
  if err != nil { log.Fatal(err) }
  defer box.Close()
  go func() { for {
    ev := box.PollEvent()
    if ev.Type == box.EventKey {
      if ev.Key == box.KeyEsc {
        os.Exit(0)
      }
    }
  }}()

  addr, err := net.ResolveTCPAddr("tcp", globalConfig["listen"])
  if err != nil { log.Fatal("cannot resolve listen address ", err) }
  ln, err := net.ListenTCP("tcp", addr)
  if err != nil { log.Fatal("cannot listen ", err) }
  connChangeChans := make(map[int64]chan *net.TCPConn)
  for {
    conn, err := ln.AcceptTCP()
    if err != nil { continue }
    go func() {
      var commId int64
      // auth
      origin := make([]byte, 64)
      n, err := io.ReadFull(conn, origin)
      if err != nil || n != 64 {
        conn.Close()
        return
      }
      encrypted := make([]byte, 64)
      n, err = io.ReadFull(conn, encrypted)
      if err != nil || n != 64 {
        conn.Close()
        return
      }
      expected, err := encrypt([]byte(globalConfig["key"]), origin)
      if err != nil { log.Fatal(err) }
      if bytes.Compare(expected, encrypted) != 0 { // auth fail
        conn.Write([]byte{0x0})
        conn.Close()
        return
      } else {
        conn.Write([]byte{0x1})
      }
      // read comm id
      err = binary.Read(conn, binary.LittleEndian, &commId)
      if err != nil { return }
      c, ok := connChangeChans[commId]
      if ok { // change conn
        c <- conn
      } else { // handle new comm
        newConnIn := make(chan *net.TCPConn)
        newConn := utils.MakeChan(newConnIn).(<-chan *net.TCPConn)
        connChangeChans[commId] = newConnIn
        handleClient(conn, newConn, newConnIn)
      }
    }()
  }
}

type Serv struct {
  session *session.Session
  sendQueue [][]byte
  targetConn io.Writer
  localClosed bool
  remoteClosed bool
  closeTargetConnOnce sync.Once
  hostPort string
  closeOnce sync.Once
}

func handleClient(conn *net.TCPConn, connChange <-chan *net.TCPConn, connChangeIn chan *net.TCPConn) {
  defer close(connChangeIn)
  targetReader := cr.New()
  defer targetReader.Close()
  comm := session.NewComm(conn, []byte(globalConfig["key"]))
  targetConnEvents := make(chan *Serv)
  connectTarget := func(serv *Serv, hostPort string) {
    defer func() {
      targetConnEvents <- serv
    }()
    addr, err := net.ResolveTCPAddr("tcp", hostPort)
    if err != nil { return }
    targetConn, err := net.DialTCP("tcp", nil, addr)
    if err != nil { return }
    targetReader.Add(targetConn, serv)
    serv.targetConn = targetConn
    return
  }

  heartbeat := time.NewTicker(time.Second * 1)
  var memStats runtime.MemStats
  printer := NewPrinter(40)

  loop: for { select {
  // heartbeat
  case <-heartbeat.C:
    if time.Now().Sub(comm.LastReadTime) > time.Minute * 5 {
      break loop
    }
    box.Clear(box.ColorDefault, box.ColorDefault)
    printer.Reset()
    runtime.ReadMemStats(&memStats)
    printer.Print("conf %s", CONFIG_FILEPATH)
    printer.Print("%s memory in use", formatFlow(memStats.Alloc))
    printer.Print("%d connections", targetReader.Count)
    printer.Print("--- %d sessions ---", len(comm.Sessions))
    for _, sessionId := range ByValue(comm.Sessions, func(a, b reflect.Value) bool {
      return a.Interface().(*session.Session).StartTime.After(b.Interface().(*session.Session).StartTime)
    }).Interface().([]int64) {
      session := comm.Sessions[sessionId]
      serv, ok := session.Obj.(*Serv)
      if !ok { continue }
      if serv.localClosed {
        printer.Print("Lx %s", serv.hostPort)
      } else if serv.remoteClosed {
        printer.Print("Rx %s", serv.hostPort)
      } else {
        printer.Print(serv.hostPort)
      }
    }
    box.Flush()
  // conn change
  case conn := <-connChange:
    comm.UseConn(conn)
  // local-side events
  case ev := <-comm.Events:
    switch ev.Type {
    case session.SESSION: // new local session
      hostPort := string(ev.Data)
      if hostPort == keepaliveSessionMagic {
        continue loop
      }
      serv := &Serv{
        sendQueue: make([][]byte, 0, 8),
        hostPort: hostPort,
      }
      serv.session = ev.Session
      ev.Session.Obj = serv
      go connectTarget(serv, hostPort)
    case session.DATA: // local data
      serv := ev.Session.Obj.(*Serv)
      if serv.targetConn == nil {
        serv.sendQueue = append(serv.sendQueue, ev.Data)
      } else {
        serv.targetConn.Write(ev.Data)
      }
    case session.SIGNAL: // local session closed
      sig := ev.Data[0]
      if sig == sigClose {
        serv := ev.Session.Obj.(*Serv)
        time.AfterFunc(time.Second * 3, func() { serv.CloseConn() })
        serv.remoteClosed = true
        if serv.localClosed {
          serv.Close()
        } else {
          time.AfterFunc(time.Minute * 3, func() { serv.Close() })
        }
      } else if sig == sigPing { // from keepaliveSession
        ev.Session.Signal(sigPing)
      }
    case session.ERROR: // error
      break loop
    }
  // target connection events
  case serv := <-targetConnEvents:
    if serv.targetConn == nil { // fail to connect to target
      serv.session.Signal(sigClose)
      serv.localClosed = true
      if serv.remoteClosed {
        serv.Close()
      } else {
        time.AfterFunc(time.Minute * 3, func() { serv.Close() })
      }
      continue loop
    }
    for _, data := range serv.sendQueue {
      serv.targetConn.Write(data)
    }
    serv.sendQueue = nil
  // target events
  case ev := <-targetReader.Events:
    serv := ev.Obj.(*Serv)
    switch ev.Type {
    case cr.DATA:
      serv.session.Send(ev.Data)
    case cr.EOF, cr.ERROR:
      if serv.session == nil { // serv already closed
        continue loop
      }
      serv.session.Signal(sigClose)
      serv.localClosed = true
      if serv.remoteClosed {
        serv.Close()
      } else {
        time.AfterFunc(time.Minute * 3, func() { serv.Close() })
      }
    }
  }}

  // clear
  for _, session := range comm.Sessions {
    serv, ok := session.Obj.(*Serv)
    if ok {
      serv.CloseConn()
      serv.Close()
    } else {
      session.Close()
    }
  }
  comm.Close()
}

func (self *Serv) Close() {
  self.closeOnce.Do(func() {
    self.session.Close()
    self.session = nil
  })
}

func (self *Serv) CloseConn() {
  if self.targetConn != nil {
    self.closeTargetConnOnce.Do(func() {
      self.targetConn.(*net.TCPConn).Close()
    })
  }
}
