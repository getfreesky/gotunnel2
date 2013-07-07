package main

import (
  "log"
  "fmt"
  "./socks"
  cr "./conn_reader"
  "net"
  "./session"
  "time"
)

// configuration
var defaultConfig = map[string]string{
  "local": "localhost:23456",
  "remote": "localhost:34567",
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
}

type Session struct {
  *session.Session
  clientConn *net.TCPConn
}

func main() {
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
  serverConn, err := net.DialTCP("tcp", nil, addr)
  if err != nil { log.Fatal("cannot connect to remote server ", err) }
  defer serverConn.Close()
  fmt.Printf("connected to server %v\n", serverConn.RemoteAddr())
  comm := session.NewComm(serverConn)

  for { select {
  // new socks client
  case socksClientI := <-socksServer.Clients.Out:
    socksClient := socksClientI.(*socks.Client)
    session := &Session{
      comm.NewSession(-1, []byte(socksClient.HostPort), socksClient.Conn),
      socksClient.Conn,
    }
    clientReader.Add(socksClient.Conn, session)
  // client events
  case evI := <-clientReader.Events.Out:
    ev := evI.(cr.Event)
    session := ev.Obj.(*Session)
    switch ev.Type {
    case cr.DATA: // client data
      session.Send(ev.Data)
    case cr.EOF, cr.ERROR: // client close
      session.Close()
    }
  // server events
  case evI := <-comm.Events.Out:
    ev := evI.(session.Event)
    switch ev.Type {
    case session.SESSION:
      log.Fatal("local should not have received this type of event")
    case session.DATA:
      ev.Session.Obj.(*net.TCPConn).Write(ev.Data)
    case session.CLOSE:
      ev.Session.Close()
      defer func() {
        <-time.After(time.Second * 5)
        ev.Session.Obj.(*net.TCPConn).Close()
      }()
    case session.ERROR:
      log.Fatal("error when communicating with server ", ev.Data)
    }
  }}
}
