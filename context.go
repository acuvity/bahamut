// Author: Antoine Mercadal
// See LICENSE file for full LICENSE
// Copyright 2016 Aporeto.

package bahamut

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/aporeto-inc/elemental"
	uuid "github.com/satori/go.uuid"
)

// setCommonHeader will write the common HTTP header using the given http.ResponseWriter.
func setCommonHeader(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "X-Requested-With, X-Count-Local, X-Count-Total, X-PageCurrent, X-Page-Size, X-Page-Prev, X-Page-Next, X-Page-First, X-Page-Last, X-Namespace")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Cache-Control, If-Modified-Since, X-Requested-With, X-Count-Local, X-Count-Total, X-PageCurrent, X-Page-Size, X-Page-Prev, X-Page-Next, X-Page-First, X-Page-Last, X-Namespace")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
}

// WriteHTTPError write a Error into a http.ResponseWriter.
//
// This is mostly used by autogenerated code, and you should not need to use it manually.
func WriteHTTPError(w http.ResponseWriter, code int, errs ...*elemental.Error) {
	setCommonHeader(w)
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(errs); err != nil {
		panic("Unable to encode errors. What could I do now?")
	}
}

// A Context contains all information about a current operation.
//
// It contains various Info like the Headers, the current parent identity and ID
// (if any) for a given ReST call, the children identity, and other things like that.
// It also contains information about Pagination, as well as elemental.Idenfiable (or list Idenfiables)
// the user sent through the request.
type Context struct {

	// Info contains various request related information.
	Info *Info

	// Page contains various information about the pagination.
	Page *Page

	// Count contains various information about the counting of objects.
	Count *Count

	// InputData contains the data sent by the client. It can be either a single *elemental.Identifiable
	// or a []*elemental.Identifiable.
	InputData interface{}

	// OutputData contains the information that you want to send back to the user. You will
	// mostly need to set this in your processors.
	OutputData interface{}

	// StatusCode contains the HTTP status code to return.
	// Bahamut will try to guess it, but you can set it yourself.
	StatusCode int

	// Operation contains the current request Operation.
	Operation elemental.Operation

	// UserInfo allows you to store any additional opaque data.
	UserInfo interface{}

	id     string
	events elemental.Events
	errors elemental.Errors
}

// NewContext creates a new *Context for the given Operation.
//
// This is mostly used by autogenerated code, and you should not need to use it manually.
func NewContext(operation elemental.Operation) *Context {

	return &Context{
		Info:      newInfo(),
		Page:      newPage(),
		Count:     newCount(),
		Operation: operation,
		id:        uuid.NewV4().String(),

		errors: elemental.Errors{},
		events: elemental.Events{},
	}
}

// ReadRequest reads information from the given http.Request and populate the Context's Info and Page.
func (c *Context) ReadRequest(req *http.Request) error {

	c.Info.fromRequest(req)
	c.Page.fromValues(req.URL.Query())

	return nil
}

// Identifier returns the unique identifier of the context.
func (c *Context) Identifier() string {

	return c.id
}

// EnqueueEvents enqueues the given event to the Context.
//
// Bahamut will automatically generate events on the currently processed object.
// But if your processor creates other objects alongside with the main one and you want to
// send a push to the user, then you can use this method.
//
// The events you enqueue using EnqueueEvents will be sent in order to the enqueueing, and
// *before* the main object related event.
func (c *Context) EnqueueEvents(events ...*elemental.Event) {

	c.events = append(c.events, events...)
}

// SetEvents set the full list of Errors in the Context.
func (c *Context) SetEvents(events elemental.Events) {

	c.events = events
}

// HasEvents returns true if the context has some custom events.
func (c *Context) HasEvents() bool {

	return len(c.events) > 0
}

// Events returns the current Events.
func (c *Context) Events() elemental.Events {

	return c.events
}

// AddErrors inserts a new Error in the Context.
//
// If the Context contains at least one error, then this error
// will be sent to the user, and OutputData will be discarded.
func (c *Context) AddErrors(err ...*elemental.Error) {

	c.errors = append(c.errors, err...)
}

// SetErrors set the full list of Errors in the Context.
func (c *Context) SetErrors(errs elemental.Errors) {

	c.errors = errs
}

// HasErrors returns true if the context has some errors.
func (c *Context) HasErrors() bool {

	return len(c.errors) > 0
}

// Errors returns the current errors.
func (c *Context) Errors() elemental.Errors {

	return c.errors
}

// WriteResponse writes the final response to the given http.ResponseWriter.
//
// This is mostly used by autogenerated code, and you should not need to use it manually.
func (c *Context) WriteResponse(w http.ResponseWriter) error {

	setCommonHeader(w)

	buffer := &bytes.Buffer{}

	if c.HasErrors() {

		if c.StatusCode == 0 {
			c.StatusCode = c.Errors()[0].Code
		}

		if err := json.NewEncoder(buffer).Encode(c.errors); err != nil {
			return err
		}

	} else {

		if c.StatusCode == 0 {
			if c.Operation == elemental.OperationCreate {
				c.StatusCode = http.StatusCreated
			} else {
				c.StatusCode = http.StatusOK
			}
		}

		if c.Operation == elemental.OperationRetrieveMany {

			c.Page.compute(c.Info.BaseRawURL, c.Info.Parameters, c.Count.Total)

			w.Header().Set("X-Page-Current", strconv.Itoa(c.Page.Current))
			w.Header().Set("X-Page-Size", strconv.Itoa(c.Page.Size))

			w.Header().Set("X-Page-First", c.Page.First)
			w.Header().Set("X-Page-Last", c.Page.Last)

			if pageLink := c.Page.Prev; pageLink != "" {
				w.Header().Set("X-Page-Prev", pageLink)
			}

			if pageLink := c.Page.Next; pageLink != "" {
				w.Header().Set("X-Page-Next", pageLink)
			}

			w.Header().Set("X-Count-Local", strconv.Itoa(c.Count.Current))
			w.Header().Set("X-Count-Total", strconv.Itoa(c.Count.Total))
		}

		if c.OutputData != nil {
			if err := json.NewEncoder(buffer).Encode(c.OutputData); err != nil {
				return err
			}
		}
	}

	w.WriteHeader(c.StatusCode)

	var err error
	if buffer != nil {
		_, err = io.Copy(w, buffer)
	}

	return err
}
