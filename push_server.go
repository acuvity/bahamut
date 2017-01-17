// Author: Antoine Mercadal
// See LICENSE file for full LICENSE
// Copyright 2016 Aporeto.

package bahamut

import (
	"net/http"

	"golang.org/x/net/websocket"

	"github.com/Sirupsen/logrus"
	"github.com/aporeto-inc/elemental"
	"github.com/go-zoo/bone"
)

type pushServer struct {
	address         string
	sessions        map[string]*PushSession
	register        chan *PushSession
	unregister      chan *PushSession
	events          chan *elemental.Event
	close           chan bool
	multiplexer     *bone.Mux
	config          Config
	processorFinder processorFinder
}

func newPushServer(config Config, multiplexer *bone.Mux) *pushServer {

	srv := &pushServer{
		sessions:    map[string]*PushSession{},
		register:    make(chan *PushSession),
		unregister:  make(chan *PushSession),
		close:       make(chan bool, 2),
		events:      make(chan *elemental.Event, 1024),
		multiplexer: multiplexer,
		config:      config,
	}

	srv.multiplexer.Handle("/events", websocket.Handler(srv.handlePushConnection))
	srv.multiplexer.Handle("/wsapi", websocket.Handler(srv.handleAPIConnection))

	return srv
}

// adds a new push session to register in the push server
func (n *pushServer) registerSession(session *PushSession) {

	n.register <- session
}

// adds a new push session to unregister from the push server
func (n *pushServer) unregisterSession(session *PushSession) {

	n.unregister <- session
}

// handlePushConnection handle connection for push events
func (n *pushServer) handlePushConnection(ws *websocket.Conn) {

	n.runSession(ws, newPushSession(ws, n))
}

// handlePushConnection handle connection for push events
func (n *pushServer) handleAPIConnection(ws *websocket.Conn) {

	n.runSession(ws, newAPISession(ws, n))
}

func (n *pushServer) runSession(ws *websocket.Conn, session *PushSession) {

	if handler := n.config.WebSocketServer.SessionsHandler; handler != nil {
		ok, err := handler.IsAuthenticated(session)
		if err != nil {
			log.WithError(err).Error("Error during checking authentication.")
		}

		if !ok {
			if session.sType == pushSessionTypeAPI {
				response := elemental.NewResponse()
				writeWebSocketError(ws, response, elemental.NewError("Unauthorized", "You are not authorized to access this api", "bahamut", http.StatusUnauthorized))
			}
			ws.Close()
			return
		}
	}

	if session.sType == pushSessionTypeAPI {
		response := elemental.NewResponse()
		response.StatusCode = http.StatusOK
		websocket.JSON.Send(ws, response)
	}

	n.registerSession(session)
	session.listen()
}

// push a new event. If the global push system is available, it will be used.
// otherwise, only local sessions will receive the push
func (n *pushServer) pushEvents(events ...*elemental.Event) {

	for _, e := range events {
		select {
		case n.events <- e:
		default:
		}
	}
}

func (n *pushServer) closeAllSessions() {

	for _, session := range n.sessions {
		session.close()
	}
	n.sessions = map[string]*PushSession{}
}

// starts the push server
func (n *pushServer) start() {

	log.WithField("endpoint", n.address+"/events").Info("Starting event server.")

	for {
		select {

		case session := <-n.register:

			if _, ok := n.sessions[session.id]; ok {
				break
			}

			n.sessions[session.id] = session

			log.WithFields(logrus.Fields{
				"total":  len(n.sessions),
				"client": session.socket.RemoteAddr(),
			}).Debug("Push session started.")

			if handler := n.config.WebSocketServer.SessionsHandler; handler != nil {
				handler.OnPushSessionStart(session)
			}

		case session := <-n.unregister:

			if _, ok := n.sessions[session.id]; !ok {
				break
			}

			delete(n.sessions, session.id)

			log.WithFields(logrus.Fields{
				"total":  len(n.sessions),
				"client": session.socket.RemoteAddr(),
			}).Debug("Push session closed.")

			if handler := n.config.WebSocketServer.SessionsHandler; handler != nil {
				handler.OnPushSessionStop(session)
			}

		case event := <-n.events:

			if n.config.WebSocketServer.Service != nil {
				publication := NewPublication(n.config.WebSocketServer.Topic)
				if err := publication.Encode(event); err != nil {
					log.WithFields(logrus.Fields{
						"topic": publication.Topic,
						"event": event,
						"error": err,
					}).Error("Unable to encode ervent. Message dropped.")
				}
				err := n.config.WebSocketServer.Service.Publish(publication)
				if err != nil {
					log.WithFields(logrus.Fields{
						"topic": publication.Topic,
						"event": event,
						"error": err,
					}).Warn("Unable to publish. Message dropped.")
				}
			}

		case <-n.close:
			log.Info("Stopping push server.")

			n.closeAllSessions()
			return
		}
	}
}

// stops the push server
func (n *pushServer) stop() {

	n.close <- true
}
