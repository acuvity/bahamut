// Author: Antoine Mercadal
// See LICENSE file for full LICENSE
// Copyright 2016 Aporeto.

package bahamut

import (
	"fmt"
	"testing"

	"github.com/aporeto-inc/elemental"
	. "github.com/smartystreets/goconvey/convey"
)

type FakeProcessor struct {
}

type Auth struct {
	authenticated bool
	authorized    bool
	errored       bool
}

func (a *Auth) IsAuthenticated(ctx *Context) (bool, error) {

	if a.errored {
		return false, fmt.Errorf("this is an %s", "error")
	}

	return a.authenticated, nil
}
func (a *Auth) IsAuthorized(ctx *Context) (bool, error) {

	if a.errored {
		return false, fmt.Errorf("this is an %s", "error")
	}

	return a.authorized, nil
}

type testSessionHandler struct {
	sessionCount int
	shouldCalls  int
	block        bool
}

func (h *testSessionHandler) OnPushSessionStart(session *PushSession)            { h.sessionCount++ }
func (h *testSessionHandler) OnPushSessionStop(session *PushSession)             { h.sessionCount-- }
func (h *testSessionHandler) IsAuthenticated(session *PushSession) (bool, error) { return true, nil }
func (h *testSessionHandler) ShouldPush(session *PushSession, event *elemental.Event) (bool, error) {
	h.shouldCalls++
	return !h.block, nil
}

func TestBahamut_NewBahamut(t *testing.T) {

	Convey("Given I create a new Bahamut with no server", t, func() {

		b := NewServer(
			APIServerConfig{
				Disabled: true,
			},
			PushServerConfig{
				Disabled: true,
			},
		)

		Convey("Then apiServer should be nil", func() {
			So(b.(*server).apiServer, ShouldBeNil)
		})

		Convey("Then pushServer should be nil", func() {
			So(b.(*server).pushServer, ShouldBeNil)
		})

		Convey("Then number of routes should be 0", func() {
			So(len(b.(*server).multiplexer.Routes), ShouldEqual, 0)
		})

		Convey("Then pushing an event should not panic", func() {
			So(func() { b.Push(elemental.NewEvent(elemental.EventCreate, NewList())) }, ShouldNotPanic)
		})
	})

	Convey("Given I create a new Bahamut with all servers", t, func() {

		b := NewServer(APIServerConfig{}, PushServerConfig{})

		Convey("Then apiServer should not be nil", func() {
			So(b.(*server).apiServer, ShouldNotBeNil)
		})

		Convey("Then pushServer should be nil", func() {
			So(b.(*server).pushServer, ShouldNotBeNil)
		})

		Convey("Then number of routes should be 0", func() {
			So(len(b.(*server).multiplexer.Routes), ShouldEqual, 7)
		})
	})
}

func TestBahamut_DefaultBahamut(t *testing.T) {

	Convey("Given I create a Bahamut", t, func() {

		b := NewServer(APIServerConfig{}, PushServerConfig{})

		Convey("Then the defaultBahamut should be set", func() {
			So(DefaultServer(), ShouldEqual, b)
		})
	})
}

func TestBahamut_ProcessorRegistration(t *testing.T) {

	Convey("Given I create a Bahamut, aProcessor and an Identity", t, func() {

		p := &FakeProcessor{}
		ident := elemental.MakeIdentity("identity", "random")
		b := NewServer(APIServerConfig{}, PushServerConfig{})

		Convey("When I register it for an identity", func() {

			err := b.RegisterProcessor(p, ident)

			Convey("Then err should be nil", func() {
				So(err, ShouldBeNil)
			})

			Convey("Then it should be registered", func() {
				processor, err := b.ProcessorForIdentity(ident)
				So(processor, ShouldEqual, p)
				So(err, ShouldBeNil)
			})

			Convey("Then the number of registered processors should be 1", func() {
				So(b.ProcessorsCount(), ShouldEqual, 1)
			})
		})

		Convey("When I register it twie", func() {

			b.RegisterProcessor(p, ident)
			err := b.RegisterProcessor(p, ident)

			Convey("Then err should not be nil", func() {
				So(err, ShouldNotBeNil)
			})

			Convey("Then the number of registered processors should be 1", func() {
				So(b.ProcessorsCount(), ShouldEqual, 1)
			})
		})

		Convey("When I unregister it", func() {

			b.RegisterProcessor(p, ident)
			err := b.UnregisterProcessor(ident)

			Convey("Then err should be nil", func() {
				So(err, ShouldBeNil)
			})

			Convey("Then it should be unregistered", func() {
				processor, err := b.ProcessorForIdentity(ident)
				So(processor, ShouldBeNil)
				So(err, ShouldNotBeNil)
			})

			Convey("Then the number of registered processors should be 0", func() {
				So(b.ProcessorsCount(), ShouldEqual, 0)
			})
		})

		Convey("When I unregister it twice", func() {

			b.UnregisterProcessor(ident)
			err := b.UnregisterProcessor(ident)

			Convey("Then err should not be nil", func() {
				So(err, ShouldNotBeNil)
			})
		})
	})
}

func TestBahamut_Authenticator(t *testing.T) {

	Convey("Given I create a new Bahamut", t, func() {

		auth := &Auth{}

		Convey("When I access an Authenticator while there is none", func() {

			b := NewServer(APIServerConfig{}, PushServerConfig{})
			a, err := b.Authenticator()

			Convey("Then the authenticator should be set", func() {
				So(a, ShouldBeNil)
				So(err, ShouldNotBeNil)
			})
		})

		Convey("When I set an Authenticator", func() {

			b := NewServer(APIServerConfig{Authenticator: auth}, PushServerConfig{})
			a, err := b.Authenticator()

			Convey("Then the authenticator should be set", func() {
				So(a, ShouldNotBeNil)
				So(err, ShouldBeNil)
			})
		})
	})
}

func TestBahamut_Authorizer(t *testing.T) {

	Convey("Given I create a new Bahamut", t, func() {

		auth := &Auth{}

		Convey("When I access an Authorizer while there is none", func() {

			b := NewServer(APIServerConfig{}, PushServerConfig{})
			a, err := b.Authorizer()

			Convey("Then the authorizer should be nil", func() {
				So(a, ShouldBeNil)
				So(err, ShouldNotBeNil)
			})
		})

		Convey("When I set an Authorizer", func() {

			b := NewServer(APIServerConfig{Authorizer: auth}, PushServerConfig{})
			a, err := b.Authorizer()

			Convey("Then the Authorizer should be set", func() {
				So(a, ShouldNotBeNil)
				So(err, ShouldBeNil)
			})
		})
	})
}
