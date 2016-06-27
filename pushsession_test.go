// Author: Antoine Mercadal
// See LICENSE file for full LICENSE
// Copyright 2016 Aporeto.

package bahamut

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Shopify/sarama"
	"github.com/go-zoo/bone"
	. "github.com/smartystreets/goconvey/convey"
	"golang.org/x/net/websocket"
)

func TestSession_newSession(t *testing.T) {

	Convey("When I create have a new pushSession", t, func() {

		ws := &websocket.Conn{}
		session := newSession(ws, newPushServer("fake", bone.New(), nil))

		Convey("Then the session id should not be empty", func() {
			So(session.id, ShouldNotBeEmpty)
		})

		Convey("Then the socket should be nil", func() {
			So(session.socket, ShouldEqual, ws)
		})

		Convey("Then the events channel should be a chan of bytes", func() {
			So(session.events, ShouldHaveSameTypeAs, make(chan string))
		})
	})
}

func TestSession_listenToKafkaMessages(t *testing.T) {

	Convey("Given I create have a new pushSession with valid kafka info", t, func() {

		broker := sarama.NewMockBroker(t, 1)
		broker.SetHandlerByMap(map[string]sarama.MockResponse{
			"MetadataRequest": sarama.NewMockMetadataResponse(t).
				SetBroker(broker.Addr(), broker.BrokerID()).
				SetLeader("topic", 0, broker.BrokerID()),
			"OffsetRequest": sarama.NewMockOffsetResponse(t).
				SetOffset("topic", 0, sarama.OffsetOldest, 0).
				SetOffset("topic", 0, sarama.OffsetNewest, 0),
			"FetchRequest": sarama.NewMockFetchResponse(t, 1).
				SetMessage("topic", 0, 0, sarama.StringEncoder(`{"hello":"world"}`)),
		})

		KafkaInfo := NewKafkaInfo([]string{broker.Addr()}, "topic")
		ws := &websocket.Conn{}
		session := newSession(ws, newPushServer("fake", bone.New(), KafkaInfo))

		Convey("When I listen for kafka messages", func() {
			go session.listenToKafkaMessages()

			var message []byte
			select {
			case message = <-session.out:
			case <-time.After(3 * time.Millisecond):
				break
			}

			Convey("Then the messge should be correct", func() {
				So(string(message), ShouldEqual, `{"hello":"world"}`)
			})
		})

		Convey("When I get a stop while I listen for messages", func() {
			c := make(chan bool, 1)
			go func() {
				session.listenToKafkaMessages()
				c <- true
			}()

			session.close()

			var returned bool
		LOOP:
			for {
				select {
				case returned = <-c:
					break LOOP
				case <-session.server.unregister:
				case <-time.After(3 * time.Millisecond):
					break LOOP
				}
			}

			Convey("Then the function should exit correctly", func() {
				So(returned, ShouldBeTrue)
			})
		})
	})

	Convey("Given I create have a new pushSession with invalid kafka info", t, func() {

		broker := sarama.NewMockBroker(t, 1)
		errorResponse := &sarama.FetchResponse{}
		errorResponse.AddError("topic", 0, sarama.ErrInvalidTopic)
		broker.SetHandlerByMap(map[string]sarama.MockResponse{
			"MetadataRequest": sarama.NewMockMetadataResponse(t).
				SetBroker(broker.Addr(), broker.BrokerID()).
				SetLeader("topic", 0, broker.BrokerID()),
			"OffsetRequest": sarama.NewMockWrapper(errorResponse),
		})
		KafkaInfo := NewKafkaInfo([]string{broker.Addr()}, "topic")
		ws := &websocket.Conn{}
		session := newSession(ws, newPushServer("fake", bone.New(), KafkaInfo))

		Convey("When I listen for kafka messages", func() {

			err := session.listenToKafkaMessages()

			Convey("Then it should return right an error", func() {
				So(err, ShouldNotBeNil)
			})
		})
	})
}

func TestSession_listenToLocalMessages(t *testing.T) {

	Convey("Given I create have a new pushSession with no valid kafka info", t, func() {

		ws := &websocket.Conn{}
		session := newSession(ws, newPushServer("fake", bone.New(), nil))

		Convey("When I listen for local messages", func() {
			go session.listenToLocalMessages()

			session.events <- `{"hello":"world"}`
			var message []byte
			select {
			case message = <-session.out:
			case <-time.After(3 * time.Millisecond):
				break
			}

			Convey("Then the messge should be correct", func() {
				So(string(message), ShouldEqual, `{"hello":"world"}`)
			})
		})

		Convey("When I get a stop while I listen for messages", func() {
			c := make(chan bool, 1)
			go func() {
				session.listenToLocalMessages()
				c <- true
			}()

			session.close()

			var returned bool
		LOOP:
			for {
				select {
				case returned = <-c:
					break LOOP
				case <-session.server.unregister:
				case <-time.After(3 * time.Millisecond):
					break LOOP
				}
			}

			Convey("Then the function should exit correctly", func() {
				So(returned, ShouldBeTrue)
			})
		})
	})

	Convey("Given I create have a new pushSession with invalid kafka info", t, func() {

		broker := sarama.NewMockBroker(t, 1)
		errorResponse := &sarama.FetchResponse{}
		errorResponse.AddError("topic", 0, sarama.ErrInvalidTopic)
		broker.SetHandlerByMap(map[string]sarama.MockResponse{
			"MetadataRequest": sarama.NewMockMetadataResponse(t).
				SetBroker(broker.Addr(), broker.BrokerID()).
				SetLeader("topic", 0, broker.BrokerID()),
			"OffsetRequest": sarama.NewMockWrapper(errorResponse),
		})
		KafkaInfo := NewKafkaInfo([]string{broker.Addr()}, "topic")
		ws := &websocket.Conn{}
		session := newSession(ws, newPushServer("fake", bone.New(), KafkaInfo))

		Convey("When I listen for kafka messages", func() {

			err := session.listenToKafkaMessages()

			Convey("Then it should return right an error", func() {
				So(err, ShouldNotBeNil)
			})
		})
	})
}

func TestSession_write(t *testing.T) {

	Convey("Given I create a session with a websocket", t, func() {

		ts := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
			var d []byte
			websocket.Message.Receive(ws, &d)
			websocket.Message.Send(ws, d)
		}))
		defer ts.Close()

		ws, _ := websocket.Dial("ws"+ts.URL[4:], "", ts.URL)
		defer ws.Close()

		session := newSession(ws, newPushServer("fake", bone.New(), nil))

		Convey("When I send some data to the session", func() {

			go session.write()

			session.out <- []byte("hello world")

			var data []byte
			websocket.Message.Receive(ws, &data)

			Convey("Then the websocket should receive the data", func() {
				So(string(data), ShouldEqual, "hello world")
			})
		})

		Convey("When I stop the session while listening to the websocket", func() {

			c := make(chan bool, 1)
			go func() {
				session.write()
				c <- true
			}()

			session.close()

			var returned bool
			select {
			case returned = <-c:
			case <-time.After(3 * time.Millisecond):
				break
			}

			Convey("Then the function should exit", func() {
				So(returned, ShouldBeTrue)
			})
		})

		Convey("When the websocket is closed while I'm listening", func() {

			c := make(chan bool, 1)
			go func() {
				session.write()
				c <- true
			}()

			ws.Close()
			session.out <- []byte("hello world")

			var returned bool
			select {
			case returned = <-c:
			case <-time.After(3 * time.Millisecond):
				break
			}

			Convey("Then the write function should exit", func() {
				So(returned, ShouldBeTrue)
			})
		})
	})
}

func TestSession_read(t *testing.T) {

	Convey("Given I create a session with a websocket", t, func() {

		dt := make(chan []byte)
		ts := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
			websocket.Message.Send(ws, <-dt)
		}))
		defer ts.Close()

		ws, _ := websocket.Dial("ws"+ts.URL[4:], "", ts.URL)
		defer ws.Close()

		session := newSession(ws, newPushServer("fake", bone.New(), nil))

		Convey("When I receive some data to the session", func() {

			c := make(chan bool, 1)
			go func() {
				session.read()
				c <- true
			}()

			dt <- []byte("hello world")

			var returned bool
			select {
			case returned = <-c:
			case <-time.After(3 * time.Millisecond):
				break
			}

			Convey("Then the write function should not exit", func() {
				So(returned, ShouldBeTrue) // TODO: this is should be False.
			})
		})
	})
}
