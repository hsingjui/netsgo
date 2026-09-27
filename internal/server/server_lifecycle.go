package server

import (
	"context"

	"github.com/gorilla/websocket"
)

func (s *Server) beginLongLivedHandler() (func(), bool) {
	return s.sessions.beginLongLivedHandler()
}

func (s *Server) trackManagedConn(conn *websocket.Conn) (func(), bool) {
	return s.sessions.trackManagedConn(conn)
}

func (s *Server) stopLongLivedAdmission() {
	s.sessions.beginShutdown()
}

func (s *Server) closeManagedConns(reason string) {
	s.sessions.closeManagedConns(reason)
}

func (s *Server) waitForLongLivedHandlers(ctx context.Context) error {
	return s.sessions.waitForLongLivedHandlers(ctx)
}
