// Copyright 2019 Aporeto Inc.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//     http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bahamut

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	opentracing "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/log"
	"go.acuvity.ai/elemental"
)

type stackTaceLogger struct{}

func (e stackTaceLogger) LogValue() slog.Value {
	return slog.StringValue(string(debug.Stack()))
}

type handlerFunc func(*bcontext, config, processorFinderFunc, eventPusherFunc) *elemental.Response

func makeResponse(ctx *bcontext, response *elemental.Response, marshallers map[elemental.Identity]CustomMarshaller) *elemental.Response {

	if ctx.redirect != "" {
		response.Redirect = ctx.redirect
		return response
	}

	var fields []log.Field
	defer func() {
		if span := opentracing.SpanFromContext(ctx.ctx); span != nil {
			span.LogFields(fields...)
			span.SetTag("status.code", response.StatusCode)
		}
	}()

	response.StatusCode = ctx.statusCode
	var statusCodeWasUnset bool
	if response.StatusCode == 0 {
		switch ctx.request.Operation {
		case elemental.OperationInfo:
			response.StatusCode = http.StatusNoContent
		default:
			response.StatusCode = http.StatusOK
		}
		statusCodeWasUnset = true
	}

	if ctx.request.Operation == elemental.OperationRetrieveMany || ctx.request.Operation == elemental.OperationInfo {
		response.Total = ctx.count
		fields = append(fields, (log.Int("count-total", ctx.count)))
	}

	if msgs := ctx.messages; len(msgs) > 0 {
		response.Messages = msgs
		fields = append(fields, (log.Object("messages", msgs)))
	}

	if ctx.next != "" {
		response.Next = ctx.next
	}

	if len(ctx.outputCookies) > 0 {
		response.Cookies = ctx.outputCookies
	}

	if ctx.outputData == nil {
		if statusCodeWasUnset || ctx.statusCode == http.StatusOK {
			response.StatusCode = http.StatusNoContent
		}
		return response
	}

	var requestedFields []string
	if ctx.Request().Headers != nil {
		requestedFields = ctx.Request().Headers["X-Fields"]
	}

	elemental.ResetSecretAttributesValues(ctx.outputData)

	if m, ok := marshallers[ctx.Request().Identity]; ok {
		data, err := m(response, ctx.outputData, nil)
		if err != nil {
			panic(fmt.Sprintf("unable to encode output data using custom marshaller: %s", err))
		}
		response.Data = data
	} else {
		if len(requestedFields) > 0 {

			switch ident := ctx.outputData.(type) {
			case elemental.PlainIdentifiable:
				ctx.outputData = ident.ToSparse(requestedFields...)
			case elemental.PlainIdentifiables:
				ctx.outputData = ident.ToSparse(requestedFields...)
			}
		}

		if err := response.Encode(ctx.OutputData()); err != nil {
			panic(fmt.Sprintf("unable to encode output data: %s", err))
		}
	}

	return response
}

func makeErrorResponse(ctx context.Context, response *elemental.Response, err error, marshallers map[elemental.Identity]CustomMarshaller, errorTransformer func(error) error) *elemental.Response {

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}

	if errorTransformer != nil {
		if perr := errorTransformer(err); perr != nil {
			err = perr
		}
	}

	outError := processError(ctx, err)
	response.StatusCode = outError.Code()

	if response.StatusCode == http.StatusInternalServerError {

		slog.Error("Internal Server Error",
			slog.Any("error", err),
			slog.Any("stacktrace", stackTaceLogger{}),
		)
	}

	if m, ok := marshallers[response.Request.Identity]; ok {
		data, err := m(response, nil, outError)
		if err != nil {
			panic(fmt.Sprintf("unable to encode error using custom marshaller: %s", err))
		}
		response.Data = data
	} else {
		if err := response.Encode(outError); err != nil {
			panic(fmt.Sprintf("unable to encode error: %s", err))
		}
	}

	return response
}

func runDispatcher(ctx *bcontext, r *elemental.Response, d func() error, disablePanicRecovery bool, marshallers map[elemental.Identity]CustomMarshaller, errorTransformer func(error) error) (out *elemental.Response) {

	defer func() {
		if err := handleRecoveredPanic(ctx.ctx, recover(), disablePanicRecovery); err != nil {
			// out is the named returned value. This switches the output of the function.
			out = makeErrorResponse(ctx.ctx, r, err, marshallers, errorTransformer)
		}
	}()

	if err := d(); err != nil {
		return makeErrorResponse(ctx.ctx, r, err, marshallers, errorTransformer)
	}

	return makeResponse(ctx, r, marshallers)
}

func handleRetrieveMany(ctx *bcontext, cfg config, processorFinder processorFinderFunc, pusherFunc eventPusherFunc) (response *elemental.Response) {

	response = elemental.NewResponse(ctx.request)

	if !elemental.IsOperationAllowed(
		cfg.model.modelManagers[ctx.request.Version].Relationships(),
		ctx.request.Identity,
		ctx.request.ParentIdentity,
		elemental.OperationRetrieveMany,
	) {
		return makeErrorResponse(
			ctx.ctx,
			response,
			elemental.NewError(
				"Not allowed",
				"RetrieveMany operation not allowed on "+ctx.request.Identity.Category,
				"bahamut",
				http.StatusMethodNotAllowed,
			),
			cfg.model.marshallers,
			cfg.hooks.errorTransformer,
		)
	}

	return runDispatcher(
		ctx,
		response,
		func() error {
			return dispatchRetrieveManyOperation(
				ctx,
				processorFinder,
				cfg.security.requestAuthenticators,
				cfg.security.authorizers,
				pusherFunc,
				cfg.security.auditer,
			)
		},
		cfg.general.panicRecoveryDisabled,
		cfg.model.marshallers,
		cfg.hooks.errorTransformer,
	)
}

func handleRetrieve(ctx *bcontext, cfg config, processorFinder processorFinderFunc, pusherFunc eventPusherFunc) (response *elemental.Response) {

	response = elemental.NewResponse(ctx.request)

	if !elemental.IsOperationAllowed(
		cfg.model.modelManagers[ctx.request.Version].Relationships(),
		ctx.request.Identity,
		ctx.request.ParentIdentity,
		elemental.OperationRetrieve,
	) {
		return makeErrorResponse(
			ctx.ctx,
			response,
			elemental.NewError(
				"Not allowed",
				"Retrieve operation not allowed on "+ctx.request.Identity.Name, "bahamut",
				http.StatusMethodNotAllowed,
			),
			cfg.model.marshallers,
			cfg.hooks.errorTransformer,
		)
	}

	return runDispatcher(
		ctx,
		response,
		func() error {
			return dispatchRetrieveOperation(
				ctx,
				processorFinder,
				cfg.security.requestAuthenticators,
				cfg.security.authorizers,
				pusherFunc,
				cfg.security.auditer,
			)
		},
		cfg.general.panicRecoveryDisabled,
		cfg.model.marshallers,
		cfg.hooks.errorTransformer,
	)
}

func handleCreate(ctx *bcontext, cfg config, processorFinder processorFinderFunc, pusherFunc eventPusherFunc) (response *elemental.Response) {

	response = elemental.NewResponse(ctx.request)

	if !elemental.IsOperationAllowed(
		cfg.model.modelManagers[ctx.request.Version].Relationships(),
		ctx.request.Identity,
		ctx.request.ParentIdentity,
		elemental.OperationCreate,
	) {
		return makeErrorResponse(
			ctx.ctx,
			response,
			elemental.NewError(
				"Not allowed",
				"Create operation not allowed on "+ctx.request.Identity.Name, "bahamut",
				http.StatusMethodNotAllowed,
			),
			cfg.model.marshallers,
			cfg.hooks.errorTransformer,
		)
	}

	return runDispatcher(
		ctx,
		response,
		func() error {
			return dispatchCreateOperation(
				ctx,
				processorFinder,
				cfg.model.modelManagers[ctx.request.Version],
				cfg.model.unmarshallers[ctx.request.Identity],
				cfg.security.requestAuthenticators,
				cfg.security.authorizers,
				pusherFunc,
				cfg.security.auditer,
				cfg.model.readOnly,
				cfg.model.readOnlyExcludedIdentities,
			)
		},
		cfg.general.panicRecoveryDisabled,
		cfg.model.marshallers,
		cfg.hooks.errorTransformer,
	)
}

func handleUpdate(ctx *bcontext, cfg config, processorFinder processorFinderFunc, pusherFunc eventPusherFunc) (response *elemental.Response) {

	response = elemental.NewResponse(ctx.request)

	if !elemental.IsOperationAllowed(
		cfg.model.modelManagers[ctx.request.Version].Relationships(),
		ctx.request.Identity,
		ctx.request.ParentIdentity,
		elemental.OperationUpdate,
	) {
		return makeErrorResponse(
			ctx.ctx,
			response,
			elemental.NewError(
				"Not allowed",
				"Update operation not allowed on "+ctx.request.Identity.Name, "bahamut",
				http.StatusMethodNotAllowed,
			),
			cfg.model.marshallers,
			cfg.hooks.errorTransformer,
		)
	}

	return runDispatcher(
		ctx,
		response,
		func() error {
			return dispatchUpdateOperation(
				ctx,
				processorFinder,
				cfg.model.modelManagers[ctx.request.Version],
				cfg.model.unmarshallers[ctx.request.Identity],
				cfg.security.requestAuthenticators,
				cfg.security.authorizers,
				pusherFunc,
				cfg.security.auditer,
				cfg.model.readOnly,
				cfg.model.readOnlyExcludedIdentities,
				cfg.model.retriever,
				cfg.model.disableObjectRetrieverForIdentities,
			)
		},
		cfg.general.panicRecoveryDisabled,
		cfg.model.marshallers,
		cfg.hooks.errorTransformer,
	)
}

func handleDelete(ctx *bcontext, cfg config, processorFinder processorFinderFunc, pusherFunc eventPusherFunc) (response *elemental.Response) {

	response = elemental.NewResponse(ctx.request)

	if !elemental.IsOperationAllowed(
		cfg.model.modelManagers[ctx.request.Version].Relationships(),
		ctx.request.Identity,
		ctx.request.ParentIdentity,
		elemental.OperationDelete,
	) {
		return makeErrorResponse(
			ctx.ctx,
			response,
			elemental.NewError(
				"Not allowed",
				"Delete operation not allowed on "+ctx.request.Identity.Name, "bahamut",
				http.StatusMethodNotAllowed,
			),
			cfg.model.marshallers,
			cfg.hooks.errorTransformer,
		)
	}

	return runDispatcher(
		ctx,
		response,
		func() error {
			return dispatchDeleteOperation(
				ctx,
				processorFinder,
				cfg.security.requestAuthenticators,
				cfg.security.authorizers,
				pusherFunc,
				cfg.security.auditer,
				cfg.model.readOnly,
				cfg.model.readOnlyExcludedIdentities,
			)
		},
		cfg.general.panicRecoveryDisabled,
		cfg.model.marshallers,
		cfg.hooks.errorTransformer,
	)
}

func handleInfo(ctx *bcontext, cfg config, processorFinder processorFinderFunc, pusherFunc eventPusherFunc) (response *elemental.Response) {

	response = elemental.NewResponse(ctx.request)

	if !elemental.IsOperationAllowed(
		cfg.model.modelManagers[ctx.request.Version].Relationships(),
		ctx.request.Identity,
		ctx.request.ParentIdentity,
		elemental.OperationInfo,
	) {
		return makeErrorResponse(
			ctx.ctx,
			response,
			elemental.NewError(
				"Not allowed",
				"Info operation not allowed on "+ctx.request.Identity.Category, "bahamut",
				http.StatusMethodNotAllowed,
			),
			cfg.model.marshallers,
			cfg.hooks.errorTransformer,
		)
	}

	return runDispatcher(
		ctx,
		response,
		func() error {
			return dispatchInfoOperation(
				ctx,
				processorFinder,
				cfg.security.requestAuthenticators,
				cfg.security.authorizers,
				pusherFunc,
				cfg.security.auditer,
			)
		},
		cfg.general.panicRecoveryDisabled,
		cfg.model.marshallers,
		cfg.hooks.errorTransformer,
	)
}

func handlePatch(ctx *bcontext, cfg config, processorFinder processorFinderFunc, pusherFunc eventPusherFunc) (response *elemental.Response) {

	response = elemental.NewResponse(ctx.request)

	if !elemental.IsOperationAllowed(
		cfg.model.modelManagers[ctx.request.Version].Relationships(),
		ctx.request.Identity,
		ctx.request.ParentIdentity,
		elemental.OperationPatch,
	) {
		return makeErrorResponse(
			ctx.ctx,
			response,
			elemental.NewError(
				"Not allowed",
				"Patch operation not allowed on "+ctx.request.Identity.Category, "bahamut",
				http.StatusMethodNotAllowed,
			),
			cfg.model.marshallers,
			cfg.hooks.errorTransformer,
		)
	}

	return runDispatcher(
		ctx,
		response,
		func() error {
			return dispatchPatchOperation(
				ctx,
				processorFinder,
				cfg.model.modelManagers[ctx.request.Version],
				cfg.model.unmarshallers[ctx.request.Identity],
				cfg.security.requestAuthenticators,
				cfg.security.authorizers,
				pusherFunc,
				cfg.security.auditer,
				cfg.model.readOnly,
				cfg.model.readOnlyExcludedIdentities,
				cfg.model.retriever,
				cfg.model.disableObjectRetrieverForIdentities,
			)
		},
		cfg.general.panicRecoveryDisabled,
		cfg.model.marshallers,
		cfg.hooks.errorTransformer,
	)
}
