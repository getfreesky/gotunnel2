package main

import (
  "log"
  "fmt"
  "./socks"
  cr "./conn_reader"
  "net"
  "./session"
  "time"
  "math/rand"
  "encoding/binary"
)

// configuration
var defaultConfig = map[string]string{
  "local": "localhost:23456",
  "remote": "localhost:34567",
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
  checkConfig("local")
  checkConfig("remote")
  checkConfig("key")

  rand.Seed(time.Now().UnixNano())
}

type Serv struct {
  session *session.Session
  clientConn *net.TCPConn
  hostPort string
  localClosed bool
  remoteClosed bool
}

func main() {
  t1 := time.Now()
  delta := func() string {
    return fmt.Sprintf("%-10.3f", time.Now().Sub(t1).Seconds())
  }

  // socks5 server
  socksServer, err := socks.New(globalConfig["local"])
  if err != nil {
    log.Fatal(err)
  }
  fmt.Printf("socks server listening on %s\n", globalConfig["local"])
  clientReader := cr.New()

  // connect to remote server
  addr, err := net.ResolveTCPAddr("tcp", globalConfig["remote"])
  if err != nil { log.Fatal("cannot resolve remote addr ", err) }
  commId := rand.Int63()
  getServerConn := func() *net.TCPConn {
    serverConn, err := net.DialTCP("tcp", nil, addr)
    if err != nil { log.Fatal("cannot connect to remote server ", err) }
    fmt.Printf("connected to server %v\n", serverConn.RemoteAddr())
    binary.Write(serverConn, binary.LittleEndian, commId)
    return serverConn
  }
  comm := session.NewComm(getServerConn(), []byte(globalConfig["key"]), nil)

  // keepalive
  keepaliveSession := comm.NewSession(-1, []byte(keepaliveSessionMagic), nil)
  keepaliveInterval := time.Second * 5
  keepaliveTicker := time.NewTicker(keepaliveInterval)
  lastRemotePing := time.Now()
  lastConnect := time.Now()

  for { select {
  // keepalive
  case <-keepaliveTicker.C:
    keepaliveSession.Signal(sigPing)
    fmt.Printf("%s ping\n", delta())
    if time.Now().Sub(lastRemotePing) > keepaliveInterval {
      if time.Now().Sub(lastConnect) > time.Minute * 1 {
        fmt.Printf("connection gone bad, reconnecting\n")
        comm = session.NewComm(getServerConn(), []byte(globalConfig["key"]), comm)
        lastConnect = time.Now()
      }
    }
  // new socks client
  case socksClient := <-socksServer.Clients:
    serv := &Serv{
      clientConn: socksClient.Conn,
      hostPort: socksClient.HostPort,
    }
    serv.session = comm.NewSession(-1, []byte(socksClient.HostPort), serv)
    clientReader.Add(socksClient.Conn, serv)
  // client events
  case ev := <-clientReader.Events:
    serv := ev.Obj.(*Serv)
    switch ev.Type {
    case cr.DATA: // client data
      serv.session.Send(ev.Data)
    case cr.EOF, cr.ERROR: // client close
      serv.session.Signal(sigClose)
      serv.localClosed = true
      if serv.remoteClosed { serv.session.Close() }
    }
  // server events
  case ev := <-comm.Events:
    switch ev.Type {
    case session.SESSION:
      log.Fatal("local should not have received this type of event")
    case session.DATA:
      serv := ev.Session.Obj.(*Serv)
      serv.clientConn.Write(ev.Data)
    case session.SIGNAL:
      sig := ev.Data[0]
      if sig == sigClose {
        serv := ev.Session.Obj.(*Serv)
        time.AfterFunc(time.Second * 5, func() {
          serv.clientConn.Close()
        })
        serv.remoteClosed = true
        if serv.localClosed { serv.session.Close() }
      } else if sig == sigPing {
        fmt.Printf("%s pong %10d >< %-10d\n", delta(), comm.BytesSent, comm.BytesReceived)
        lastRemotePing = time.Now()
      }
    case session.ERROR:
      log.Fatal("error when communicating with server ", string(ev.Data))
    }
  }}
}
